package relay

import (
	"testing"
	"time"
)

// newProbeTestSession 造一个带 n 条 TLS 通道的会话
func newProbeTestSession(n int) *agentSession {
	s := &agentSession{nodeID: "n-probe"}
	for i := 0; i < n; i++ {
		// 分数刻意不同 ⇒ 主通道确定
		s.addChannel(fakeChannel(ChannelTLS, float64(50+i*10)))
	}
	return s
}

// TestPickProbeChannelPrefersNonPrimary 验证探测优先选**非业务主通道**
//
// 背景 ：探测与业务共享同一条 TCP 的拥塞窗口
// ⇒ 业务大流量会把探测挤到后面 ⇒ 探测超时 ⇒ 误判「出口坏了」⇒ 熔断级联
func TestPickProbeChannelPrefersNonPrimary(t *testing.T) {
	s := newProbeTestSession(3)

	now := time.Now()
	primary := s.selectChannel(now, TransportAuto)
	if primary == nil {
		t.Fatal("拿不到主通道")
	}

	probe := s.pickProbeChannel(now, TransportAuto)
	if probe == nil {
		t.Fatal("拿不到探测通道")
	}
	if probe == primary {
		t.Fatal("探测通道应避开业务主通道，却选中了同一条")
	}
}

// TestPickProbeChannelPrefersLeastBusy 验证在多条备选里选在途流最少的
func TestPickProbeChannelPrefersLeastBusy(t *testing.T) {
	s := newProbeTestSession(3)

	now := time.Now()
	primary := s.selectChannel(now, TransportAuto)
	if primary == nil {
		t.Fatal("拿不到主通道")
	}

	var others []*channel
	for _, c := range s.channels() {
		if c != primary {
			others = append(others, c)
		}
	}
	if len(others) < 2 {
		t.Skipf("非主通道不足（%d），跳过 ", len(others))
	}
	others[0].inflight.Add(50)
	others[1].inflight.Add(1)

	probe := s.pickProbeChannel(now, TransportAuto)
	if probe == nil {
		t.Fatal("拿不到探测通道")
	}
	if probe == others[0] {
		t.Fatal("探测通道应选在途流少的那条，而不是在途流多的")
	}
}

// TestPickProbeChannelFallsBackToPrimary 验证只有一条通道时退化到它
//
// 退化是必须的 ：总不能让「没有别的通道」变成「不探测」——那更糟
func TestPickProbeChannelFallsBackToPrimary(t *testing.T) {
	s := newProbeTestSession(1)

	now := time.Now()
	primary := s.selectChannel(now, TransportAuto)
	if primary == nil {
		t.Fatal("拿不到主通道")
	}
	probe := s.pickProbeChannel(now, TransportAuto)
	if probe == nil {
		t.Fatal("拿不到探测通道")
	}
	if probe != primary {
		t.Fatal("只有一条通道时，探测应退化到主通道")
	}
}

// TestPickProbeChannelNoneWhenNoChannel 验证无可用通道时返回 nil
func TestPickProbeChannelNoneWhenNoChannel(t *testing.T) {
	s := newProbeTestSession(2)
	for _, c := range s.channels() {
		c.closed.Store(true)
	}
	if got := s.pickProbeChannel(time.Now(), TransportAuto); got != nil {
		t.Fatal("没有可用通道时应返回 nil")
	}
}

// TestPickProbeChannelRespectsMode 验证 zt 模式下探测仍走 ZT 通道
func TestPickProbeChannelRespectsMode(t *testing.T) {
	s := &agentSession{nodeID: "n-mode"}
	s.addChannel(fakeChannel(ChannelZT, 200))
	s.addChannel(fakeChannel(ChannelTLS, 40))

	probe := s.pickProbeChannel(time.Now(), TransportZT)
	if probe == nil {
		t.Fatal("拿不到探测通道")
	}
	if probe.kind != ChannelZT {
		t.Fatalf("zt 模式的探测通道必须是 ZT，实际 %v", probe.kind)
	}
}
