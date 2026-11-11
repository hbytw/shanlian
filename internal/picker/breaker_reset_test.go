package picker

import (
	"net"
	"testing"
	"time"
)

// —— 熔断状态随 agent 掉线/重连复位 ——
//
// 线上症状：节点频繁熔断，网络基本不可用。
// 根因之一：熔断状态按 node_id 记，agent 掉线再回来时 node_id 不变，
// 它带着上一段的冷却期（60s 起，指数退避到 10 分钟封顶）回来，
// 继续被排除在候选集之外 —— 表现为「刚回来又熔断」。
//
// 这组测试把这条性质钉死。

// TestSelector_ReconnectResetsBreaker 覆盖验收项 a：
// agent 掉线再回来 → breaker 复位为 closed、能被选中。
func TestSelector_ReconnectResetsBreaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL1("egB", 200, 0.2, 5, now)

	// 连续失败把 egA 熔断
	for i := 0; i < breakerThreshold; i++ {
		s.ReportHealth("egA", false, now)
	}
	if st, _ := s.breakers["egA"].State(now); st != "熔断" {
		t.Fatalf("egA 应处于熔断，实际 %q", st)
	}
	if d := s.Pick(nil, "", []string{"egA", "egB"}, now); d.Egress == "egA" {
		t.Fatalf("熔断中的 egA 不应被选中，实际 %q", d.Egress)
	}

	// agent 掉线 → 全部状态复位（熔断器整个删掉）
	s.ResetEgress("egA")
	if b := s.breakers["egA"]; b != nil {
		t.Fatal("掉线后熔断器应被删除（否则会带着冷却期回来）")
	}

	// agent 重新接入 → 记下上线时刻
	s.MarkOnline("egA", now)
	if _, ok := s.OnlineSince("egA"); !ok {
		t.Fatal("应记录上线时刻")
	}

	// 复位后 egA 评分更高，应当能被选中
	s.UpdateL1("egA", 20, 0, 100, now.Add(time.Second))
	d := s.Pick(nil, "", []string{"egA", "egB"}, now.Add(2*time.Second))
	if d.Egress != "egA" {
		t.Fatalf("复位后应能选中 egA，实际 %q（%s）", d.Egress, d.Reason)
	}
	for _, c := range d.All {
		if c.NodeID == "egA" && !c.Eligible {
			t.Fatal("复位后 egA 应重新可用")
		}
	}
}

// TestSelector_ResetEgressClearsEverything 确认复位是「全清」而不是只清熔断器。
func TestSelector_ResetEgressClearsEverything(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL2("egA", "youtube.com", 30, 0, 40, true, now)
	s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA"}, now)
	if s.Table().Len() == 0 {
		t.Fatal("应已写入路由表")
	}
	if s.Current() != "egA" {
		t.Fatalf("当前出口应为 egA，实际 %q", s.Current())
	}

	s.ResetEgress("egA")

	if s.Table().Len() != 0 {
		t.Fatalf("复位后路由表应为空，实际 %d 条", s.Table().Len())
	}
	if s.Current() != "" {
		t.Fatalf("复位后当前出口应清空，实际 %q", s.Current())
	}
	s.mu.RLock()
	_, hasL1 := s.l1["egA"]
	hasL2 := false
	for _, m := range s.l2 {
		if _, ok := m["egA"]; ok {
			hasL2 = true
		}
	}
	s.mu.RUnlock()
	if hasL1 {
		t.Fatal("复位后 L1 采样应被清除")
	}
	if hasL2 {
		t.Fatal("复位后 L2 采样应被清除")
	}
}

// —— 降低误判：最小存活时长 ——

// TestSelector_GracePeriodPreventsTrip 覆盖验收项 b：
// 上线未满最小存活时长时，探测失败不熔断。
func TestSelector_GracePeriodPreventsFailures(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)

	// 上线 1 分钟内连续失败（远超阈值）也不该熔断：
	// 刚上线的出口最容易抖，此时熔断就是「刚回来又熔断」。
	for i := 0; i < breakerThreshold*3; i++ {
		s.ReportHealth("egA", false, now.Add(time.Duration(i)*time.Second))
	}
	b := s.breakers["egA"]
	if b != nil {
		// 万一之前就有熔断器，它也必须处于「正常」。
		if st, _ := b.State(now.Add(time.Minute)); st == "熔断" {
			t.Fatalf("最小存活时长内不应熔断，实际 %q", st)
		}
		if !b.Allow(now.Add(time.Minute)) {
			t.Fatal("最小存活时长内该出口应仍然可用")
		}
	}
}

// TestSelector_TripsAfterGracePeriod 覆盖验收项 c：
// 满存活时长后连续失败仍会熔断（别把熔断功能改没了）。
func TestSelector_TripsAfterGracePeriod(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)

	later := now.Add(MinEgressUptime + time.Second)
	for i := 0; i < breakerThreshold; i++ {
		s.ReportHealth("egA", false, later)
	}
	b := s.breakers["egA"]
	if b == nil {
		t.Fatal("应已创建熔断器")
	}
	if st, _ := b.State(later); st != "熔断" {
		t.Fatalf("超过最小存活时长后连续失败应熔断，实际 %q", st)
	}
	if b.Allow(later) {
		t.Fatal("熔断中的出口不应被放行")
	}
}

// TestSelector_NoOnlineTimeNoGracePeriod 确认「没登记上线时刻」的出口不受保护。
//
// 这是向后兼容的保证：packet 模式下的对等节点、单元测试里的直接调用，
// 行为与改动前完全一致（失败照常计入熔断计数）。
func TestSelector_NoOnlineTimeNoGracePeriod(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	for i := 0; i < breakerThreshold; i++ {
		s.ReportHealth("egA", false, now)
	}
	if st, _ := s.breakers["egA"].State(now); st != "熔断" {
		t.Fatalf("未登记上线时刻的出口应照常熔断，实际 %q", st)
	}
}

// TestSelector_OnlineSinceClearedOnReset 确认掉线时上线时刻一并清掉。
func TestSelector_OnlineSinceClearedOnReset(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	s.ResetEgress("egA")
	if _, ok := s.OnlineSince("egA"); ok {
		t.Fatal("掉线后上线时刻应被清除")
	}
}

// —— 熔断原因可读 ——

// TestSelector_BreakerReasonSurfaced 确认失败原因能出现在状态快照里。
func TestSelector_BreakerReasonSurfaced(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	later := now.Add(MinEgressUptime + time.Second)
	s.UpdateL1("egA", 20, 0, 100, later)

	const reason = "L2 连不上 1.1.1.1（超时）"
	for i := 0; i < breakerThreshold; i++ {
		s.ReportHealthWithReason("egA", false, reason, later)
	}

	snap := s.SnapshotState([]string{"egA"}, later)
	if len(snap.Candidates) != 1 {
		t.Fatalf("应有 1 个候选，实际 %d", len(snap.Candidates))
	}
	c := snap.Candidates[0]
	if c.Breaker != "熔断" {
		t.Fatalf("状态应为熔断，实际 %q", c.Breaker)
	}
	if c.BreakerReason != reason {
		t.Fatalf("熔断原因应被带出，实际 %q", c.BreakerReason)
	}
	if c.Eligible {
		t.Fatal("熔断中的出口不应可选")
	}
}

// TestSelector_ReasonKeptAcrossSuccess 确认原因不会立刻被下一次成功抹掉。
//
// L1 探测每 2 秒就成功一次；如果成功就清空原因，
// L2 探测记录的原因会被立刻抹掉，状态页就永远看不到「连不上 1.1.1.1」。
func TestSelector_ReasonKeptAcrossSuccess(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	const reason = "L2 连不上 1.1.1.1（超时）"
	s.ReportHealthWithReason("egA", false, reason, now)
	s.ReportHealthWithReason("egA", true, "", now.Add(time.Second))
	if got := s.breakers["egA"].Reason(); got != reason {
		t.Fatalf("成功不应抹掉最近一次失败原因，实际 %q", got)
	}
}
