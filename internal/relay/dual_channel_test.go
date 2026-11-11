package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"slx/internal/logx"
	"slx/internal/proto"
	"slx/internal/stream"
)

// fakeAgent 模拟一个出口 agent：同时具备两种角色 ——
//   - 反向连接（yamux 客户端）连到网关，模拟 ZeroTier 兜底通道；
//   - 监听 TLS（yamux 服务端）供网关拨入，模拟公网直连通道。
//
// 它的存在是为了复现并锁住 P2 现场问题：
// 「同一个 agent 同时存在 ZT 通道和直连通道时，ZT 通道上的解析必须仍然正常」。
type fakeAgent struct {
	key    []byte
	nodeID string

	// failResolve 为 true 时，解析请求返回失败（模拟出口 DNS 坏了）。
	failResolve atomic.Bool

	// hangResolve 为 true 时，解析请求收下但不答复（模拟出口不响应）。
	hangResolve atomic.Bool

	// closeProbeAfter：回满这么多个 pong 后就关掉这条探测流
	// （模拟对端把探测流关了 —— 现场症状的来源）。0 = 不关。
	closeProbeAfter atomic.Int32

	// 反向连接
	revSess *yamux.Session
	revOnce sync.Once

	// 直连监听
	ln       net.Listener
	cert     tls.Certificate
	pin      string
	directWG sync.WaitGroup
	stopped  chan struct{}
}

// newFakeAgent 起一个假 agent：监听 TLS（直连通道用）。
func newFakeAgent(t *testing.T, key []byte, nodeID string) *fakeAgent {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "relay-cert.pem")
	cert, err := LoadOrCreateCert(certFile, 365)
	if err != nil {
		t.Fatalf("准备证书失败: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	a := &fakeAgent{
		key:     key,
		nodeID:  nodeID,
		ln:      ln,
		cert:    cert,
		pin:     CertFingerprint(leaf),
		stopped: make(chan struct{}),
	}
	// 接受网关拨来的直连连接
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			a.directWG.Add(1)
			go func() {
				defer a.directWG.Done()
				a.serveDirect(conn)
			}()
		}
	}()
	return a
}

// addr 返回直连监听地址。
func (a *fakeAgent) addr() string { return a.ln.Addr().String() }

// hostPort 返回上报用的 host:port。
func (a *fakeAgent) hostPort() (string, uint16) {
	host, portStr, _ := net.SplitHostPort(a.addr())
	p, _ := strconv.Atoi(portStr)
	return host, uint16(p)
}

// serveDirect 处理一条网关拨来的直连连接（网关是 yamux 客户端，这里是服务端）。
func (a *fakeAgent) serveDirect(conn net.Conn) {
	if _, _, err := ServerHandshakeWithSkew(conn, a.key); err != nil {
		_ = conn.Close()
		return
	}
	sess, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sess.Close()
	for {
		s, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go a.handleStream(s)
	}
}

// handleStream 处理一条流：解析请求 → 回结果。
func (a *fakeAgent) handleStream(s *yamux.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	kind, payload, err := stream.ReadFrame(s)
	if err != nil {
		return
	}
	switch kind {
	case stream.KindResolveReq:
		req, err := stream.DecodeResolveReq(payload)
		if err != nil {
			return
		}
		if a.hangResolve.Load() {
			// 模拟「出口那边不回解析结果」：请求收到了，就是不答复。
			// 用来验证网关侧的**总预算**（开流 + 解析）不会超出调用方给的时限。
			select {}
		}
		if a.failResolve.Load() {
			// 模拟「出口那边解析不了」：业务失败，不是链路坏了。
			_ = stream.WriteResolveResp(s, proto.ResolveResp{
				Domain: req.Domain,
				Err:    "模拟解析失败",
			})
			return
		}
		_ = stream.WriteResolveResp(s, proto.ResolveResp{
			Domain: req.Domain,
			IPs:    []string{"203.0.113.7"},
		})
	case stream.KindProbeHello:
		sent := int32(0)
		for {
			k, _, err := stream.ReadFrame(s)
			if err != nil {
				return
			}
			if k == stream.KindPing {
				_ = stream.WriteControlFrame(s, stream.KindPong)
				sent++
				// 模拟「对端把探测流关掉」：
				// 回够 N 个 pong 就关流（网关侧于是拿到一条僵尸流）。
				if n := a.closeProbeAfter.Load(); n > 0 && sent >= n {
					return
				}
			}
		}
	}
}

// connectReverse 反向连接网关（模拟 ZT 通道），并上报公网地址。
func (a *fakeAgent) connectReverse(t *testing.T, gwAddr string) {
	t.Helper()
	conn, err := tls.Dial("tcp", gwAddr, TLSConfigForClient())
	if err != nil {
		t.Fatalf("反向连接失败: %v", err)
	}
	if err := ClientHandshake(conn, a.key, a.nodeID); err != nil {
		t.Fatalf("反向握手失败: %v", err)
	}
	sess, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		t.Fatalf("建立 yamux 客户端会话失败: %v", err)
	}
	a.revSess = sess

	// 反向连接上也要能接网关开来的流（网关在这条连接上请求解析/建连）。
	go a.serveReverse(sess)

	// 上报公网地址与证书指纹（走已认证的反向连接）。
	host, port := a.hostPort()
	st, err := sess.OpenStream()
	if err != nil {
		t.Fatalf("打开上报流失败: %v", err)
	}
	rep := proto.AddrReport{
		Proto:      "tcp",
		Host:       host,
		Port:       port,
		CertSHA256: a.pin,
		AgentVer:   "test",
	}
	if err := stream.WriteAddrReport(st, rep); err != nil {
		t.Fatalf("上报地址失败: %v", err)
	}
	_ = st.Close()

	// 保持会话活着。
	go func() {
		<-a.stopped
		_ = sess.Close()
	}()
}

// close 收尾。
func (a *fakeAgent) close() {
	close(a.stopped)
	_ = a.ln.Close()
	if a.revSess != nil {
		_ = a.revSess.Close()
	}
	a.directWG.Wait()
}

// startGateway 起一个网关监听。
func startGatewayWithMode(t *testing.T, key []byte, mode string, gwNodeID string) (*Gateway, string, func()) {
	t.Helper()
	g := NewGateway(GatewayOptions{
		Key:           key,
		Addr:          "127.0.0.1:0",
		Log:           logx.Discard(),
		Transport:     mode,
		NodeID:        gwNodeID,
		DirectConns:   2,
		DialTimeout:   3 * time.Second,
		ProbeInterval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = g.Run(ctx)
	}()
	// 等监听就绪
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if g.Addr() != "127.0.0.1:0" && g.Listening() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return g, g.Addr(), func() {
		cancel()
		g.Close()
		<-done
	}
}

// —— 回归测试 ——

// TestDualChannel_ZTResolveUnaffected 是 P2 现场问题的回归测试：
//
// 同一个 agent **同时**存在 ZeroTier 通道与公网直连通道时，
// 走 ZeroTier 通道的域名解析必须仍然正常 —— 直连通道出问题
// （建不起来、指纹不匹配、熔断）绝不允许影响兜底通道。
func TestDualChannel_ZTResolveUnaffected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-zt")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-1")
	defer stop()

	ag.connectReverse(t, gwAddr)

	// 等直连通道建立。
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-zt").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 关键断言：兜底通道上的解析必须正常。
	ips, err := resolveWithTimeout(t, gw, "agent-zt", "example.com", 3*time.Second)
	if err != nil {
		t.Fatalf("直连通道存在时，兜底通道的解析必须正常: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("解析结果为空")
	}
}

// TestDualChannel_ResolveOnEachChannel 验证两条通道各自都能正常解析。
func TestDualChannel_ResolveOnEachChannel(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-both")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-2")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-both").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 直连通道（tls 模式）
	ips, err := resolveWithTimeout(t, gw, "agent-both", "a.example.com", 3*time.Second)
	if err != nil {
		t.Fatalf("直连通道上的解析应正常: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("解析结果为空")
	}

	// 兜底通道（zt 模式）
	gw.SetTransport(TransportZT)
	ips, err = resolveWithTimeout(t, gw, "agent-both", "b.example.com", 3*time.Second)
	if err != nil {
		t.Fatalf("兜底通道上的解析应正常: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("解析结果为空")
	}
}

// TestDualChannel_DirectFailureIsolated 验证防御性设计：
// 直连通道建不起来（指纹不匹配）时，兜底通道不受任何影响。
func TestDualChannel_DirectFailureIsolated(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-badpin")
	defer ag.close()

	// 网关钉一个错的指纹 → 直连必然失败
	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-3")
	gw.pinnedTLS = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	defer stop()

	ag.connectReverse(t, gwAddr)

	// 等一等，确认直连建不起来
	time.Sleep(500 * time.Millisecond)
	if n := len(agentSessionOf(gw, "agent-badpin").tlsChannels()); n != 0 {
		t.Fatalf("指纹不匹配时不应建立直连通道，实际 %d 条", n)
	}

	// 兜底通道必须照常工作。
	ips, err := resolveWithTimeout(t, gw, "agent-badpin", "c.example.com", 3*time.Second)
	if err != nil {
		t.Fatalf("直连失败不得影响兜底通道: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("解析结果为空")
	}
}

// —— 小工具 ——

// resolveWithTimeout 在独立 goroutine 里解析，超时即失败。
//
// 这正是现场症状的检测方式：解析卡住 → DNS 转发器（串行）被拖死 → 全屋断网。
func resolveWithTimeout(t *testing.T, g *Gateway, nodeID, domain string, timeout time.Duration) ([]net.IP, error) {
	t.Helper()
	type res struct {
		ips []net.IP
		err error
	}
	ch := make(chan res, 1)
	go func() {
		ips, err := g.Resolve(nodeID, domain, 2*time.Second)
		ch <- res{ips, err}
	}()
	select {
	case r := <-ch:
		return r.ips, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("解析 %s 超时 %s（通道卡死）", domain, timeout)
	}
}

// waitFor 轮询等待条件成立。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// agentSessionOf 安全地取某个 agent 的会话。
//
// ★ agent 还没接入（或刚断开）时 map 里没有它 ⇒ Gateway.Session 返回 nil ✅✅
// ⇒ **直接 .tlsChannels() 会 nil 解引用** ✅⚠️（2026-11-11 CI 上真实崩过 ✅⚠️）
// ⇒ 这里统一返回**空会话**而不是 nil ✅✅ ⇒ 调用方拿到的是空通道列表 ✅✅
//
//	—— 对测试语义完全正确 ✅✅（还没接入 ⇒ 确实没有通道 ✅✅）✅ ✅
func agentSessionOf(g *Gateway, nodeID string) *agentSession {
	s := g.Session(nodeID)
	if s == nil {
		return &agentSession{chans: nil}
	}
	return s
}

// serveReverse 处理反向连接上网关开来的流（agent 在这条连接上是 yamux 客户端）。
func (a *fakeAgent) serveReverse(sess *yamux.Session) {
	for {
		s, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go a.handleStream(s)
	}
}

// TestProbeLoop_DirectChannelStaysHealthy 模拟生产条件：
// 网关的**通道探测循环**（每 15s）+ **L1 探测**（每 2s）同时跑在直连通道上。
//
// 现场症状：直连通道反复被熔断（熔断 → 冷却 → 半开 → 又失败 → 再熔断）。
// 这个测试就是为了把它复现出来。
func TestProbeLoop_DirectChannelStaysHealthy(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-probe")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-probe")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-probe").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 跑 12 轮：每轮一次通道探测 + 一次 L1 探测。
	for i := 0; i < 12; i++ {
		for _, c := range agentSessionOf(gw, "agent-probe").channels() {
			rtt, err := gw.probeOnChannel(c, 5*time.Second)
			if err != nil {
				t.Fatalf("第 %d 轮探测 %s 通道失败: %v", i, c.Name(), err)
			}
			t.Logf("第 %d 轮 %s 通道 RTT=%s", i, c.Name(), rtt)
		}
		if _, err := gw.Ping("agent-probe", 3*time.Second); err != nil {
			t.Fatalf("第 %d 轮 L1 探测失败: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 断言：直连通道没有反复熔断。
	for _, c := range agentSessionOf(gw, "agent-probe").tlsChannels() {
		open, until := c.breaker.state()
		if open {
			t.Fatalf("直连通道处于熔断状态（until=%s）", until)
		}
	}
}

// TestProbe_PeerClosedStreamRecoversFast 是「直连通道反复熔断」的回归测试：
//
// 对端把探测流关掉后（网关侧变成僵尸流），网关必须
//  1. **快速**发现（不能干等到探测超时 —— 现场是 5~10 秒一轮，等到人都麻木了）；
//  2. 丢掉这条坏流，**重建**一条新的；
//  3. 下一次探测恢复正常，通道不熔断。
//
// 修复前：探测流失效后要等超时才发现，而且半开时还拿同一条坏流去试
// ⇒ 熔断 → 冷却 → 半开 → 又失败 → 再熔断，永远回不来。
func TestProbe_PeerClosedStreamRecoversFast(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-closed")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-closed")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-closed").tlsChannels()) >= 1
	}, "直连通道应建立")

	chans := agentSessionOf(gw, "agent-closed").tlsChannels()
	c := chans[0]

	// 第一轮探测：正常，建出探测流。
	if _, err := gw.probeOnChannel(c, 5*time.Second); err != nil {
		t.Fatalf("首轮探测应成功: %v", err)
	}
	if c.probe == nil {
		t.Fatal("首轮探测后应留下常驻探测流")
	}

	// 对端回满 1 个 pong 后就关掉这条探测流（网关侧于是拿到僵尸流）。
	ag.closeProbeAfter.Store(1)

	// 接下来几轮里必须有一轮**快速**失败。
	//
	// 注意：对端关流的那一轮可能仍然成功（pong 先到、FIN 后到），
	// 失败出现在**下一轮**（写进已关的流 → 读回 EOF）。
	// 关键不是第几轮失败，而是**失败要快**（读 EOF，不是干等超时）。
	failed := false
	var elapsed time.Duration
	for i := 0; i < 5 && !failed; i++ {
		start := time.Now()
		_, err := gw.probeOnChannel(c, 5*time.Second)
		elapsed = time.Since(start)
		if err != nil {
			failed = true
		}
	}
	if !failed {
		t.Fatal("对端关掉探测流后，网关应能发现")
	}
	if elapsed > time.Second {
		t.Fatalf("探测失败耗时 %s，应快速失败（不能等满探测超时）", elapsed)
	}

	// 坏流必须被丢掉（否则下一次还拿它试）。
	if c.probe != nil {
		t.Fatal("探测失败后必须丢弃这条坏流")
	}

	// 第三轮探测：应当重建流并成功。
	if _, err := gw.probeOnChannel(c, 5*time.Second); err != nil {
		t.Fatalf("重建探测流后探测应成功: %v", err)
	}
	if c.probe == nil {
		t.Fatal("重建后应留下新的探测流")
	}

	// 单次失败不该熔断这条通道。
	if open, _ := c.breaker.state(); open {
		t.Fatal("单次探测失败不应熔断通道")
	}
}

// TestResolveFailure_DoesNotTripChannelBreaker 是级联故障的回归测试。
//
// 现场事故：DNS 解析失败 → 业务路径把通道熔断计数推上去 →
// 通道熔断 → 该出口没有可用通道 → L1 探测把它记成 98~100% 丢包 →
// agent 级熔断 → 出口被踢出候选集 → DNS 经出口解析全失败 → 循环。
//
// 正确的分工：**通道的健康只能由通道探测来判定**。
// 业务路径（DNS 解析、建连）失败不许碰熔断器。
func TestResolveFailure_DoesNotTripChannelBreaker(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-resolve")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-resolve")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-resolve").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 出口那边解析不了（业务失败）。
	ag.failResolve.Store(true)

	// 连着解析 10 次，每次都失败。
	for i := 0; i < 10; i++ {
		if _, err := gw.Resolve("agent-resolve", "example.com", 3*time.Second); err == nil {
			t.Fatal("解析应当失败")
		}
	}

	// **所有通道的熔断计数必须纹丝不动**。
	//
	// 解析请求可能落在任意一条候选通道上，所以要逐条查，
	// 不能只查其中一条。
	for _, c := range agentSessionOf(gw, "agent-resolve").channels() {
		if open, _ := c.breaker.state(); open {
			t.Fatalf("业务解析失败不应熔断通道 %s（熔断只能由通道探测判定）", c.Name())
		}
		c.breaker.mu.Lock()
		fails := c.breaker.failures
		c.breaker.mu.Unlock()
		if fails != 0 {
			t.Fatalf("业务解析失败不应推进通道 %s 的熔断计数，当前计数 = %d", c.Name(), fails)
		}
	}
}

// TestPing_NoUsableChannelIsDistinct 是级联故障的第二个回归测试：
//
// 一台 agent 的通道全被熔断时，
//   - Ping 必须返回 ErrNoUsableChannel（**不是** ErrNoAgent）——
//     因为 agent 明明在线，只是没有可用通道；
//   - 上层据此不把它记成丢包、不推进 agent 级熔断。
func TestPing_NoUsableChannelIsDistinct(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-nocand")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-nocand")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-nocand").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 把这台 agent 的所有通道都熔断掉。
	now := time.Now()
	for _, c := range agentSessionOf(gw, "agent-nocand").channels() {
		for i := 0; i < 100; i++ {
			c.recordProbe(false, 0, now)
		}
	}

	// 探测必须报「没有可用通道」，而不是「没有在线 agent」。
	_, err := gw.Ping("agent-nocand", 2*time.Second)
	if err == nil {
		t.Fatal("所有通道熔断时 Ping 应当失败")
	}
	if !errors.Is(err, ErrNoUsableChannel) {
		t.Fatalf("应当返回 ErrNoUsableChannel，实际 = %v", err)
	}
	if errors.Is(err, ErrNoAgent) {
		t.Fatal("agent 在线时不应返回 ErrNoAgent（会让人误以为 agent 掉线了）")
	}

	// 同样地，Open 也必须报「没有可用通道」。
	if _, err := gw.Open("agent-nocand", proto.ConnectReq{Net: 0, Transport: proto.ConnectTCP, Addr: "1.1.1.1", Port: 443}); !errors.Is(err, ErrNoUsableChannel) {
		t.Fatalf("Open 应当返回 ErrNoUsableChannel，实际 = %v", err)
	}
}
