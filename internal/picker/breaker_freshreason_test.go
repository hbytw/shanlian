package picker

import (
	"testing"
	"time"
)

// TestBreaker_FreshReasonExpires 覆盖 2026-11-13 伟哥之问：
// 「为什么香港还显示失败」—— 一次历史失败的留痕不该在全绿的出口上
// 永远挂着。Reason() 仍永久保留（排障用），展示路径 30 分钟过期。
func TestBreaker_FreshReasonExpires(t *testing.T) {
	b := NewBreaker()
	t0 := time.Now()

	b.RecordWithReason(false, "出口链路失败", t0)

	if got := b.FreshReason(t0.Add(5 * time.Minute)); got != "出口链路失败" {
		t.Fatalf("5 分钟内应展示，实际 %q", got)
	}
	if got := b.FreshReason(t0.Add(failureNoteTTL + time.Minute)); got != "" {
		t.Fatalf("超过展示窗口应过期，实际 %q", got)
	}
	// 数据本身不丢：排障仍可读
	if got := b.Reason(); got != "出口链路失败" {
		t.Fatalf("Reason() 应永久保留，实际 %q", got)
	}
}

// TestBreaker_FreshReasonZeroTime 未失败过的熔断器不展示任何留痕。
func TestBreaker_FreshReasonZeroTime(t *testing.T) {
	b := NewBreaker()
	if got := b.FreshReason(time.Now()); got != "" {
		t.Fatalf("从未失败不该有留痕，实际 %q", got)
	}
}
