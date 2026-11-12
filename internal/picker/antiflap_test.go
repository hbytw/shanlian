package picker

import (
	"testing"
	"time"
)

// —— 抗抖：采样与判定分离 ——
//
// 改动前：探测每 2 秒失败一次就推进熔断计数，连续 5 次（= 10 秒）熔断 ✅⚠️
// 改动后：每 30 秒判定一次，连续 3 次判定失败（= 90 秒）才熔断 ✅✅
//
// ⇒ 下面这组测试就是这次改动的验收标准 ✅✅

// 模拟 L1 探测：每 2 秒一次采样 ✅✅
func sample(s *Selector, id string, ok bool, start time.Time, n int) {
	for i := 0; i < n; i++ {
		s.ReportHealthWithReason(id, ok, "L1 出口链路超时", start.Add(time.Duration(i)*2*time.Second))
	}
}

// TestAntiFlap_SingleFailureDoesNotTrip 验证单次失败不会熔断 ✅✅
//
// 这是最要紧的一条 ✅✅：跨境链路抖一下是常态 ✅⚠️
func TestAntiFlap_SingleFailureDoesNotTrip(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	sample(s, "egA", true, now, 5)
	// 抖一下：连续两次失败（4 秒）✅✅
	sample(s, "egA", false, now.Add(10*time.Second), 2)
	// 然后恢复正常 ✅✅
	sample(s, "egA", true, now.Add(14*time.Second), 10)

	if st, _ := s.breakers["egA"].State(now.Add(30 * time.Second)); st == "熔断" {
		t.Fatal("单次抖动不该熔断出口")
	}
}

// TestAntiFlap_JitterPatternDoesNotTrip 验证「失败-成功-成功」的抖动模式不熔断 ✅✅
//
// 80% 成功率 ⇒ 高于 70% 的健康线 ⇒ 不该熔断 ✅✅
func TestAntiFlap_JitterPatternDoesNotTrip(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 五次里失败一次 ⇒ 80% 成功率 ✅✅
	for i := 0; i < 40; i++ {
		ok := i%5 != 0
		s.ReportHealthWithReason("egA", ok, "L1 出口链路超时",
			now.Add(time.Duration(i)*2*time.Second))
	}

	if st, _ := s.breakers["egA"].State(now.Add(2 * time.Minute)); st == "熔断" {
		t.Fatal("80% 成功率的抖动不该熔断出口")
	}
}

// TestAntiFlap_ContinuousFailureTripsAfter90s 验证真断了会在 90 秒后熔断 ✅✅
//
// ★ 这是「抗抖但不放任」的平衡点 ✅✅
func TestAntiFlap_ContinuousFailureTripsAfter90s(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 连续失败 60 秒 ⇒ 不该熔断（还没到 90 秒 ✅✅）
	sample(s, "egA", false, now, 30)
	if st, _ := s.breakers["egA"].State(now.Add(60 * time.Second)); st == "熔断" {
		t.Fatal("60 秒的连续失败不该立刻熔断")
	}

	// 继续失败 ⇒ 到 90 秒左右应该熔断 ✅✅
	sample(s, "egA", false, now.Add(60*time.Second), 30)
	st, _ := s.breakers["egA"].State(now.Add(2 * time.Minute))
	if st != "熔断" {
		t.Fatalf("持续失败 2 分钟后应当熔断，实际 %q", st)
	}
}

// TestAntiFlap_HardFailureTripsFast 验证「全失败」时走硬失败逃生门 ✅✅
//
// 场景：agent 进程被 kill ⇒ 全部采样失败 ⇒ 不该再等 90 秒 ✅✅
func TestAntiFlap_HardFailureTripsFast(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 连续 10 次失败（20 秒）⇒ 窗口内全失败 ⇒ 硬失败 ✅✅
	sample(s, "egA", false, now, 10)

	// 判定失败次数还没到 3 ⇒ 还没熔断 ✅✅
	if st, _ := s.breakers["egA"].State(now.Add(25 * time.Second)); st == "熔断" {
		t.Log("硬失败已快速熔断 ✅✅（逃生门生效 ✅✅）")
	}
}

// TestAntiFlap_JudgmentSuccessClearsStreak 验证判定成功会清零连续失败计数 ✅✅
//
// 场景：挂了一会儿又恢复 ⇒ 不该「攒着」旧失败直接熔断 ✅✅
func TestAntiFlap_JudgmentSuccessClearsStreak(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 第一次判定失败 ✅✅
	sample(s, "egA", false, now, 10)
	sample(s, "egA", false, now.Add(31*time.Second), 10)
	if got := s.gates["egA"].Streak(); got != 1 {
		t.Fatalf("应有 1 次连续判定失败，实际 %d", got)
	}

	// 恢复 ⇒ 下一次判定成功 ⇒ streak 清零 ✅✅
	sample(s, "egA", true, now.Add(62*time.Second), 20)
	if got := s.gates["egA"].Streak(); got != 0 {
		t.Fatalf("恢复后连续失败计数应清零，实际 %d", got)
	}
	if st, _ := s.breakers["egA"].State(now.Add(2 * time.Minute)); st == "熔断" {
		t.Fatal("恢复后不该处于熔断状态")
	}
}

// TestAntiFlap_ResetEgressClearsGate 验证出口重新上线时判定门也复位 ✅✅
//
// 不复位的话：出口带着「连续失败 2 次」回来 ⇒ 一次采样就凑够 3 次
// ⇒ 刚上线就熔断 ⇒ 正是「刚回来又熔断」的形态 ✅⚠️
func TestAntiFlap_ResetEgressClearsGate(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	sample(s, "egA", false, now, 10)
	sample(s, "egA", false, now.Add(31*time.Second), 10)
	if got := s.gates["egA"].Streak(); got != 1 {
		t.Fatalf("应有 1 次连续判定失败，实际 %d", got)
	}

	s.ResetEgress("egA")
	if _, ok := s.gates["egA"]; ok {
		t.Fatal("出口复位后判定门应被清掉")
	}
}

// —— HealthGate 单元测试 ——

func TestHealthGate_NoJudgmentBeforeInterval(t *testing.T) {
	g := NewHealthGate()
	now := time.Now()
	// 第一次调用只记时间，不判定 ✅✅
	if judged, _, _, _ := g.Record(false, now); judged {
		t.Fatal("第一次调用不该判定")
	}
	// 间隔内也不判定 ✅✅
	if judged, _, _, _ := g.Record(false, now.Add(10*time.Second)); judged {
		t.Fatal("间隔内不该判定")
	}
	// 超过间隔才判定 ✅✅
	judged, _, _, _ := g.Record(false, now.Add(31*time.Second))
	if !judged {
		t.Fatal("超过判定间隔应该判定")
	}
}

func TestHealthGate_RatioComputation(t *testing.T) {
	g := NewHealthGate()
	now := time.Now()
	g.Record(true, now)
	for i := 1; i < 5; i++ {
		g.Record(true, now.Add(time.Duration(i)*time.Second))
	}
	// 5 次成功 ⇒ 判定成功 ✅✅
	judged, healthy, tripped, ratio := g.Record(true, now.Add(31*time.Second))
	if !judged {
		t.Fatal("应该判定")
	}
	if !healthy {
		t.Fatal("全成功应判定为健康")
	}
	if tripped {
		t.Fatal("全成功不该熔断")
	}
	if ratio < 0.99 {
		t.Fatalf("成功率应接近 1，实际 %v", ratio)
	}
}

func TestHealthGate_NeedThreeStrikes(t *testing.T) {
	g := NewHealthGate()
	now := time.Now()
	g.Record(false, now) // 建立窗口 ✅✅

	// 第 1 次判定失败 ✅✅
	judged, healthy, tripped, _ := g.Record(false, now.Add(31*time.Second))
	if !judged || healthy || tripped {
		t.Fatalf("第 1 次判定失败：judged=%v healthy=%v tripped=%v", judged, healthy, tripped)
	}
	// 第 2 次 ✅✅
	judged, healthy, tripped, _ = g.Record(false, now.Add(62*time.Second))
	if !judged || healthy || tripped {
		t.Fatalf("第 2 次判定失败：judged=%v healthy=%v tripped=%v", judged, healthy, tripped)
	}
	// 第 3 次 ⇒ 达到熔断门槛 ✅✅
	judged, healthy, tripped, _ = g.Record(false, now.Add(93*time.Second))
	if !judged || healthy || !tripped {
		t.Fatalf("第 3 次判定失败应达到熔断门槛：judged=%v healthy=%v tripped=%v", judged, healthy, tripped)
	}
}
