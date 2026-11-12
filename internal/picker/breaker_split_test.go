package picker

import (
	"testing"
	"time"
)

// —— 用户建连失败 vs 出口本身故障 ——
//
// 线上症状：TCP 接受 658 条 / 失败 3874 条（85% 失败），
// 客户端连国内站点都 ERR。
//
// 根因：用户访问打不开的站点被记成「出口坏了」→ 出口被熔断排除
// → 可用出口更少 → 更多请求失败 → 更多出口被熔断。死亡螺旋。

// TestSelector_TargetFailureDoesNotTripBreaker 覆盖验收项 a：
// 用户目标不可达**不会**推进熔断计数（连失败 10 次也不熔断）。
func TestSelector_TargetFailureDoesNotTripBreaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	for i := 0; i < 10; i++ {
		s.ReportTargetFailure("egA")
	}

	// 熔断器连创建都不该创建 —— 计数完全没动过。
	if b := s.breakers["egA"]; b != nil {
		t.Fatal("目标不可达不该创建熔断器")
	}
	if n := s.TargetFailures("egA"); n != 10 {
		t.Fatalf("目标不可达次数应为 10，实际 %d", n)
	}

	// 出口依然可选。
	s.UpdateL1("egA", 20, 0, 100, now)
	d := s.Pick(nil, "", []string{"egA"}, now)
	if d.Egress != "egA" {
		t.Fatalf("目标不可达不应影响选路，实际 %q（%s）", d.Egress, d.Reason)
	}
}

// TestSelector_EgressFailureTripsBreaker 覆盖验收项 b：
// 连 agent 本身失败**会**推进熔断计数。
func TestSelector_EgressFailureTripsBreaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// ★ 采样与判定分离 ⇒ 需要推进到判定 ✅✅
	tripBreakerWithReason(t, s, "egA", "出口链路失败：超时", now)
	b := s.breakers["egA"]
	if b == nil {
		t.Fatal("应已创建熔断器")
	}
	if st, _ := b.State(now); st != "熔断" {
		t.Fatalf("出口链路连续失败应熔断，实际 %q", st)
	}
	if b.Allow(now) {
		t.Fatal("熔断中的出口不应被放行")
	}
}

// TestSelector_L1SuccessDoesNotClearL2 覆盖验收项 c：
// L1 成功**不会**清掉 L2 的独立失败计数。
//
// 这是 L1/L2 分离的核心保证：以前两者共用一个计数器，
// L1 每 2 秒成功一次就把 L2 的失败清零，L2 永远无法单独熔断。
func TestSelector_L1SuccessDoesNotClearL2(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	later := now.Add(MinEgressUptime + time.Second)

	// L2 失败一次
	s.ReportL2Health("egA", false, "L2 连不上 1.1.1.1（超时）", later)
	if b2 := s.l2Breakers["egA"]; b2 == nil {
		t.Fatal("L2 应有独立的熔断器")
	}

	// L1 成功 —— 绝不能清掉 L2 的失败计数
	s.ReportHealthWithReason("egA", true, "", later.Add(time.Second))

	// L2 再失败一次 → 达到 L2 自己的阈值 → 熔断
	l2time := later.Add(2 * time.Second)
	s.ReportL2Health("egA", false, "L2 连不上 1.1.1.1（超时）", l2time)
	b2 := s.l2Breakers["egA"]
	if st, _ := b2.State(l2time); st != "熔断" {
		t.Fatalf("L2 连续失败应独立熔断，实际 %q", st)
	}
	// L1 那边不该被熔断（它一直是成功的）
	if b1 := s.breakers["egA"]; b1 != nil {
		if st, _ := b1.State(l2time); st == "熔断" {
			t.Fatal("L1 一直成功，不该熔断")
		}
	}
}

// TestSelector_L1FailureDoesNotClearL2 反向验证：L1 失败也不该影响 L2 的计数。
func TestSelector_L1FailureDoesNotClearL2(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	later := now.Add(MinEgressUptime + time.Second)

	s.ReportL2Health("egA", false, "L2 连不上 1.1.1.1（超时）", later)
	s.ReportHealthWithReason("egA", false, "出口链路失败：超时", later.Add(time.Second))

	b2 := s.l2Breakers["egA"]
	if b2 == nil {
		t.Fatal("L2 应有独立的熔断器")
	}
	if st, _ := b2.State(later.Add(time.Second)); st == "熔断" {
		t.Fatal("L2 只失败一次，不该熔断")
	}
}

// TestSelector_L2TripExcludesEgress 确认 L2 熔断会把出口排除出候选集。
func TestSelector_L2TripExcludesEgress(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL1("egB", 200, 0.2, 5, now)
	later := now.Add(MinEgressUptime + time.Second)

	for i := 0; i < l2BreakerThreshold; i++ {
		s.ReportL2Health("egA", false, "L2 连不上 1.1.1.1（超时）", later)
	}

	d := s.Pick(nil, "", []string{"egA", "egB"}, later)
	if d.Egress == "egA" {
		t.Fatalf("L2 熔断的出口应被排除，实际 %q（%s）", d.Egress, d.Reason)
	}
	for _, c := range d.All {
		if c.NodeID == "egA" && c.Eligible {
			t.Fatal("egA 应标记为不可用")
		}
	}
}

// TestSelector_ResetEgressClearsL2Breaker 确认掉线复位时 L2 熔断器也被清掉。
func TestSelector_ResetEgressClearsL2Breaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	later := now.Add(MinEgressUptime + time.Second)
	for i := 0; i < l2BreakerThreshold; i++ {
		s.ReportL2Health("egA", false, "L2 连不上 1.1.1.1（超时）", later)
	}
	if b := s.l2Breakers["egA"]; b == nil {
		t.Fatal("L2 熔断器应存在")
	}

	s.ResetEgress("egA")
	if b := s.l2Breakers["egA"]; b != nil {
		t.Fatal("掉线复位后 L2 熔断器应被删除")
	}
	if n := s.TargetFailures("egA"); n != 0 {
		t.Fatalf("掉线复位后目标不可达计数应清零，实际 %d", n)
	}
}

// TestSelector_L2ReasonSurfaced 确认 L2 的失败原因出现在状态快照里。
func TestSelector_L2ReasonSurfaced(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.MarkOnline("egA", now)
	s.UpdateL1("egA", 20, 0, 100, now)
	later := now.Add(MinEgressUptime + time.Second)

	const reason = "L2 连不上 1.1.1.1（超时）"
	for i := 0; i < l2BreakerThreshold; i++ {
		s.ReportL2Health("egA", false, reason, later)
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
		t.Fatalf("L2 熔断原因应被带出，实际 %q", c.BreakerReason)
	}
}
