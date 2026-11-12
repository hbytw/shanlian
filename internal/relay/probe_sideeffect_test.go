package relay

import (
	"testing"
	"time"
)

// —— 2026-11-12 审计：探测路径的副作用 ——
//
// pickProbeChannel 原来调 selectChannel，而 selectChannel **会写 s.primary**
// ⇒ 探测这个「看起来只读」的路径却改了业务路由
//
// ⇒ 现在：排除**全局业务主通道** s.primary，且不改它

// TestPickProbeChannel_DoesNotChangeBusinessPrimary 验证探测不改业务主通道
func TestPickProbeChannel_DoesNotChangeBusinessPrimary(t *testing.T) {
	s := newProbeTestSession(3)
	now := time.Now()

	// 先让业务选一次主通道
	business := s.selectChannel(now, TransportAuto)
	if business == nil {
		t.Fatal("拿不到业务主通道")
	}

	// 探测在 tls 模式下跑（最容易出问题的模式 ）
	for i := 0; i < 5; i++ {
		s.pickProbeChannel(now, TransportTLS)
	}

	// ★ 业务主通道不能被探测改掉
	if got := s.Primary(); got != business {
		t.Fatalf("探测不该改业务主通道：期望 %p，实际 %p", business, got)
	}
}

// TestPickProbeChannel_ExcludesGlobalBusinessPrimary 验证排除的是全局业务主通道
//
// ★ 关键场景 ：
//
//	2026-11-12 起 auto 模式 TLS 优先 ⇒ 业务主通道是 TLS 那条
//	探测按 tls 模式选 ⇒ 排除的必须是**业务主通道那条 TLS**
//	⇒ 而不是「tls 模式里评分最高的那条」（可能就是业务主通道本身）
func TestPickProbeChannel_ExcludesGlobalBusinessPrimary(t *testing.T) {
	s := &agentSession{nodeID: "n-mixed"}
	// 两条 TLS + 一条 ZT ⇒ 业务主通道必是 TLS（TLS 优先）✅✅
	// fakeChannel 第二个参数是 RTT，越小越快、评分越高
	tlsA := fakeChannel(ChannelTLS, 20) // 评分最高 ⇒ 业务会选它
	tlsB := fakeChannel(ChannelTLS, 80)
	zt := fakeChannel(ChannelZT, 40)
	s.addChannel(tlsA)
	s.addChannel(tlsB)
	s.addChannel(zt)

	now := time.Now()
	business := s.selectChannel(now, TransportAuto)
	if business != tlsA {
		t.Fatalf("业务主通道应是评分最高的 TLS 那条，实际 %v", business)
	}

	// 探测走 tls 模式 ⇒ 应排除业务主通道 tlsA，选 tlsB
	probe := s.pickProbeChannel(now, TransportTLS)
	if probe == nil {
		t.Fatal("拿不到探测通道")
	}
	if probe == business {
		t.Fatal("探测不该选到业务主通道（承载业务流量的那条）")
	}
	if probe != tlsB {
		t.Fatalf("探测应选另一条 TLS（tlsB），实际 %v", probe)
	}
}

// TestPickProbeChannel_NoPrimaryYet 验证还没有主通道时的退化
func TestPickProbeChannel_NoPrimaryYet(t *testing.T) {
	s := newProbeTestSession(2)
	now := time.Now()

	// 不先调 selectChannel ⇒ s.primary 还是 nil
	probe := s.pickProbeChannel(now, TransportAuto)
	if probe == nil {
		t.Fatal("没有主通道时也应能选到探测通道")
	}
}
