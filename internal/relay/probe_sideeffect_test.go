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
//	业务走 ZT（评分高），探测按 tls 模式选
//	⇒ 排除的必须是 **ZT 那条**（真正承载业务的那条）
//	⇒ 而不是「tls 模式里评分最高的那条」
func TestPickProbeChannel_ExcludesGlobalBusinessPrimary(t *testing.T) {
	s := &agentSession{nodeID: "n-mixed"}
	// ★ fakeChannel 的第二个参数是 **RTT**，越小越快、评分越高
	// ZT 通道更快 ⇒ 业务会选它
	zt := fakeChannel(ChannelZT, 20)
	tls := fakeChannel(ChannelTLS, 80)
	s.addChannel(zt)
	s.addChannel(tls)

	now := time.Now()
	business := s.selectChannel(now, TransportAuto)
	if business != zt {
		t.Fatalf("业务主通道应是 ZT 那条，实际 %v", business)
	}

	// 探测走 tls 模式
	probe := s.pickProbeChannel(now, TransportTLS)
	if probe == nil {
		t.Fatal("拿不到探测通道")
	}
	// ★ 只有一条 TLS 通道，而它是探测唯一可选的 ⇒ 退化用它
	//   （但**不能**是 ZT —— 那是业务主通道 ）
	if probe == zt {
		t.Fatal("探测不该选到业务主通道（ZT 那条）")
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
