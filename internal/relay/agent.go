package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"

	"slx/internal/logx"
	"slx/internal/proto"
	"slx/internal/stream"
)

// dialTimeout 是 agent 替网关连目标时的超时。
//
// 30 秒是经验值：太短会把「慢但不死」的站点误判成不可达，
// 太长会让网关侧的 gvisor 连接一直挂着（用户看到浏览器转圈不报错）。
const dialTimeout = 30 * time.Second

// Agent 是出口侧的 agent：反向拨号到网关，按网关请求建立连接并中继字节。
//
// 它不需要 root、不建 TUN、不碰 iptables —— 全部工作是 net.Dial + io.Copy。
//
// # 两种角色
//
//   - 反向拨号（一直有）：拨网关的 ZeroTier 地址，作为兜底通道；
//   - 公网监听（可选）：网关主动拨 agent 的公网 IP（默认 8443），
//     走 TCP+TLS+指纹钉扎，作为主通道。
//
// agent 上跑着 nginx、443 已被占，所以默认端口是 8443。
type Agent struct {
	key         []byte
	gatewayAddr string
	nodeID      string
	log         *slog.Logger

	// Dialer 允许注入自定义拨号器（测试用），nil 时用默认。
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

	// —— 直连通道（可选）——

	// tlsListen 是公网 TLS 监听地址（host:port）。空 = 不监听。
	tlsListen string
	// tlsCert 是持久化证书路径。
	tlsCert string
	// tlsPort 是上报给网关的端口。
	tlsPort uint16
	// advertise 是上报给网关的公网地址（host）。
	advertise string
	// certFingerprint 是本机 TLS 服务端证书的指纹（上报给网关做钉扎）。
	certFingerprint string
	// version 只用于日志与排障。
	version string
	// certDays 是自签证书有效期（天）。
	certDays int
}

// AgentOptions 是 agent 构造参数。
type AgentOptions struct {
	Key         []byte
	GatewayAddr string
	NodeID      string
	Log         *slog.Logger
	Dialer      func(ctx context.Context, network, addr string) (net.Conn, error)

	// TLSListen 是 agent 侧的公网 TLS 监听地址（host:port）。
	//
	// 空 = 不监听（默认，行为与改造前完全一致，不扩大安全面）。
	TLSListen string

	// TLSCert 是持久化证书路径（含私钥，0600）。
	// 空 = 用 DefaultCertFile。
	TLSCert string

	// TLSPort 是上报给网关的端口（默认 8443）。
	TLSPort uint16

	// Advertise 是上报给网关的公网地址（host）。
	// 空 = 由调用方自动探测后填入。
	Advertise string

	// CertDays 是自签证书有效期（天）。0 = 默认 3650。
	CertDays int

	// Version 只用于日志与排障。
	Version string

	// CertFingerprint 是本机 TLS 服务端证书的指纹（上报给网关做钉扎）。
	//
	// 由调用方提前算好（证书要先落盘，指纹才稳定）：
	// 否则「先建反向连接、后起监听」时序下，指纹还没算出来，
	// 上报会被跳过（那就少了一条直连通道，功能不受影响，但白等一轮）。
	CertFingerprint string
}

// NewAgent 创建 agent。
func NewAgent(o AgentOptions) *Agent {
	certFile := o.TLSCert
	if certFile == "" {
		certFile = DefaultCertFile
	}
	a := &Agent{
		key:         o.Key,
		gatewayAddr: o.GatewayAddr,
		nodeID:      o.NodeID,
		log:         o.Log,
		Dialer:      o.Dialer,
		tlsListen:   o.TLSListen,
		tlsCert:     certFile,
		tlsPort:     o.TLSPort,
		advertise:   o.Advertise,
		version:     o.Version,
	}
	a.certFingerprint = o.CertFingerprint
	if a.tlsPort == 0 {
		a.tlsPort = 8443
	}
	if o.CertDays > 0 {
		a.certDays = o.CertDays
	}
	return a
}

// Run 维持到网关的连接，断了自动重连，直到 ctx 取消。
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	// 启动时就说清楚 ICMP 能力，避免用户到「ping 不通」时才发现。
	if !ICMPAvailable() {
		a.log.Warn("本机无法发送 ICMP echo，proxy 模式下 ping 将退化为 TCP 探测",
			logx.FieldEvent, "agent.icmp_unavailable",
			logx.FieldHint, "要启用真 ICMP: sysctl -w net.ipv4.ping_group_range=\"0 65535\"；"+
				"或以 CAP_NET_RAW 权限运行 agent")
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		err := a.connectOnce(ctx)

		if ctx.Err() != nil {
			return nil
		}
		// 连上过一段时间又断 = 网络抖动，重置退避快速重连；
		// 秒断秒重连才需要退避，否则会把网关打满。
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}

		a.log.Warn("与网关的连接断开，准备重连",
			logx.Event("relay.disconnected"),
			"gateway", a.gatewayAddr,
			logx.FieldErr, errString(err),
			"retry_in", backoff.String())

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connectOnce 拨一次网关并服务到连接结束。
func (a *Agent) connectOnce(ctx context.Context) error {
	d := &tls.Dialer{Config: TLSConfigForClient()}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	conn, err := d.DialContext(dialCtx, "tcp", a.gatewayAddr)
	if err != nil {
		return fmt.Errorf("连接网关失败: %w", err)
	}

	if err := ClientHandshake(conn, a.key, a.nodeID); err != nil {
		_ = conn.Close()
		return err
	}

	sess, err := yamux.Client(conn, &yamux.Config{
		AcceptBacklog:          256,
		EnableKeepAlive:        true,
		KeepAliveInterval:      10 * time.Second,
		ConnectionWriteTimeout: 10 * time.Second,
		MaxStreamWindowSize:    512 * 1024,
		LogOutput:              io.Discard, // 同上：必须显式指定，否则 yamux 拒绝
	})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("建立 yamux 会话失败: %w", err)
	}
	defer sess.Close()

	a.log.Info("已连接到网关",
		logx.Event("relay.connected"),
		"gateway", a.gatewayAddr,
		logx.FieldNodeID, shortID(a.nodeID))

	// 把自己的公网地址告诉网关（网关才能主动拨过来）。
	a.reportAddr(sess)

	return a.serve(ctx, sess)
}

// serve 接受网关开来的流并逐条处理，直到会话结束。
func (a *Agent) serve(ctx context.Context, sess *yamux.Session) error {
	// ctx 取消时关闭会话，让 AcceptStream 返回
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-sess.CloseChan():
		}
	}()

	for {
		s, err := sess.AcceptStream()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		go a.handleStream(s)
	}
}

// handleStream 处理一条流：读请求 → 连目标 → 回结果 → 中继。
func (a *Agent) handleStream(s *yamux.Stream) {
	defer func() {
		if rec := recover(); rec != nil {
			a.log.Error("处理中继流 panic", logx.Event("relay.stream_panic"), "detail", rec)
			_ = s.Close()
		}
	}()

	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	kind, payload, err := stream.ReadFrame(s)
	if err != nil {
		// 读不到握手说明网关侧已经放弃，直接关掉即可
		_ = s.Close()
		return
	}

	// 探测通道是长连接，专门用来做 L1 链路质量采样（RTT/丢包）。
	// 它不中继任何数据，只回 pong。
	if kind == stream.KindProbeHello {
		_ = s.SetDeadline(time.Time{}) // 长连接，清掉握手期的 20s 期限
		a.serveProbe(s)
		return
	}
	// 域名解析：用 agent 自己的系统 DNS 解析。
	//
	// 这是「CDN 就近」的关键：同一个域名在网关当地和出口当地解析出来的 IP
	// 可能属于不同边缘节点。用户要的是贴着出口的地址，否则解析在日本、
	// 流量却从法兰克福出去，视频照样卡。
	if kind == stream.KindResolveReq {
		rreq, rerr := stream.DecodeResolveReq(payload)
		if rerr != nil {
			_ = s.Close()
			return
		}
		resp := resolveOnAgent(rreq.Domain)
		_ = stream.WriteResolveResp(s, resp)
		_ = s.Close()
		return
	}

	// 主机可达性探测：agent 替网关对目标做一次 ICMP echo。
	// 这样「能不能 ping 通」反映的是出口侧的网络，而不是网关自己。
	if kind == stream.KindHostPing {
		preq, perr := stream.DecodeHostPing(payload)
		if perr != nil {
			_ = s.Close()
			return
		}
		ip := net.ParseIP(preq.Addr)
		alive := false
		if ip != nil {
			alive = pingHost(ip, hostPingTimeout)
		}
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		_ = stream.WriteHostPong(s, proto.HostPingResp{Alive: alive})
		_ = s.Close()
		return
	}
	if kind != stream.KindConnectReq {
		_ = s.Close()
		return
	}
	req, derr := stream.DecodeConnectReq(payload)
	if derr != nil {
		_ = s.Close()
		return
	}

	target := net.JoinHostPort(req.Addr, strconv.Itoa(int(req.Port)))
	network := "tcp"
	if req.Transport == proto.ConnectUDP {
		network = "udp"
	}

	dial := a.Dialer
	if dial == nil {
		nd := &net.Dialer{Timeout: dialTimeout}
		dial = nd.DialContext
	}

	// 目标连接用独立超时上下文：请求已经读完，流上不再需要 deadline
	dctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	_ = s.SetDeadline(time.Time{})

	targetConn, derr := dial(dctx, network, target)
	if derr != nil {
		resp := proto.ConnectResp{Reason: derr.Error()}
		// 端口拒绝 → 让网关回 RST；超时/网络不可达 → 让客户端自己等超时
		if stream.ClassifyNetError(derr) != stream.UDPErrNone || isRefused(derr) {
			resp.Reset = true
		}
		_ = stream.WriteConnectResp(s, resp)
		_ = s.Close()
		a.log.Debug("建连失败",
			logx.Event("relay.connect_failed"),
			"target", target, "network", network,
			logx.FieldErr, derr.Error())
		return
	}

	if err := stream.WriteConnectResp(s, proto.ConnectResp{Established: true}); err != nil {
		_ = targetConn.Close()
		_ = s.Close()
		return
	}

	if req.Transport == proto.ConnectUDP {
		err = stream.FramedPipe(s, targetConn, stream.ClassifyNetError)
	} else {
		err = stream.RelayTCP(s, targetConn)
	}
	if err != nil && !isBenignClose(err) {
		a.log.Debug("中继结束",
			logx.Event("relay.stream_closed"),
			"target", target, logx.FieldErr, err.Error())
	}
}

// isRefused 判断错误是否为「连接被拒」（对应客户端应看到 RST）。
//
// 为什么必须区分：连不上分两种，语义完全不同 ——
//
//	· 端口拒绝（RST）：告诉客户端「这里没服务」，应立刻回 RST 快速失败；
//	· 超时/网络不可达：不该回包，让客户端按自己的策略重试。
//
// 混成一种会让「网站挂了」表现为「浏览器转圈 30 秒」而不是「立刻报错」。
func isRefused(err error) bool {
	if err == nil {
		return false
	}
	// errors.Is 能穿过 net.OpError / os.SyscallError 拿到原始 errno。
	// Windows 上 syscall.ECONNREFUSED 同样有定义（映射 WSAECONNREFUSED）。
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return stream.ClassifyNetError(err) == stream.UDPErrPortUnreachable
}

// isBenignClose 过滤正常关闭产生的错误，避免日志噪声。
func isBenignClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"use of closed network connection", "stream closed", "broken pipe", "connection reset"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// errString 把 nil error 渲染成 "-"，便于日志字段统一。
func errString(err error) string {
	if err == nil {
		return "-"
	}
	return err.Error()
}

// serveProbe 服务一条探测通道：收到 Ping 就回 Pong。
//
// 为什么用独立流而不是复用控制消息：探测每 2 秒一次，走 yamux 流可以顺便
// 测到「这条连接本身的往返」，包括多路复用的调度开销 ——
// 这才是网关到出口的真实链路质量，而不是某个应用层心跳的数字。
func (a *Agent) serveProbe(s *yamux.Stream) {
	defer s.Close()
	for {
		kind, _, err := stream.ReadFrame(s)
		if err != nil {
			return
		}
		if kind != stream.KindPing {
			return // 探测通道上只允许 Ping
		}
		if err := stream.WriteControlFrame(s, stream.KindPong); err != nil {
			return
		}
	}
}

// resolveTimeout 是 agent 侧单次域名解析的超时。
const resolveTimeout = 4 * time.Second

// resolveOnAgent 用本机系统 DNS 解析域名，返回贴着出口位置的 IP。
func resolveOnAgent(domain string) proto.ResolveResp {
	resp := proto.ResolveResp{Domain: domain}
	if domain == "" {
		resp.Err = "域名为空"
		return resp
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, domain)
	if err != nil {
		resp.Err = err.Error()
		return resp
	}
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			resp.IPs = append(resp.IPs, ip.IP.String())
		}
	}
	if len(resp.IPs) == 0 {
		resp.Err = "没有 A 记录"
	}
	return resp
}

// —— 公网直连通道（可选）——

// ListenDirect 监听公网 TLS，等待网关主动拨来。
//
// 角色与反向连接相反：这里是 **agent 当服务端**，网关是客户端。
// 握手仍然用同一套 network_key 的 HMAC（ServerHandshake），
// 证书校验走指纹钉扎（网关侧）。
//
// 监听地址为空时直接返回（= 不启用直连通道，行为与改造前一致）。
func (a *Agent) ListenDirect(ctx context.Context) error {
	if a.tlsListen == "" {
		return nil
	}
	cert, err := LoadOrCreateCert(a.tlsCert, a.certDays)
	if err != nil {
		return fmt.Errorf("准备 TLS 证书失败: %w", err)
	}
	// 指纹不在这里写回 Agent：调用方（CLI）已经算好并填好了，
	// 这里再写会与「上报地址」那条 goroutine 抢同一个字段（数据竞争）。
	pin := ""
	if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
		pin = CertFingerprint(leaf)
	}

	tr := TLSTransport{Server: true, CertFile: a.tlsCert, CertDays: a.certDays}
	ln, err := tr.Listen(ctx, a.tlsListen)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", a.tlsListen, err)
	}
	defer ln.Close()

	a.log.Info("公网直连监听已启动",
		logx.Event("relay.direct_listen"),
		"addr", ln.Addr().String(),
		"port", a.tlsPort,
		"pin", shortFingerprint(pin),
		logx.Hint("网关会主动拨这个地址；nginx 若要代理必须用 stream 层 TCP 透传"))

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			a.log.Warn("接受直连失败",
				logx.Event("relay.direct_accept_failed"),
				logx.FieldErr, err.Error())
			continue
		}
		go a.serveDirect(ctx, conn)
	}
}

// serveDirect 处理一条网关拨来的直连连接。
func (a *Agent) serveDirect(ctx context.Context, conn net.Conn) {
	// 网关现在是发起方，由它介绍自己；我们校验它的身份。
	nodeID, skew, err := ServerHandshakeWithSkew(conn, a.key)
	if err != nil {
		a.log.Warn("直连握手失败",
			logx.Event("relay.direct_auth_failed"),
			"remote", conn.RemoteAddr().String(),
			logx.FieldErr, err.Error())
		_ = conn.Close()
		return
	}
	if d := skew.Abs(); d > ClockSkewWarn {
		a.log.Warn("对端时钟偏差偏大",
			logx.Event("relay.clock_skew"),
			"remote", conn.RemoteAddr().String(),
			"skew", d.Round(time.Second).String(),
			logx.FieldHint, "检查对端 NTP；偏差过大会让证书校验与日志排序失准")
	}

	sess, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		a.log.Warn("直连建立 yamux 会话失败",
			logx.Event("relay.direct_yamux_failed"),
			logx.FieldErr, err.Error())
		_ = conn.Close()
		return
	}
	a.log.Info("网关已直连",
		logx.Event("relay.direct_connected"),
		"remote", conn.RemoteAddr().String(),
		"peer", shortID(nodeID))

	// 会话结束（网关断开）时清理。
	go func() {
		<-sess.CloseChan()
		_ = conn.Close()
	}()

	err = a.serve(ctx, sess)
	// **必须把会话和连接都关掉**。
	//
	// 不关的话：网关那边看到的是「TCP 连接还在、会话也没关」，
	// 但 agent 已经不再服务它了 —— 于是网关的探测一直失败
	//（写进黑洞 / 读超时）→ 反复熔断 → 反复冷却，
	// 而网关永远不知道这条连接已经死了（现场症状：直连通道反复熔断）。
	_ = sess.Close()
	_ = conn.Close()
	if err != nil {
		a.log.Debug("直连会话结束", logx.Event("relay.direct_closed"),
			logx.FieldErr, err.Error())
	}
}

// reportAddr 在反向连接上上报自己的公网地址与证书指纹。
//
// 走的是**已通过 HMAC 握手认证**的连接，所以这份信息可信度等同于 network_key
// （TOFU：信任首次上报）。
//
// 直连通道没启用（tlsListen 为空）时什么都不做 —— 老 agent 的行为完全不变。
func (a *Agent) reportAddr(sess *yamux.Session) {
	if a.tlsListen == "" || a.advertise == "" {
		return
	}
	if a.certFingerprint == "" {
		return
	}
	st, err := sess.OpenStream()
	if err != nil {
		return
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(10 * time.Second))
	rep := proto.AddrReport{
		Proto:      "tcp",
		Host:       a.advertise,
		Port:       a.tlsPort,
		CertSHA256: a.certFingerprint,
		AgentVer:   a.version,
	}
	if err := stream.WriteAddrReport(st, rep); err != nil {
		a.log.Debug("上报直连地址失败", logx.Event("relay.addr_report_failed"),
			logx.FieldErr, err.Error())
		return
	}
	a.log.Info("已上报直连地址",
		logx.Event("relay.addr_reported"),
		"addr", net.JoinHostPort(a.advertise, strconv.Itoa(int(a.tlsPort))),
		"pin", shortFingerprint(a.certFingerprint))
}
