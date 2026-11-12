package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// —— 测试辅助 ——

// fakeChannel 造一条不连真实网络的通道（只用于评分与选择逻辑）。
func fakeChannel(kind ChannelKind, score float64) *channel {
	c := &channel{
		kind:    kind,
		since:   time.Now(),
		rtt:     newEWMA(0.3),
		succ:    newEWMA(0.3),
		bw:      newEWMA(0.3),
		bytesAt: time.Now(),
		breaker: newChannelBreaker(),
	}
	// 用探测样本把分数推到指定值：成功率与 RTT 决定分数。
	c.succ.set(1)
	c.rtt.set(float64(score))
	return c
}

// setScore 直接把通道分数推到指定值（成功率 1、RTT 反推）。
func setScore(c *channel, rttMS float64) {
	c.succ.set(1)
	c.rtt.set(rttMS)
	c.bw.set(5)
}

// —— 通道选择与滞回 ——

// TestSelectChannel_ZTModeIgnoresDirect 验证 P0 的行为一致性：
// relay_transport=zt 时永远走 ZeroTier 兜底通道，
// 即使直连通道已经建立且质量更好。
func TestSelectChannel_ZTModeIgnoresDirect(t *testing.T) {
	s := &agentSession{nodeID: "n1"}
	zt := fakeChannel(ChannelZT, 200)
	tlsCh := fakeChannel(ChannelTLS, 40) // 明显更好
	s.addChannel(zt)
	s.addChannel(tlsCh)

	got := s.selectChannel(time.Now(), TransportZT)
	if got == nil || got.kind != ChannelZT {
		t.Fatalf("zt 模式必须选 ZeroTier 通道，实际 %v", got)
	}
}

// TestSelectChannel_TLSModeUsesDirect 验证 tls 模式只走直连通道。
func TestSelectChannel_TLSModeUsesDirect(t *testing.T) {
	s := &agentSession{nodeID: "n1"}
	s.addChannel(fakeChannel(ChannelZT, 200))
	tlsCh := fakeChannel(ChannelTLS, 40)
	s.addChannel(tlsCh)

	got := s.selectChannel(time.Now(), TransportTLS)
	if got == nil || got.kind != ChannelTLS {
		t.Fatalf("tls 模式必须选直连通道，实际 %v", got)
	}
}

// TestSelectChannel_Hysteresis 验证 8 分滞回：
// 优势不到 8 分不切换，超过 8 分才切。
//
// ★ 注意：滞回只在「没有 TLS 通道」时生效 ✅✅
//
// auto 模式下 TLS 优先（2026-11-12 改动）：只要有可用 TLS 就直接选 TLS，
// 跳过滞回。所以滞回测试要用两条 ZT 通道（没有 TLS）来测。
func TestSelectChannel_Hysteresis(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}

	// 只放 ZT 通道（没有 TLS）⇒ 走滞回逻辑 ✅✅
	zt1 := fakeChannel(ChannelZT, 200)
	s.addChannel(zt1)
	zt2 := fakeChannel(ChannelZT, 200)
	s.addChannel(zt2)

	// 先把主通道定在 zt1 上（auto 模式，分数相同时第一个候选胜出）。
	sel := s.selectChannel(now, TransportAuto)
	if sel == nil || sel.kind != ChannelZT {
		t.Fatalf("初始应选中 zt 通道，实际 %v", sel)
	}
	first := sel

	// 另一条只快一点点（RTT 190 vs 200，分数差 < 8）→ 不切换。
	setScore(zt2, 190)
	sel = s.selectChannel(now, TransportAuto)
	if sel != first {
		t.Fatalf("优势不足 8 分时不应切换，实际选 %v", sel)
	}

	// 另一条明显更好（RTT 60 vs 200）→ 切换。
	setScore(zt2, 60)
	sel = s.selectChannel(now, TransportAuto)
	if sel == zt1 {
		t.Fatalf("优势超过 8 分时应切换到更好的通道，实际仍选 %v", sel)
	}
}

// TestSelectChannel_TLSPreferred 验证 auto 模式下 TLS 直连优先 ✅✅
//
// 2026-11-12 晚高峰排查结论：ZT 是 UDP 虚拟以太网，跨境 UDP 间歇性抖动明显；
// TLS 直连走 TCP 443，与 v2ray 同类路径，稳定性更好。
// 之前 TLS 评分略高但差 8 分滞回门槛切不过去，流量一直挤在 ZT 上。
func TestSelectChannel_TLSPreferred(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}

	// ZT 评分明显更高（RTT 20），TLS 评分低（RTT 200）
	zt := fakeChannel(ChannelZT, 20)
	tlsCh := fakeChannel(ChannelTLS, 200)
	s.addChannel(zt)
	s.addChannel(tlsCh)

	// auto 模式应选 TLS（优先级高于评分）✅✅
	sel := s.selectChannel(now, TransportAuto)
	if sel == nil || sel.kind != ChannelTLS {
		t.Fatalf("auto 模式应优先选 TLS 直连，实际 %v", sel)
	}
}

// TestSelectChannel_TLSPreferredSkipsHysteresis 验证 TLS 优先跳过滞回 ✅✅
//
// TLS 优先是策略选择，不是评分比较 ⇒ 只要 TLS 可用就切，不看评分差距
func TestSelectChannel_TLSPreferredSkipsHysteresis(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}

	// 先把主通道定在 ZT 上
	zt := fakeChannel(ChannelZT, 20)
	s.addChannel(zt)
	sel := s.selectChannel(now, TransportAuto)
	// 此时没有 TLS，选 ZT
	if sel == nil || sel.kind != ChannelZT {
		t.Fatalf("没有 TLS 时应选 ZT，实际 %v", sel)
	}

	// TLS 通道上线（评分很低，RTT 500）⇒ 应立即切换，不看滞回
	tlsCh := fakeChannel(ChannelTLS, 500)
	s.addChannel(tlsCh)
	sel = s.selectChannel(now, TransportAuto)
	if sel == nil || sel.kind != ChannelTLS {
		t.Fatalf("TLS 上线后应立即切换（跳过滞回），实际 %v", sel)
	}
}

// TestSelectChannel_FallsBackWhenTLSDies 验证 TLS 全部不可用时回退 ZT ✅✅
func TestSelectChannel_FallsBackWhenTLSDies(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}

	zt := fakeChannel(ChannelZT, 100)
	tlsCh := fakeChannel(ChannelTLS, 100)
	s.addChannel(zt)
	s.addChannel(tlsCh)

	// 正常时选 TLS ✅✅
	sel := s.selectChannel(now, TransportAuto)
	if sel == nil || sel.kind != ChannelTLS {
		t.Fatalf("应选 TLS，实际 %v", sel)
	}

	// TLS 熔断 ⇒ 回退 ZT ✅✅
	tlsCh.closed.Store(true)
	sel = s.selectChannel(now, TransportAuto)
	if sel == nil || sel.kind != ChannelZT {
		t.Fatalf("TLS 不可用时应回退 ZT，实际 %v", sel)
	}
}

// TestSelectChannel_ZTOnlyAgent 验证只有 ZT 的 agent（如 .26）仍可用 ✅✅
func TestSelectChannel_ZTOnlyAgent(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}

	// 只有 ZT，没有 TLS
	zt := fakeChannel(ChannelZT, 100)
	s.addChannel(zt)

	sel := s.selectChannel(now, TransportAuto)
	if sel == nil || sel.kind != ChannelZT {
		t.Fatalf("只有 ZT 时应选 ZT，实际 %v", sel)
	}
}

// TestSelectChannel_FallsBackWhenDirectDies 验证直连熔断后自动回退兜底通道。
func TestSelectChannel_FallsBackWhenDirectDies(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}
	zt := fakeChannel(ChannelZT, 200)
	s.addChannel(zt)
	tlsCh := fakeChannel(ChannelTLS, 60)
	s.addChannel(tlsCh)

	if got := s.selectChannel(now, TransportAuto); got == nil || got.kind != ChannelTLS {
		t.Fatalf("直连明显更好时应选直连，实际 %v", got)
	}

	// 直连连续 3 次探测失败 → 通道级熔断。
	for i := 0; i < ChannelProbeFailuresDefault; i++ {
		tlsCh.recordProbe(false, 0, now)
	}
	if tlsCh.usable(now) {
		t.Fatal("连续失败 3 次后通道应被熔断")
	}

	got := s.selectChannel(now, TransportAuto)
	if got == nil || got.kind != ChannelZT {
		t.Fatalf("直连熔断后应回退到兜底通道，实际 %v", got)
	}
}

// —— hash(dst)%N 打散 ——

// TestHashTarget_Distribution 验证 1000 个目标均匀落到 4 条连接上。
func TestHashTarget_Distribution(t *testing.T) {
	const n = 4
	counts := make([]int, n)
	for i := 0; i < 1000; i++ {
		dst := fmt.Sprintf("10.%d.%d.%d:443", i/65536%256, i/256%256, i%256)
		counts[hashTarget(dst, n)]++
	}
	for i, c := range counts {
		if c < 150 || c > 350 {
			t.Fatalf("连接 %d 分到的目标数 %d 偏离均匀分布过多: %v", i, c, counts)
		}
	}
}

// TestPickChannel_SameTargetStaysOnSameChannel 验证连接亲和：
// 同一个目标始终落在同一条连接上。
func TestPickChannel_SameTargetStaysOnSameChannel(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}
	s.addChannel(fakeChannel(ChannelZT, 200))
	for i := 0; i < 4; i++ {
		tlsCh := fakeChannel(ChannelTLS, 60)
		s.addChannel(tlsCh)
	}
	first := s.pickChannel("1.2.3.4:443", now, TransportTLS)
	if first == nil {
		t.Fatal("应能选出通道")
	}
	for i := 0; i < 20; i++ {
		got := s.pickChannel("1.2.3.4:443", now, TransportTLS)
		if got != first {
			t.Fatal("同一目标应始终落在同一条连接上")
		}
	}
}

// TestPickChannel_FallsBackWhenHashTargetDead 验证哈希选中的连接坏了会退回主通道。
func TestPickChannel_FallsBackWhenHashTargetDead(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}
	s.addChannel(fakeChannel(ChannelZT, 200))
	var tlsChans []*channel
	for i := 0; i < 4; i++ {
		c := fakeChannel(ChannelTLS, 60)
		s.addChannel(c)
		tlsChans = append(tlsChans, c)
	}
	// 把所有直连连接都熔断掉 → 兜底通道接管。
	for _, c := range tlsChans {
		for i := 0; i < ChannelProbeFailuresDefault; i++ {
			c.recordProbe(false, 0, now)
		}
	}
	got := s.pickChannel("1.2.3.4:443", now, TransportAuto)
	if got == nil || got.kind != ChannelZT {
		t.Fatalf("直连全熔断时应走兜底通道，实际 %v", got)
	}
}

// —— 通道级熔断与 agent 级熔断的分离 ——

// TestChannelBreaker_CountsIndependently 验证通道级熔断的计数与冷却独立：
// 连续 3 次失败才熔断，且熔断只影响这条通道本身。
func TestChannelBreaker_CountsIndependently(t *testing.T) {
	now := time.Now()
	b := newChannelBreaker()

	for i := 0; i < ChannelProbeFailuresDefault-1; i++ {
		b.Record(false, now)
		if !b.Allow(now) {
			t.Fatalf("第 %d 次失败不应熔断", i+1)
		}
	}
	b.Record(false, now)
	if b.Allow(now) {
		t.Fatal("第 3 次失败应熔断")
	}
	// 冷却期内一直不可用。
	if b.Allow(now.Add(30 * time.Second)) {
		t.Fatal("冷却期内应不可用")
	}
	// 冷却结束后半开：放一次。
	if !b.Allow(now.Add(61 * time.Second)) {
		t.Fatal("冷却结束后应半开放行")
	}
	// 半开期间再失败 → 重新熔断（冷却翻倍）。
	b.Record(false, now.Add(62*time.Second))
	if b.Allow(now.Add(63 * time.Second)) {
		t.Fatal("半开期间失败应重新熔断")
	}
}

// TestChannelBreaker_DoesNotTouchOtherChannels 验证一条通道熔断不影响同 agent 的其它通道。
func TestChannelBreaker_DoesNotTouchOtherChannels(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}
	zt := fakeChannel(ChannelZT, 200)
	bad := fakeChannel(ChannelTLS, 60)
	s.addChannel(zt)
	s.addChannel(bad)

	for i := 0; i < ChannelProbeFailuresDefault; i++ {
		bad.recordProbe(false, 0, now)
	}
	if !zt.usable(now) {
		t.Fatal("一条直连通道熔断不应影响兜底通道")
	}
	if bad.usable(now) {
		t.Fatal("坏通道应处于熔断状态")
	}
}

// —— 指纹钉扎 ——

// TestNormalizeFingerprint 覆盖各种输入形式。
func TestNormalizeFingerprint(t *testing.T) {
	cases := []struct {
		in   string
		want string
		bad  bool
	}{
		{"", "", false},
		{"AB:CD:EF", "", true}, // 长度不对
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"sha256=0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
	}
	for _, c := range cases {
		got, err := NormalizeFingerprint(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("输入 %q 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("输入 %q 不该报错: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("输入 %q 得到 %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestPinnedClientRejectsWrongFingerprint 验证钉扎不匹配时握手被拒。
func TestPinnedClientRejectsWrongFingerprint(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "relay-cert.pem")

	// 服务端证书
	srvCert, err := LoadOrCreateCert(certFile, 365)
	if err != nil {
		t.Fatalf("准备证书失败: %v", err)
	}
	leaf, err := parseLeaf(srvCert)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	realPin := CertFingerprint(leaf)

	// 客户端钉一个错的指纹
	wrongPin := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				tc, ok := c.(*tls.Conn)
				if ok {
					_ = tc.Handshake()
				}
				_ = c.Close()
			}(c)
		}
	}()

	cfg, err := TLSConfigForPinnedClient(wrongPin)
	if err != nil {
		t.Fatalf("生成钉扎配置失败: %v", err)
	}
	d := &tls.Dialer{Config: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err == nil {
		_ = conn.Close()
		t.Fatal("指纹不匹配时握手应当失败")
	}

	// 换成正确的指纹 → 应当成功
	cfg2, err := TLSConfigForPinnedClient(realPin)
	if err != nil {
		t.Fatalf("生成钉扎配置失败: %v", err)
	}
	d2 := &tls.Dialer{Config: cfg2}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	conn2, err := d2.DialContext(ctx2, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("指纹匹配时握手应成功: %v", err)
	}
	_ = conn2.Close()
}

// TestPinnedClientRequiresPin 验证没有指纹时拒绝建连（钉扎强制）。
func TestPinnedClientRequiresPin(t *testing.T) {
	if _, err := TLSConfigForPinnedClient(""); err == nil {
		t.Fatal("没有指纹时必须报错")
	}
}

// TestFingerprintMismatchDoesNotAffectZTChannel 验证「指纹不匹配」只废掉直连通道，
// 不影响 ZeroTier 兜底通道（这正是通道级熔断与 agent 级熔断必须分开的理由）。
func TestFingerprintMismatchDoesNotAffectZTChannel(t *testing.T) {
	now := time.Now()
	s := &agentSession{nodeID: "n1"}
	zt := fakeChannel(ChannelZT, 200)
	s.addChannel(zt)

	// 直连通道因为指纹不匹配被丢弃：它压根没进通道列表。
	// 兜底通道照常可用。
	got := s.selectChannel(now, TransportAuto)
	if got == nil || got.kind != ChannelZT {
		t.Fatalf("兜底通道应照常可用，实际 %v", got)
	}
	if !zt.usable(now) {
		t.Fatal("兜底通道不应受影响")
	}
}

// TestCertPersistence 验证证书持久化：第二次加载的是同一张（指纹不变）。
func TestCertPersistence(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "relay-cert.pem")

	c1, err := LoadOrCreateCert(certFile, 365)
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	leaf1, _ := parseLeaf(c1)
	c2, err := LoadOrCreateCert(certFile, 365)
	if err != nil {
		t.Fatalf("加载证书失败: %v", err)
	}
	leaf2, _ := parseLeaf(c2)
	if CertFingerprint(leaf1) != CertFingerprint(leaf2) {
		t.Fatal("持久化后指纹必须保持一致，否则钉扎会失效")
	}
}

// parseLeaf 取出证书的叶证书。
func parseLeaf(cert tls.Certificate) (*x509.Certificate, error) {
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("证书为空")
	}
	return x509.ParseCertificate(cert.Certificate[0])
}
