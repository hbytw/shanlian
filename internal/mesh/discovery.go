package mesh

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"slx/internal/buildinfo"
	"slx/internal/envdetect"
	"slx/internal/logx"
	"slx/internal/proto"
)

// 发现参数。集中定义便于调参与测试。
const (
	// DefaultPort 是发现与控制的默认 UDP 端口。
	//
	// 这是全系统唯一在语义上「需要一个大家都知道的固定端口」的地方——
	// 广播发现必须有个落点。仍可通过配置/环境变量覆盖，
	// 但改动后全网节点必须一致，doctor 会检查此项。
	DefaultPort = 51820

	// portScanLimit 是默认端口被占用时最多向上尝试多少个端口。
	portScanLimit = 16

	// maxDatagram 是单个发现报文的最大长度。发现消息都很小。
	maxDatagram = 2048

	// startupBurst 是启动阶段的广播节奏，让成网在 2 秒内完成。
	burstFastGap = 250 * time.Millisecond
	burstFastNum = 6

	// maintainInterval 是稳定后的维护心跳间隔。
	maintainInterval = 10 * time.Second

	// scanGap 是子网扫描时逐 IP 发送的间隔，避免瞬间打爆。
	scanGap = 30 * time.Millisecond

	// pruneAge 是彻底遗忘一个节点的阈值。
	pruneAge = 10 * time.Minute
)

// Discovery 负责在 ZeroTier 接口上发现对端节点。
type Discovery struct {
	nodeID string
	port   int
	zt     []envdetect.ZTIface
	table  *PeerTable
	log    *slog.Logger

	// selfCaps / selfTransports 是本节点要通告出去的能力。
	selfCaps       []string
	selfTransports []string
	// advertiseAddr 是本节点声称的直连地址（可为空，表示只能经 ZT 到达）。
	advertiseAddr string

	conn *net.UDPConn

	mu        sync.Mutex
	lastScan  time.Time
	scanned   bool
	onNewPeer func(*Peer)
	// onData 是数据帧的处理回调（由网关/出口的转发循环注入）。
	// 为空时数据帧会被丢弃 —— 发现专用模式（如 --probe-only）就是这样。
	onData func(pkt []byte, from *net.UDPAddr)
	// 探测消息回调（可为 nil）
	onProbeReq  func(req proto.ProbeReq, from *net.UDPAddr)
	onProbeResp func(resp proto.ProbeResp, from *net.UDPAddr)
	// 解析消息回调（可为 nil）
	onResolveReq  func(req proto.ResolveReq, from *net.UDPAddr)
	onResolveResp func(resp proto.ResolveResp, from *net.UDPAddr)
}

// Options 构造 Discovery。
type Options struct {
	NodeID        string
	Port          int // 0 表示用 DefaultPort 并自动避让占用
	ZTIfaces      []envdetect.ZTIface
	Capabilities  []string
	Transports    []string
	AdvertiseAddr string
	Log           *slog.Logger
	OnNewPeer     func(*Peer)
	// OnData 处理收到的数据帧（载荷是一个完整 IP 包）。
	//
	// 与发现共用同一个套接字：帧类型（DATA / CTRL）已足够区分，
	// 不需要为数据面再开一个端口 —— 少一个端口就少一处防火墙要放行。
	// 为空时数据帧被丢弃（发现专用模式）。
	OnData func(pkt []byte, from *net.UDPAddr)
}

// New 创建 Discovery 并绑定套接字。
func New(o Options) (*Discovery, error) {
	if o.Log == nil {
		o.Log = logx.Discard()
	}
	if len(o.ZTIfaces) == 0 {
		return nil, fmt.Errorf("没有可用的 ZeroTier 接口，无法进行局域网发现\n" +
			"提示: 确认 zerotier-one 正在运行且已加入网络（zerotier-cli listnetworks 应为 OK）")
	}

	d := &Discovery{
		nodeID:         o.NodeID,
		zt:             o.ZTIfaces,
		table:          NewPeerTable(),
		log:            o.Log,
		selfCaps:       o.Capabilities,
		selfTransports: o.Transports,
		advertiseAddr:  o.AdvertiseAddr,
		onNewPeer:      o.OnNewPeer,
		onData:         o.OnData,
	}

	conn, port, err := bindUDP(o.Port)
	if err != nil {
		return nil, err
	}
	d.conn = conn
	d.port = port

	if o.Port != 0 && port != o.Port {
		d.log.Warn("指定端口不可用，已改用其它端口",
			logx.Event("discovery.port_changed"),
			"want", o.Port, "got", port,
			logx.Hint("广播发现要求全网节点端口一致；若你手工指定过端口，请确保其它节点也用同一个"))
	}

	d.log.Info("发现通道已就绪",
		logx.Event("discovery.ready"),
		"port", port,
		"zt", ztSummary(o.ZTIfaces))

	return d, nil
}

// Table 暴露节点表供选路与 CLI 使用。
func (d *Discovery) Table() *PeerTable { return d.table }

// SetProbeHandlers 注册探测消息的收发回调。
//
// 用回调而不是在发现模块里实现探测：发现模块只管「谁在线」，
// 探测与评分属于选路层的职责，分开后两边都能独立测试。
func (d *Discovery) SetProbeHandlers(
	onReq func(req proto.ProbeReq, from *net.UDPAddr),
	onResp func(resp proto.ProbeResp, from *net.UDPAddr),
) {
	d.onProbeReq = onReq
	d.onProbeResp = onResp
}

// SetResolveHandlers 注册解析消息的收发回调。
func (d *Discovery) SetResolveHandlers(
	onReq func(req proto.ResolveReq, from *net.UDPAddr),
	onResp func(resp proto.ResolveResp, from *net.UDPAddr),
) {
	d.onResolveReq = onReq
	d.onResolveResp = onResp
}

// SendControl 向指定对端发送一条控制消息。
func (d *Discovery) SendControl(to *net.UDPAddr, typ proto.MsgType, body any) error {
	payload, err := marshalCtrl(typ, body)
	if err != nil {
		return err
	}
	_, err = d.conn.WriteToUDP(payload, to)
	return err
}

// Port 返回实际使用的端口。
func (d *Discovery) Port() int { return d.port }

// SetAdvertiseAddr 设置本节点对外通告的直连地址。
//
// 需要在 New 之后、Run 之前调用：端口是 New 里绑定后才确定的，
// 而通告地址要带上实际端口。
func (d *Discovery) SetAdvertiseAddr(host string) {
	if host == "" {
		return
	}
	d.advertiseAddr = fmt.Sprintf("%s:%d", host, d.port)
}

// sendBufSize 是数据帧发送缓冲上限。
//
// 取 2KB：内层 MTU 已设为 1380，正常情况下包不会超过这个尺寸。
// 刻意不开到 64KB —— 低端路由器只有几十 MB 内存，
// 池里躺几百个 64KB 缓冲就会直接 OOM。
const sendBufSize = 2048

var sendPool = sync.Pool{
	New: func() any {
		b := make([]byte, sendBufSize)
		return &b
	},
}

// SendData 把原始 IP 包封装成数据帧发往对端。
//
// 零拷贝思路：帧头直接写进池化缓冲的前 4 字节，IP 包紧随其后，
// 一次 WriteToUDP 发出，避免「先分配拼接再发送」的两份内存拷贝。
func (d *Discovery) SendData(dst *net.UDPAddr, pkt []byte) error {
	total := proto.HeaderLen + len(pkt)
	if total > sendBufSize {
		return fmt.Errorf("数据包过大: %d 字节（上限 %d），应受内层 MTU 约束",
			len(pkt), sendBufSize-proto.HeaderLen)
	}

	bp := sendPool.Get().(*[]byte)
	buf := (*bp)[:total]

	hdr := proto.Frame{Type: proto.TypeData, Payload: pkt}
	// Encode 会把 Payload 拷进 dst[4:]，这里 dst 就是池化缓冲
	if _, err := hdr.Encode(buf); err != nil {
		sendPool.Put(bp)
		return err
	}
	_, err := d.conn.WriteToUDP(buf, dst)
	sendPool.Put(bp)
	return err
}

// PeerAddr 返回某节点当前可用的传输地址。
//
// 优先 ZeroTier 地址（实测最稳、无需公网可达）；
// 若该节点通告了直连地址则一并返回，供 direct 传输使用。
func (d *Discovery) PeerAddr(nodeID string) (*net.UDPAddr, error) {
	p, ok := d.table.Get(nodeID)
	if !ok {
		return nil, fmt.Errorf("节点 %s 不在拓扑中", nodeID)
	}
	if p.ZTAddr != nil {
		return p.ZTAddr, nil
	}
	if p.AdvertiseAddr != "" {
		if a, err := net.ResolveUDPAddr("udp", p.AdvertiseAddr); err == nil {
			return a, nil
		}
	}
	return nil, fmt.Errorf("节点 %s 没有可用地址", nodeID)
}

// bindUDP 绑定 0.0.0.0 上的 UDP 端口，并打开 SO_BROADCAST。
//
// 绑 0.0.0.0 而不是绑 ZT 网卡地址：Linux 上绑到具体单播地址的套接字
// 收不到发往广播地址的报文，而广播发现必须能收到。
// 出方向靠路由表把 192.168.99.255 这类地址送到 ZT 网卡，无需显式指定接口。
func bindUDP(want int) (*net.UDPConn, int, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var setErr error
			if err := c.Control(func(fd uintptr) {
				setErr = setSocketOptions(fd)
			}); err != nil {
				return err
			}
			return setErr
		},
	}

	ctx := context.Background()
	if want != 0 {
		pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", want))
		if err != nil {
			return nil, 0, fmt.Errorf("绑定 UDP %d 失败: %w", want, err)
		}
		return pc.(*net.UDPConn), want, nil
	}

	for p := DefaultPort; p < DefaultPort+portScanLimit; p++ {
		pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			return pc.(*net.UDPConn), p, nil
		}
	}
	return nil, 0, fmt.Errorf("端口 %d~%d 全部被占用，无法建立发现通道",
		DefaultPort, DefaultPort+portScanLimit-1)
}

// Run 启动发现循环，直到 ctx 结束。
func (d *Discovery) Run(ctx context.Context) error {
	defer d.conn.Close()

	// ctx 结束时立刻关闭套接字，让下面的 ReadFromUDP 立即返回。
	//
	// 不加这段的话，退出要等满一个读超时（2 秒），
	// 表现为 systemctl stop 之后进程还活两秒 —— 自动化脚本里会误以为没死透。
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = d.conn.Close()
		case <-shutdown:
		}
	}()
	defer close(shutdown)

	readErr := make(chan error, 1)
	go func() { readErr <- d.readLoop(ctx) }()

	// 启动期密集广播，让成网尽快完成
	for i := 0; i < burstFastNum; i++ {
		d.broadcastHello()
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(burstFastGap):
		}
	}

	// 广播没喊到人 → 立刻退化为子网扫描。
	//
	// 实测发现（2026-11-04 三节点真机）：ZT 的广播并不对称。
	// 旁路由广播能被各 VPS 收到，但 VPS 自己广播时其它节点未必收得到
	// （取决于 ZT 的多播转发角色与拓扑）。
	// 所以扫描不是「可选兜底」，而是与广播并行的第二条发现路径，必须尽早启动。
	if d.table.Len() == 0 {
		d.log.Info("广播未发现任何节点，开始子网扫描",
			logx.Event("discovery.fallback_scan"),
			logx.Hint("ZT 广播在部分拓扑下不对称，扫描是并行发现路径"))
		d.scanSubnet()
	}

	// 之后进入维护节奏
	maint := time.NewTicker(maintainInterval)
	defer maint.Stop()
	prune := time.NewTicker(30 * time.Second)
	defer prune.Stop()
	// 还是一个节点都没有时定期重扫（对端启动晚、ZT 刚上线等场景）
	retryScan := time.NewTicker(30 * time.Second)
	defer retryScan.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			if err != nil && ctx.Err() == nil {
				return err
			}
			return nil
		case <-retryScan.C:
			if d.table.Len() == 0 {
				d.log.Info("仍未发现节点，重试子网扫描", logx.Event("discovery.rescan"))
				d.scanSubnet()
			}
		case <-maint.C:
			d.broadcastHello()
			// 对已知节点额外单播一轮。
			//
			// 实测发现 ZT 广播并不对称：旁路由的广播能到各 VPS，
			// 但 VPS 的广播其它节点未必收得到。只靠广播的话，
			// 某些节点的 last_seen 会一直不刷新，落进「迟滞」态，
			// 进而被选路判为不可用 —— 明明链路是通的。
			// 单播心跳是对称的，能保证双方互相续命。
			d.sendUnicastHellos()
		case <-prune.C:
			if n := d.table.Prune(time.Now(), pruneAge); n > 0 {
				d.log.Debug("清理长期失联节点", logx.Event("discovery.prune"), "count", n)
			}
		}
	}
}

// readLoop 收包并分发。返回非 nil 表示致命错误。
func (d *Discovery) readLoop(ctx context.Context) error {
	buf := make([]byte, maxDatagram)
	for {
		if ctx.Err() != nil {
			return nil
		}
		// 设读超时，让 ctx 取消能及时生效
		_ = d.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, raddr, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		d.handleDatagram(buf[:n], raddr)
	}
}

// handleDatagram 解析一个报文并按类型分发。
//
// 关键兼容性要求：未知消息类型只回 NACK，绝不因为「不认识」就中断。
func (d *Discovery) handleDatagram(b []byte, from *net.UDPAddr) {
	f, err := proto.Decode(b)
	if err != nil {
		d.log.Debug("收到无法解析的报文",
			logx.Event("discovery.bad_frame"), "from", from.String(), logx.FieldErr, err.Error())
		return
	}
	if f.Type != proto.TypeCtrl {
		// 数据面报文：交给转发循环
		if f.Type == proto.TypeData {
			if d.onData != nil {
				d.onData(f.Payload, from)
			}
			return
		}
		// 其它类型（KEEPALIVE / PROBE）M2 阶段暂不处理
		return
	}

	env, err := proto.UnmarshalEnvelope(f.Payload)
	if err != nil {
		d.log.Debug("控制消息解析失败",
			logx.Event("discovery.bad_ctrl"), "from", from.String(), logx.FieldErr, err.Error())
		return
	}

	switch env.Type {
	case proto.MsgHello:
		d.onHello(env.Body, from)
	case proto.MsgHelloAck:
		d.onHelloAck(env.Body, from)
	case proto.MsgHeartbeat:
		d.onHeartbeat(env.Body, from)
	case proto.MsgProbeReq:
		var req proto.ProbeReq
		if jsonUnmarshal(env.Body, &req) == nil && d.onProbeReq != nil {
			d.onProbeReq(req, from)
		}
	case proto.MsgProbeResp:
		var resp proto.ProbeResp
		if jsonUnmarshal(env.Body, &resp) == nil && d.onProbeResp != nil {
			d.onProbeResp(resp, from)
		}
	case proto.MsgResolveReq:
		var req proto.ResolveReq
		if jsonUnmarshal(env.Body, &req) == nil && d.onResolveReq != nil {
			d.onResolveReq(req, from)
		}
	case proto.MsgResolveResp:
		var resp proto.ResolveResp
		if jsonUnmarshal(env.Body, &resp) == nil && d.onResolveResp != nil {
			d.onResolveResp(resp, from)
		}
	case proto.MsgNack:
		// 对端不认识我们发的消息：说明版本差异，记日志即可
		var n proto.Nack
		if jsonUnmarshal(env.Body, &n) == nil {
			d.log.Debug("对端回了 NACK",
				logx.Event("discovery.nack"), "from", from.String(),
				"about", string(n.AboutType), "code", n.Code, "reason", n.Reason)
		}
	default:
		d.sendNack(from, env.Type, proto.CodeUnknownMessageType, "本节点不认识该消息类型")
	}
}

// onHello 处理对端广播的 HELLO：登记节点并单播回应。
//
// 回应走单播而不是广播 —— 否则 N 个节点互相同步回应会形成广播风暴。
func (d *Discovery) onHello(body []byte, from *net.UDPAddr) {
	var h proto.Hello
	if err := jsonUnmarshal(body, &h); err != nil {
		d.log.Debug("hello 解析失败", logx.Event("discovery.bad_hello"), logx.FieldErr, err.Error())
		return
	}
	if h.NodeID == d.nodeID {
		return // 自己的广播回环
	}

	agreed, err := proto.Negotiate(buildinfo.ProtoMin, buildinfo.ProtoMax, h.ProtoMin, h.ProtoMax)
	if err != nil {
		d.log.Warn("与对端协议版本无交集，跳过该节点",
			logx.Event("discovery.version_mismatch"),
			logx.FieldNodeID, h.NodeID,
			"peer_version", fmt.Sprintf("[%d,%d]", h.ProtoMin, h.ProtoMax),
			logx.Hint("升级其中一端；主版本不一致时无法互通"))
		d.sendNack(from, proto.MsgHello, proto.CodeUnsupportedVersion, err.Error())
		return
	}

	d.upsertFromHello(h, from, agreed)
	d.sendHelloAck(from, h, agreed)
}

// onHelloAck 处理对端对我们 HELLO 的回应。
func (d *Discovery) onHelloAck(body []byte, from *net.UDPAddr) {
	var a proto.HelloAck
	if err := jsonUnmarshal(body, &a); err != nil {
		d.log.Debug("hello_ack 解析失败", logx.Event("discovery.bad_ack"), logx.FieldErr, err.Error())
		return
	}
	if a.NodeID == d.nodeID {
		return
	}
	if !a.Accepted {
		d.log.Warn("对端拒绝建立关系",
			logx.Event("discovery.rejected"),
			logx.FieldNodeID, a.NodeID, "code", a.RejectCode, "reason", a.RejectReason)
		return
	}

	h := proto.Hello{
		NodeID:        a.NodeID,
		SWVersion:     a.SWVersion,
		ProtoMin:      a.ProtoMin,
		ProtoMax:      a.ProtoMax,
		Capabilities:  a.Capabilities,
		Transports:    a.Transports,
		Region:        a.Region,
		AdvertiseAddr: a.AdvertiseAddr,
	}
	d.upsertFromHello(h, from, a.AgreedProto)
}

// onHeartbeat 处理心跳：只更新负载信息，不改变节点身份。
func (d *Discovery) onHeartbeat(body []byte, from *net.UDPAddr) {
	var hb proto.Heartbeat
	if err := jsonUnmarshal(body, &hb); err != nil {
		return
	}
	d.table.Upsert(hb.NodeID, func(p *Peer, isNew bool) {
		p.Load = hb.Load
		p.ActiveFlows = hb.ActiveFlows
		p.UpBps = hb.UpBps
		p.DownBps = hb.DownBps
		if p.ZTAddr == nil {
			p.ZTAddr = from
		}
	})
}

// upsertFromHello 把 HELLO 信息写进节点表。
func (d *Discovery) upsertFromHello(h proto.Hello, from *net.UDPAddr, agreed int) {
	isNew := d.table.Upsert(h.NodeID, func(p *Peer, isNew bool) {
		p.ZTAddr = from
		p.AdvertiseAddr = h.AdvertiseAddr
		p.SWVersion = h.SWVersion
		p.Capabilities = h.Capabilities
		p.Transports = h.Transports
		p.Region = h.Region
		p.AgreedProto = agreed
	})

	if isNew {
		d.log.Info("发现新节点",
			logx.Event("discovery.peer_found"),
			logx.FieldNodeID, h.NodeID,
			"addr", from.String(),
			"caps", strings.Join(h.Capabilities, ","),
			"transports", strings.Join(h.Transports, ","),
			"sw", h.SWVersion,
			"proto", agreed)
		if d.onNewPeer != nil {
			if p, ok := d.table.Get(h.NodeID); ok {
				d.onNewPeer(p)
			}
		}
	}
}

// sendHello 向所有 ZT 接口的广播地址喊话。
func (d *Discovery) sendHello() {
	payload, err := d.buildHello()
	if err != nil {
		d.log.Warn("构造 hello 失败", logx.Event("discovery.build_failed"), logx.FieldErr, err.Error())
		return
	}
	for _, z := range d.zt {
		bc, err := broadcastAddr(z.CIDR)
		if err != nil {
			continue
		}
		dst := &net.UDPAddr{IP: bc, Port: d.port}
		if _, err := d.conn.WriteToUDP(payload, dst); err != nil {
			d.log.Debug("广播失败", logx.Event("discovery.broadcast_failed"),
				"dst", dst.String(), logx.FieldErr, err.Error())
		}
	}
}

// broadcastHello 是 sendHello 的日志包装，只会在首次广播时打日志。
func (d *Discovery) broadcastHello() {
	d.mu.Lock()
	first := !d.scanned
	d.scanned = true
	d.mu.Unlock()

	d.sendHello()
	if first {
		addrs := make([]string, 0, len(d.zt))
		for _, z := range d.zt {
			if bc, err := broadcastAddr(z.CIDR); err == nil {
				addrs = append(addrs, bc.String())
			}
		}
		d.log.Info("开始广播发现",
			logx.Event("discovery.broadcast"), "targets", strings.Join(addrs, ","), "port", d.port)
	}
}

// sendHelloAck 单播回应对方。
func (d *Discovery) sendHelloAck(to *net.UDPAddr, h proto.Hello, agreed int) {
	ack := proto.HelloAck{
		NodeID:          d.nodeID,
		SWVersion:       buildinfo.Version,
		ProtoMin:        buildinfo.ProtoMin,
		ProtoMax:        buildinfo.ProtoMax,
		AgreedProto:     agreed,
		Capabilities:    d.selfCaps,
		CapabilityInter: proto.IntersectCaps(d.selfCaps, h.Capabilities),
		Transports:      d.selfTransports,
		AdvertiseAddr:   d.advertiseAddr,
		Accepted:        true,
	}
	payload, err := marshalCtrl(proto.MsgHelloAck, ack)
	if err != nil {
		return
	}
	if _, err := d.conn.WriteToUDP(payload, to); err != nil {
		d.log.Debug("回 hello_ack 失败", logx.Event("discovery.ack_failed"),
			"dst", to.String(), logx.FieldErr, err.Error())
	}
}

// sendNack 告知对端我们处理不了它的消息。
//
// 注意：NACK 只报告「不认识这个类型」，不断开连接、不拉黑节点。
func (d *Discovery) sendNack(to *net.UDPAddr, about proto.MsgType, code int, reason string) {
	n := proto.Nack{
		AboutType:      about,
		Code:           code,
		Reason:         reason,
		SupportedTypes: proto.KnownMsgTypes(),
	}
	payload, err := marshalCtrl(proto.MsgNack, n)
	if err != nil {
		return
	}
	_, _ = d.conn.WriteToUDP(payload, to)
}

// buildHello 构造本节点的 hello 报文。
func (d *Discovery) buildHello() ([]byte, error) {
	return marshalCtrl(proto.MsgHello, proto.Hello{
		NodeID:        d.nodeID,
		SWVersion:     buildinfo.Version,
		ProtoMin:      buildinfo.ProtoMin,
		ProtoMax:      buildinfo.ProtoMax,
		Capabilities:  d.selfCaps,
		Transports:    d.selfTransports,
		AdvertiseAddr: d.advertiseAddr,
	})
}

// sendUnicastHellos 向所有已知节点单播 HELLO。
//
// 这既是保活，也是恢复手段：节点换了端口/地址后，
// 下一次单播会打到旧地址失败，但对方的广播仍能把它带回来。
func (d *Discovery) sendUnicastHellos() {
	payload, err := d.buildHello()
	if err != nil {
		return
	}
	for _, p := range d.table.Snapshot() {
		if p.ZTAddr == nil {
			continue
		}
		if _, err := d.conn.WriteToUDP(payload, p.ZTAddr); err != nil {
			d.log.Debug("单播心跳失败", logx.Event("discovery.unicast_failed"),
				"peer", p.NodeID, logx.FieldErr, err.Error())
		}
	}
}

// scanSubnet 对 ZT 子网做单播扫描。
//
// 每轮最多扫一遍，且有最小间隔，避免在节点很多或网络很慢时刷屏。
func (d *Discovery) scanSubnet() {
	d.mu.Lock()
	if d.lastScan.After(time.Now().Add(-time.Minute)) {
		d.mu.Unlock()
		return
	}
	d.lastScan = time.Now()
	d.mu.Unlock()

	payload, err := d.buildHello()
	if err != nil {
		return
	}

	for _, z := range d.zt {
		hosts, err := subnetHosts(z.CIDR, 254)
		if err != nil {
			continue
		}
		for _, ip := range hosts {
			if ip.Equal(net.ParseIP(z.IP)) {
				continue
			}
			dst := &net.UDPAddr{IP: ip, Port: d.port}
			_, _ = d.conn.WriteToUDP(payload, dst)
			select {
			case <-time.After(scanGap):
			}
		}
	}
	d.log.Info("子网扫描完成", logx.Event("discovery.scan_done"), "found", d.table.Len())
}

// —— 地址工具 ——

// broadcastAddr 从 CIDR 计算子网广播地址。
func broadcastAddr(cidr string) (net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("仅支持 IPv4: %s", cidr)
	}
	bc := make(net.IP, 4)
	binary.BigEndian.PutUint32(bc, binary.BigEndian.Uint32(v4)|^binary.BigEndian.Uint32(ipnet.Mask))
	return bc, nil
}

// subnetHosts 枚举子网内可用主机地址（跳过网络号与广播地址）。
func subnetHosts(cidr string, limit int) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("仅支持 IPv4: %s", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	if bits-ones > 16 {
		return nil, fmt.Errorf("子网过大，拒绝扫描: %s", cidr)
	}

	base := binary.BigEndian.Uint32(ipnet.IP.To4())
	total := uint32(1)<<uint(bits-ones) - 1 // 广播地址
	var out []net.IP
	for i := uint32(1); i < total && len(out) < limit; i++ {
		b := make(net.IP, 4)
		binary.BigEndian.PutUint32(b, base+i)
		out = append(out, b)
	}
	return out, nil
}

func ztSummary(zt []envdetect.ZTIface) string {
	parts := make([]string, 0, len(zt))
	for _, z := range zt {
		parts = append(parts, fmt.Sprintf("%s(%s)", z.Name, z.CIDR))
	}
	return strings.Join(parts, ",")
}

// marshalCtrl 把控制消息打包成二进制帧。
func marshalCtrl(t proto.MsgType, body any) ([]byte, error) {
	env, err := proto.MarshalEnvelope(t, body)
	if err != nil {
		return nil, err
	}
	f := proto.Frame{Type: proto.TypeCtrl, Payload: env}
	buf := make([]byte, f.EncodedLen())
	if _, err := f.Encode(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
