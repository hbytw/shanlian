package relay

import (
	"testing"
	"time"
)

// —— 2026-11-12 晚高峰修复：开流失败换下一条通道 ——
//
// 原来 Open() 只 pick 一条通道，开流失败直接返回错误
// ⇒ 撞上「半死通道」（TCP 在、yamux 会话坏、探测还没发现）
//   每次建连都失败、且不回退到其他通道 ⇒ 92% 建连失败率 ✅⚠️

// TestCandidatesFor_ReturnsOrdered 验证候选列表：选中的排第一，其他可用的跟后面 ✅✅
func TestCandidatesFor_ReturnsOrdered(t *testing.T) {
	s := &agentSession{nodeID: "n1"}
	tls1 := fakeChannel(ChannelTLS, 20)
	tls2 := fakeChannel(ChannelTLS, 80)
	zt := fakeChannel(ChannelZT, 40)
	s.addChannel(tls1)
	s.addChannel(tls2)
	s.addChannel(zt)

	now := time.Now()
	// 先定主通道（auto 模式 TLS 优先 ⇒ 选 tls1）
	sel := s.selectChannel(now, TransportAuto)
	if sel == nil {
		t.Fatal("拿不到主通道")
	}

	cands := s.candidatesFor("1.2.3.4:443", now, TransportAuto)
	if len(cands) == 0 {
		t.Fatal("候选列表不应为空")
	}
	if cands[0] != sel {
		t.Fatalf("候选列表第一条应是选中的通道，实际 %v", cands[0])
	}
	// 其余可用通道都在列表里
	found := map[*channel]bool{}
	for _, c := range cands {
		found[c] = true
	}
	for _, c := range []*channel{tls1, tls2, zt} {
		if !found[c] {
			t.Fatalf("候选列表应包含所有可用通道，缺 %v", c)
		}
	}
}

// TestCandidatesFor_ExcludesUnusable 验证不可用通道不在候选列表 ✅✅
func TestCandidatesFor_ExcludesUnusable(t *testing.T) {
	s := &agentSession{nodeID: "n1"}
	tls1 := fakeChannel(ChannelTLS, 20)
	tls2 := fakeChannel(ChannelTLS, 80)
	s.addChannel(tls1)
	s.addChannel(tls2)

	// 关掉 tls2
	tls2.closed.Store(true)

	now := time.Now()
	cands := s.candidatesFor("1.2.3.4:443", now, TransportAuto)
	for _, c := range cands {
		if c == tls2 {
			t.Fatal("已关闭的通道不应在候选列表里")
		}
	}
}

// TestCandidatesFor_AllDeadReturnsEmpty 验证全死时返回空（调用方返回 ErrNoUsableChannel）✅✅
func TestCandidatesFor_AllDeadReturnsEmpty(t *testing.T) {
	s := &agentSession{nodeID: "n1"}
	tls1 := fakeChannel(ChannelTLS, 20)
	tls1.closed.Store(true)
	s.addChannel(tls1)

	cands := s.candidatesFor("1.2.3.4:443", time.Now(), TransportAuto)
	if len(cands) != 0 {
		t.Fatalf("全死时应返回空列表，实际 %d 条", len(cands))
	}
}

// TestRecordProbeFailureOnOpenFail 验证开流失败会记探测失败（喂给通道熔断器）✅✅
func TestRecordProbeFailureOnOpenFail(t *testing.T) {
	c := fakeChannel(ChannelTLS, 20)
	now := time.Now()

	// 模拟开流失败 ⇒ recordProbe(false)
	c.recordProbe(false, 0, now)

	// 连续失败到阈值 ⇒ 通道熔断
	for i := 0; i < c.breaker.threshold; i++ {
		c.recordProbe(false, 0, now.Add(time.Duration(i)*time.Second))
	}
	if !c.breaker.open {
		t.Fatal("连续开流失败应触发通道熔断")
	}
}
