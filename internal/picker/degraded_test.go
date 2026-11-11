package picker

import (
	"testing"
	"time"
)

// TestPickSwitchesWhenCurrentDegraded 验证：当前出口明显劣化时，即使新出口
// 的优势不到 margin 门槛，也会切换。
//
// 真机事故的回归测试：韩国出口 617ms / 8.7% 丢包，只因比最优出口低 4.5 分
// （不到 8 分门槛）就一直不切，用户看到 youtube 时通时不通。
func TestPickSwitchesWhenCurrentDegraded(t *testing.T) {
	sel := NewSelector(DefaultWeights())
	now := time.Now()

	// 先让 A 成为当前出口（健康）
	sel.UpdateL1("A", 100, 0.0, 10, now.Add(-60*time.Second))
	sel.UpdateL1("B", 120, 0.0, 10, now.Add(-60*time.Second))
	d := sel.Pick(nil, "", []string{"A", "B"}, now.Add(-50*time.Second))
	if d.Egress != "A" {
		t.Fatalf("初始应选 A，实际 %s", d.Egress)
	}

	// A 劣化：617ms、8.7% 丢包；B 保持健康。
	// 注意要多喂几次：指标是 EWMA 平滑的，单次采样不足以跨过阈值。
	for i := 0; i < 12; i++ {
		sel.UpdateL1("A", 617, 0.087, 5, now)
		sel.UpdateL1("B", 120, 0.001, 10, now)
	}

	// 冷却期未过也要切（滞回与冷却都应让路）
	d2 := sel.Pick(nil, "", []string{"A", "B"}, now.Add(time.Second))
	if d2.Egress != "B" {
		t.Fatalf("A 已劣化，应切到 B；实际仍是 %s（理由: %s）", d2.Egress, d2.Reason)
	}
}

// TestPickKeepsCurrentWhenBothHealthy 验证：两个出口都健康时，滞回仍然生效。
//
// 这是防抖的本意 —— 不能因为新出口领先一点点就切，否则用户会看到流反复断。
func TestPickKeepsCurrentWhenBothHealthy(t *testing.T) {
	sel := NewSelector(DefaultWeights())
	now := time.Now()

	sel.UpdateL1("A", 100, 0.0, 10, now.Add(-60*time.Second))
	sel.UpdateL1("B", 120, 0.0, 10, now.Add(-60*time.Second))
	d := sel.Pick(nil, "", []string{"A", "B"}, now.Add(-50*time.Second))
	if d.Egress != "A" {
		t.Fatalf("初始应选 A，实际 %s", d.Egress)
	}

	// B 略好一点，但差距不到 margin（8 分）
	for i := 0; i < 12; i++ {
		sel.UpdateL1("A", 100, 0.0, 10, now)
		sel.UpdateL1("B", 90, 0.0, 11, now)
	}

	d2 := sel.Pick(nil, "", []string{"A", "B"}, now.Add(10*time.Second))
	if d2.Egress != "A" {
		t.Fatalf("两个都健康且差距小，应保持 A；实际切到了 %s", d2.Egress)
	}
}
