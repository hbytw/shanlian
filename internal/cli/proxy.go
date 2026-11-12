package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"slx/internal/config"
	"slx/internal/dispatch"
	"slx/internal/dnsfwd"
	"slx/internal/envdetect"
	"slx/internal/identity"
	"slx/internal/logx"
	"slx/internal/mesh"
	"slx/internal/netcfg"
	"slx/internal/netstack"
	"slx/internal/picker"
	"slx/internal/proto"
	"slx/internal/relay"
	"slx/internal/tun"
)

// proxyPlane 是 proxy 模式的数据面。
//
// 它与 packet 模式的根本区别：不在网关与出口之间搬运 IP 包，
// 而是让 gvisor 在网关侧终结连接，再去出口 agent 上开一条对应的中继。
// 因此本结构里没有 forward.Plane、没有用户态 NAT。
type proxyPlane struct {
	dev      *tun.Device
	sys      *netcfg.System
	dispatch *dispatchState
	// ztGuard 是 ZT 环路保护规则。
	//
	// 它的生命周期独立于 dispatch：分流被暂停时它必须留下，
	// 否则出口 agent 连不回网关（死锁）。见 dispatch.ZTGuard。
	ztGuard *dispatch.ZTGuard
	picker  *picker.Selector
	gateway *relay.Gateway
	stack   *netstack.Stack
	// dns 是 DNS 转发器（未配置 dns_listen 时为 nil）
	dns *dnsfwd.Server
	// bw 是带宽被动估算器
	bw *bwTracker

	// connector 用于 ICMP echo：需要按目标选出口再请 agent 探测
	connector *proxyConnector

	// icmpSem 限制同时在途的 ping 数，避免被 ping 洪水打爆
	icmpSem chan struct{}

	// 计数
	tunRead     atomic.Int64
	tunWrite    atomic.Int64
	tunWriteErr atomic.Int64
	icmpReplied atomic.Int64
	icmpDropped atomic.Int64

	cleanup func()
}

// proxyConnector 实现 netstack.Connector：把「去往 dst:port 的连接」落到某个出口 agent。
type proxyConnector struct {
	gw     *relay.Gateway
	picker *picker.Selector
	disc   *mesh.Discovery
	selfID string
	// fixed 非空时强制用该出口（配置里写了 egress）
	fixed string
	log   *slog.Logger
	// bw 被动统计每条中继的吞吐，作为带宽估计
	bw *bwTracker
}

// candidates 返回可用的出口节点。
//
// **来源是 agent 连接池，而不是发现表**。
//
// 这一点是 proxy 模式的核心优势：agent 能拨进来，本身就证明它可达且在线，
// 不需要 ZeroTier 提供通路，也不需要发现流程先认出它。
// 早先版本从发现表过滤，结果是「agent 明明连着却没被选到」——
// 因为发现走的是 ZT，而在没有 ZT 的环境里根本没有 peer 记录。
//
// 发现表只在「有记录时」用于补充评分（picker 的 L1 指标来自它），
// 不作为是否可用的判据。
func (c *proxyConnector) candidates() []string {
	// ★ 用 UsableAgentIDs 而不是 AgentIDs ✅✅
	//
	// 「在线」≠「能用」✅✅
	// 一台 agent 的通道可能全被熔断或正在重建 ✅✅
	// ⇒ 它仍在 AgentIDs 里、评分仍然很高 ✅⚠️
	// ⇒ 但每条连接都会失败 ⇒ 它是个黑洞代理 ✅⚠️
	//
	// ⇒ 把它们排除出候选集 ✅✅
	//   （2026-11-12 审计新增 ✅⚠️）
	agents := c.gw.UsableAgentIDs()
	if len(agents) == 0 {
		return nil
	}
	if c.fixed != "" {
		if c.gw.HasAgent(c.fixed) {
			return []string{c.fixed}
		}
		return nil
	}
	out := make([]string, 0, len(agents))
	for _, id := range agents {
		if id == c.selfID {
			continue // 网关自己不给自己当出口
		}
		out = append(out, id)
	}
	return out
}

// pick 选出这条连接该走的出口。
func (c *proxyConnector) pick(dst net.IP) (string, bool) {
	cands := c.candidates()
	if len(cands) == 0 {
		return "", false
	}
	if len(cands) == 1 {
		return cands[0], true
	}
	d := c.picker.Pick(dst, "", cands, time.Now())
	if d.Egress == "" {
		return cands[0], true // 评分缺失时兜底，不要让连接失败
	}
	return d.Egress, true
}

// connect 是 TCP/UDP 的公共路径。
func (c *proxyConnector) connect(dst net.IP, port uint16, transport uint8) (net.Conn, bool, error) {
	nodeID, ok := c.pick(dst)
	if !ok {
		// 没有可用 agent：静默（不回 RST）。客户端会重试，
		// 等 agent 上线后自动恢复；回 RST 会让浏览器直接报错。
		return nil, false, errors.New("没有在线的出口 agent")
	}
	req := proto.ConnectReq{
		Net:       0,
		Transport: transport,
		Addr:      dst.String(),
		Port:      port,
	}
	if dst.To4() == nil {
		req.Net = 1
	}
	conn, err := c.gw.Open(nodeID, req)
	if err != nil {
		reportConnectFailure(c.picker, c.log, nodeID, err, time.Now())
		var ce *relay.ConnectError
		if errors.As(err, &ce) {
			return nil, ce.Reset, err
		}
		return nil, false, err
	}
	c.picker.ReportHealth(nodeID, true, time.Now())
	// 套一层字节计数：连接关闭时结算吞吐，喂给选路器的带宽维度。
	// 被动统计不产生额外流量，代价是冷启动时没有带宽数据（走 fallback）。
	return wrapCounted(conn, nodeID, c.bw), false, nil
}

// reportConnectFailure 把一次建连失败分成两类，并分别记账。
//
// # 为什么必须分开
//
//	· **出口自己坏了**（连不上 agent / agent 会话断）→ 计入熔断；
//	· **出口好得很，只是这个目标打不开**（被墙 / 网站挂了 / DNS 失败）→ 不计入熔断。
//
// 混在一起的后果是死亡螺旋：用户访问几个打不开的站点
// → 出口被判坏并熔断 → 可用出口更少 → 更多请求失败 → 更多出口被熔断。
// 线上表现就是「频繁熔断 + 网络基本不可用」，
// 而且只要只剩一台出口，全家直接断网。
//
// 目标不可达只记一个计数（供状态页观察），**绝不推进熔断计数**。
func reportConnectFailure(pick *picker.Selector, log *slog.Logger, nodeID string, err error, now time.Time) {
	if pick == nil {
		return
	}
	var ce *relay.ConnectError
	if errors.As(err, &ce) && ce.TargetUnreachable {
		pick.ReportTargetFailure(nodeID)
		if log != nil {
			log.Debug("目标不可达（不计入熔断）",
				logx.FieldEvent, "relay.target_unreachable",
				logx.FieldNodeID, shortID(nodeID),
				logx.FieldErr, ce.Reason)
		}
		return
	}
	// 「这个出口一条可用通道都不剩」是**通道级熔断**的结果，
	// 不是出口本身坏了。把它算成出口链路失败，会让通道级熔断
	// 越级把 agent 级熔断也拖下水 ⇒ 一条坏通道拖死整台出口，
	// DNS 之类的旁路跟着一起断（现场事故：出口集体熔断 + DNS 失败 523 次）。
	//
	// 这里只记录，不推进 agent 级熔断计数。
	if errors.Is(err, relay.ErrNoUsableChannel) {
		if log != nil {
			log.Debug("该出口没有可用通道（不计入出口熔断）",
				logx.FieldEvent, "relay.no_usable_channel",
				logx.FieldNodeID, shortID(nodeID))
		}
		return
	}
	pick.ReportHealthWithReason(nodeID, false,
		"出口链路失败："+probeErrPhrase(err), now)
}

func (c *proxyConnector) ConnectTCP(dst net.IP, port uint16) (net.Conn, bool, error) {
	return c.connect(dst, port, proto.ConnectTCP)
}

func (c *proxyConnector) ConnectUDP(dst net.IP, port uint16) (net.Conn, bool, error) {
	return c.connect(dst, port, proto.ConnectUDP)
}

// setupProxyPlane 建立 proxy 模式数据面。
func setupProxyPlane(
	ctx context.Context,
	log *slog.Logger,
	c *config.Config,
	env *envdetect.Env,
	nodeID string,
	disc *mesh.Discovery,
) (*proxyPlane, error) {

	tunName := c.EffTunName()

	// —— 1. 环路检查（与 packet 模式同一套，必须在建 TUN 之前）——
	sys := netcfg.NewSystem(nil)
	problems, err := sys.CheckLoopRisk(netcfg.GuardSpec{
		TunnelDev: tunName,
		InnerCIDR: c.EffInnerCIDR(),
		ZTSubnets: ztNetworks(env),
	})
	if err != nil {
		log.Warn("环路检查未能执行", logx.FieldEvent, "guard.check_failed",
			logx.FieldErr, err.Error(),
			logx.FieldHint, "通常是 ip 命令不可用；请确认 iproute 已安装")
	}
	for _, p := range problems {
		if p.Fatal {
			return nil, fmt.Errorf("检测到路由环路风险，拒绝启动:\n%s", p.String())
		}
		log.Warn("发现可疑配置", logx.FieldEvent, "guard.warn", "detail", p.What,
			logx.FieldHint, p.Hint)
	}

	// —— 2. 建 TUN ——
	dev, err := tun.Open(tunName, tun.DefaultMTU)
	if err != nil {
		return nil, err
	}
	if err := dev.ConfigureNoAddr(); err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("配置 TUN 失败: %w", err)
	}
	log.Info("TUN 已就绪（proxy 模式）", logx.FieldEvent, "tun.ready",
		"dev", dev.Name(), "mtu", dev.MTU(),
		"detail", "用户态协议栈终结连接，不经内核转发")

	// 内核必须能转发：LAN 的包要先进 slx0（TUN），回程包还要从 slx0 转回 LAN。
	// proxy 模式虽然没有内核 NAT，但「把包送进 TUN」和「把 TUN 出来的包送回 LAN」
	// 这两步仍然走内核转发路径，ip_forward 与 FORWARD 规则一个都不能少。
	if changed, ferr := sys.EnsureIPForward(); ferr != nil {
		log.Warn("开启 IP 转发失败", logx.FieldEvent, "netcfg.ip_forward",
			logx.FieldErr, ferr.Error(),
			logx.FieldHint, "不开时表现为客户端连不上任何境外站点")
	} else if changed {
		log.Info("已开启 IP 转发", logx.FieldEvent, "netcfg.ip_forward")
	}
	for _, outgoing := range []bool{false, true} {
		if _, aerr := sys.EnsureForwardAccept(dev.Name(), outgoing); aerr != nil {
			log.Warn("放行 FORWARD 失败", logx.FieldEvent, "netcfg.forward_fail",
				logx.FieldErr, aerr.Error(),
				logx.FieldHint, "若网卡间转发被防火墙拦截，表现为分流规则装上了但没流量")
		}
	}

	// —— 3. 选路器 ——
	//
	// 必须先于网关创建：网关的接入/断开回调要用它清状态。
	pick := picker.NewSelector(picker.DefaultWeights())

	// —— 4. 起 agent 反向连接监听 ——
	// 网关自己的 node_id：直连通道里网关是拨号方，
	// 要向 agent 介绍自己（ClientHandshake）。
	gwNodeID, err := identity.DeriveNodeID(c.KeyBytes())
	if err != nil {
		log.Warn("网关身份派生失败，直连通道不可用",
			logx.FieldEvent, "identity.derive_failed",
			logx.FieldErr, err.Error(),
			logx.FieldHint, "确认 /etc/machine-id 或至少一块物理网卡存在")
		gwNodeID = ""
	}

	// 握手允许的时钟偏差（默认 60 秒，实测各节点差 ≤ 8 秒）。
	relay.SetClockSkew(c.EffRelayClockSkew())

	// 通道级熔断阈值（默认 5 次连续失败）。
	relay.SetChannelProbeFailures(c.EffRelayChannelFailures())

	gw := relay.NewGateway(relay.GatewayOptions{
		Key:  c.KeyBytes(),
		Addr: c.EffAgentListen(),
		Log:  log,
		// —— 通道相关 ——
		//
		// 传输模式默认 zt：行为与改造前完全一致，一键可回退。
		// tls  = 只走公网直连；auto = 按评分自动选。
		Transport:     c.EffRelayTransport(),
		NodeID:        gwNodeID,
		DirectConns:   c.EffRelayTLSConns(),
		DialTimeout:   c.EffRelayTLSConnectTimeout(),
		ProbeInterval: c.EffRelayTLSProbeInterval(),
		ProbeTimeout:  c.EffRelayProbeTimeout(),
		PinnedTLS:     c.EffRelayTLSPin(),
		CertFile:      c.EffRelayTLSCert(),
		// agent 接入 = 全新开始：把它的旧熔断状态、旧指标、旧路由条目全清掉。
		//
		// 不清的后果很具体：熔断状态按 node_id 记，agent 重连时 node_id 不变，
		// 它会带着上一段的冷却期（最长 10 分钟）回来，继续被排除在候选之外 ——
		// 表现为「刚回来又熔断」，退避还越滚越长，出口永远回不来。
		OnAgentJoin: func(nodeID string, since time.Time) {
			pick.ResetEgress(nodeID)
			pick.MarkOnline(nodeID, since)
			log.Info("出口已上线，熔断状态已复位",
				logx.FieldEvent, "picker.egress_reset",
				logx.FieldNodeID, shortID(nodeID),
				"phase", "join",
				"uptime", time.Since(since).Round(time.Second).String())
		},
		// agent 掉线：同样清干净，别让它的状态一直挂在内存里，
		// 也别让它带着冷却期回来。
		OnAgentLeave: func(nodeID string, uptime time.Duration) {
			pick.ResetEgress(nodeID)
			log.Info("出口已下线，已清除其熔断与测量状态",
				logx.FieldEvent, "picker.egress_reset",
				logx.FieldNodeID, shortID(nodeID),
				"phase", "leave",
				"uptime", uptime.Round(time.Second).String())
		},
	})
	gwErr := make(chan error, 1)
	go func() { gwErr <- gw.Run(ctx) }()

	// —— 5. 用户态协议栈 ——
	connector := &proxyConnector{
		gw:     gw,
		picker: pick,
		disc:   disc,
		selfID: nodeID,
		fixed:  c.Egress,
		log:    log,
		bw:     newBWTracker(),
	}
	stk, err := netstack.New(netstack.Options{
		MTU:       uint32(dev.MTU()),
		Connector: connector,
		Log:       log,
	})
	if err != nil {
		gw.Close()
		_ = dev.Close()
		return nil, err
	}

	pp := &proxyPlane{
		dev:       dev,
		sys:       sys,
		picker:    pick,
		gateway:   gw,
		stack:     stk,
		connector: connector,
		icmpSem:   make(chan struct{}, icmpMaxPending),
		bw:        connector.bw,
	}

	// —— 6. 准备分流 ——
	ds, derr := setupDispatch(ctx, log, c, env, c.EffInnerCIDR(), dev.Name())
	if derr != nil {
		log.Warn("分流未启用，继续以不分流模式运行",
			logx.FieldEvent, "dispatch.prepare_failed",
			logx.FieldLayer, logx.LayerDispatch,
			logx.FieldErr, derr.Error(),
			logx.FieldHint, "数据面本身仍然可用；只是不会自动接管境外流量")
	}
	if ds != nil {
		ds.refresh = make(chan struct{}, 1)
	}
	pp.dispatch = ds

	// —— 7. ZT 环路保护 ——
	//
	// 装在数据面这一层，而不是分流那一层：它的生命周期独立于
	// 「分流启用 / 暂停」，即使分流被撤掉它也必须留着，
	// 否则 ZT 流量会被吸进隧道，出口 agent 再也连不回网关（死锁）。
	pp.ztGuard = setupZTGuard(log, sys, env)

	pp.cleanup = func() {
		if pp.dispatch != nil {
			pp.dispatch.teardown()
		}
		// ZT 环路保护规则最后撤：它跟分流无关，只跟进程有关。
		pp.ztGuard.Remove(sys)
		// 必须删掉自己加的 FORWARD 放行规则。
		//
		// 实测踩到：proxy 模式启动时插了两条 `-o/-i slx0 -j ACCEPT`，
		// 但最初的 cleanup 没删它们，于是每次启动都在路由器上留两条、
		// 且指向一个已经不存在的网卡（slx0 已删）。反复启停会无限累积。
		sys.RemoveForwardAccept(dev.Name(), false)
		sys.RemoveForwardAccept(dev.Name(), true)
		stk.Close()
		gw.Close()
		_ = dev.Close()
	}

	go func() {
		if err := <-gwErr; err != nil && ctx.Err() == nil {
			log.Error("agent 监听退出", logx.FieldEvent, "relay.exited", logx.FieldErr, err.Error())
		}
	}()

	log.Info("数据面就绪（proxy 模式）", logx.FieldEvent, "plane.ready",
		"role", "gateway", "mode", config.ModeProxy,
		"agent_listen", gw.Addr(),
		logx.FieldHint, "出口节点用 `slx agent --gateway <本机IP:端口>` 接入")

	return pp, nil
}

// Run 启动 TUN ↔ 协议栈的双向泵。
func (p *proxyPlane) Run(ctx context.Context) error {
	// TUN → 协议栈
	go func() {
		buf := make([]byte, 65535)
		for {
			if ctx.Err() != nil {
				return
			}
			n, err := p.dev.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// 读错误通常意味着设备被关闭，直接退出泵
				return
			}
			if n > 0 {
				// ICMP echo 必须自己接管：gvisor 只终结 TCP/UDP，
				// 不处理就会导致 proxy 模式下 ping 完全不通，
				// 用户会误以为整个网络挂了。
				//
				// tryICMPEcho 内部会自己拷贝一份（它要起 goroutine 等 agent），
				// 所以这里传原始 buf 是安全的。
				if p.tryICMPEcho(buf[:n]) {
					p.tunRead.Add(1)
					continue
				}
				if pushTUNPacket(p.stack, buf, n) {
					p.tunRead.Add(1)
				}
			}
		}
	}()

	// 协议栈 → TUN
	go func() {
		for {
			pkt := p.stack.ReadPacket(ctx)
			if len(pkt) == 0 {
				return
			}
			if _, err := p.dev.Write(pkt); err != nil {
				p.tunWriteErr.Add(1)
				continue
			}
			p.tunWrite.Add(1)
		}
	}()

	<-ctx.Done()
	return nil
}

// tunPacketSink 是「把一个 IP 包交给协议栈」的接口。
//
// 抽成接口只有一个目的：**能写测试**。
// 真实实现是 *netstack.Stack；测试需要一个「假装异步使用」的替身
// 来复现数据竞争（见 proxybuf_test.go）。
type tunPacketSink interface {
	PushPacket(pkt []byte) bool
}

// pushTUNPacket 把从 TUN 读到的包交给协议栈。
//
// # **必须拷贝**
//
// TUN 读循环复用同一个 buf，而协议栈是**异步**处理包的：
//
//   - TCP：gvisor 的 forwarder 是 `go f.handler(...)`，
//     handler 协程里才做 CreateEndpoint（要读 SYN 里的 TCP 选项）；
//   - UDP：端点把包收进接收队列，之后才由中继协程读它的载荷。
//
// ⇒ 不拷贝的话，协议栈可能读到**已经被下一个包覆盖**的字节：
// 数据竞争（-race 会报），线上表现为偶发的包错乱 / 校验和错误。
//
// 注意 netstack.PushPacket 里的 buffer.MakeWithData(pkt) **不拷贝**，
// 它只是引用调用方的字节。
func pushTUNPacket(sink tunPacketSink, buf []byte, n int) bool {
	return sink.PushPacket(copyTunPacket(buf[:n]))
}

// copyTunPacket 复制一份从 TUN 读到的 IP 包。
func copyTunPacket(src []byte) []byte {
	pkt := make([]byte, len(src))
	copy(pkt, src)
	return pkt
}

// HasEgress 报告当前是否有可用出口（供分流看门狗判断是否接管流量）。
func (p *proxyPlane) HasEgress() bool {
	return len(p.gateway.AgentIDs()) > 0
}

// Stats 返回数据面计数。
func (p *proxyPlane) Stats() (tunRead, tunWrite, writeErr int64, ns netstack.Stats) {
	return p.tunRead.Load(), p.tunWrite.Load(), p.tunWriteErr.Load(), p.stack.Stats()
}

// ICMPStats 返回 ICMP echo 接管计数。
func (p *proxyPlane) ICMPStats() (replied, dropped int64) {
	return p.icmpReplied.Load(), p.icmpDropped.Load()
}

// PingHost 请求出口 agent 探测目标是否可达。
//
// 选路与 TCP/UDP 走同一套逻辑：同一个目标应该落在同一个出口上，
// 否则会出现「ping 通但网页打不开」或者反过来。
func (c *proxyConnector) PingHost(dst net.IP) bool {
	nodeID, ok := c.pick(dst)
	if !ok {
		return false
	}
	alive, err := c.gw.PingHost(nodeID, dst, icmpPingTimeout)
	if err != nil {
		// 「没有可用通道」是通道级熔断的结果，不是出口本身坏了 ⇒ 不推进 agent 级熔断。
		if errors.Is(err, relay.ErrNoUsableChannel) {
			c.picker.ReportTargetFailure(nodeID)
			return false
		}
		// relay 层失败：agent 会话断了或开不了流 → 出口自己坏了，计入熔断。
		c.picker.ReportHealthWithReason(nodeID, false,
			"出口链路失败："+probeErrPhrase(err), time.Now())
		return false
	}
	if !alive {
		// 目标不应答 ≠ 出口坏了：不推进熔断，只记一笔。
		c.picker.ReportTargetFailure(nodeID)
		return false
	}
	c.picker.ReportHealth(nodeID, true, time.Now())
	return true
}
