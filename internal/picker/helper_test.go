package picker

import (
	"testing"
	"time"
)

// —— 测试辅助 ——
//
// ★ 现在的语义 ✅✅：采样与判定是分离的 ✅✅
//   ⇒ 「连续失败 N 次就熔断」不再成立 ✅✅
//   ⇒ 熔断需要：连续 healthJudgeFailThreshold 次**判定**失败 ✅✅
//   ⇒ 每次判定要隔一个 healthJudgeInterval ✅✅
//
// ⇒ 这两个辅助函数把「推进到熔断」这件事写清楚 ✅✅

// tripBreaker 把某个出口驱动到熔断状态 ✅✅
//
// 用**模拟时间**推进，不真的等待 ✅✅
func tripBreaker(t *testing.T, s *Selector, id string, now time.Time) {
	t.Helper()
	step := healthJudgeInterval / 2
	at := now
	// 判定次数 = 失败阈值 + 1（多一次留余量 ✅✅）
	for i := 0; i < healthJudgeFailThreshold*2+2; i++ {
		at = at.Add(step)
		s.ReportHealth(id, false, at)
	}
}

// tripBreakerWithReason 同上，但带上失败原因 ✅✅
func tripBreakerWithReason(t *testing.T, s *Selector, id, reason string, now time.Time) {
	t.Helper()
	step := healthJudgeInterval / 2
	at := now
	for i := 0; i < healthJudgeFailThreshold*2+2; i++ {
		at = at.Add(step)
		s.ReportHealthWithReason(id, false, reason, at)
	}
}
