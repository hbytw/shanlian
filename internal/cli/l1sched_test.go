package cli

import (
	"testing"
	"time"
)

// —— L1 自适应探测调度（2026-11-13 伟哥需求）——
//
// 正常备用节点降频（20 秒）、异常节点与主节点高频（2 秒）、
// 主节点失败时全员高频（准备切换）、频率上限就是 2 秒（不雪上加霜）。

// TestL1Sched_HealthyBackupSlowsDown 验证健康备用节点连续成功后降频 ✅
func TestL1Sched_HealthyBackupSlowsDown(t *testing.T) {
	g := newL1Sched()
	now := time.Now()

	// 初始：无记录 ⇒ 全部到点
	due := g.due([]string{"main", "backup"}, now)
	if len(due) != 2 {
		t.Fatalf("首次应全部到点，实际 %d", len(due))
	}

	// 备用节点连续成功 5 次 → 降频到 20 秒
	// （都记在 now 时刻 ⇒ next = now + 20s）
	for i := 0; i < l1HealthyStreak; i++ {
		g.finish("backup", true, true, false, now)
	}
	// 主节点成功（isCurrent=true）
	g.finish("main", true, true, true, now)

	// 下一次 tick（2 秒后）：主节点到点，备用未到点
	next := now.Add(l1BaseInterval)
	due = g.due([]string{"main", "backup"}, next)
	if len(due) != 1 || due[0] != "main" {
		t.Fatalf("降频后应只有主节点到点，实际 %v", due)
	}

	// 20 秒后备用节点到点
	due = g.due([]string{"main", "backup"}, now.Add(l1SlowInterval))
	if len(due) != 2 {
		t.Fatalf("20 秒后备用应到点，实际 %v", due)
	}
}

// TestL1Sched_BackupFailureBackToFast 验证备用节点失败立刻回到高频 ✅
func TestL1Sched_BackupFailureBackToFast(t *testing.T) {
	g := newL1Sched()
	now := time.Now()

	// 先降频
	for i := 0; i < l1HealthyStreak; i++ {
		g.finish("backup", true, true, false, now)
	}
	// 失败一次 → 立刻回到 2 秒节奏
	g.finish("backup", true, false, false, now.Add(l1SlowInterval))
	due := g.due([]string{"backup"}, now.Add(l1SlowInterval+l1BaseInterval))
	if len(due) != 1 {
		t.Fatalf("失败后应立刻恢复高频探测，实际 %v", due)
	}
}

// TestL1Sched_CurrentFailingPromotesAll 验证主节点失败时全员高频 ✅
func TestL1Sched_CurrentFailingPromotesAll(t *testing.T) {
	g := newL1Sched()
	now := time.Now()

	// 备用节点已降频
	for i := 0; i < l1HealthyStreak; i++ {
		g.finish("backup", true, true, false, now)
	}
	g.finish("main", true, true, true, now)

	// 确认降频生效
	due := g.due([]string{"main", "backup"}, now.Add(l1BaseInterval))
	if len(due) != 1 {
		t.Fatalf("降频后应只有主节点，实际 %v", due)
	}

	// 主节点探测失败 → 全员高频（准备切换）
	g.finish("main", true, false, true, now.Add(2*l1BaseInterval))
	due = g.due([]string{"main", "backup"}, now.Add(2*l1BaseInterval+l1BaseInterval))
	if len(due) != 2 {
		t.Fatalf("主节点失败后应全员高频，实际 %v", due)
	}

	// 主节点恢复成功 → 高频状态解除，备用回到降频节奏
	g.finish("main", true, true, true, now.Add(3*l1BaseInterval))
	due = g.due([]string{"main", "backup"}, now.Add(3*l1BaseInterval+l1BaseInterval))
	if len(due) != 1 || due[0] != "main" {
		t.Fatalf("主节点恢复后备用应回到降频，实际 %v", due)
	}
}

// TestL1Sched_SkippedRetryFast 验证跳过（无可用通道）按 2 秒重试 ✅
func TestL1Sched_SkippedRetryFast(t *testing.T) {
	g := newL1Sched()
	now := time.Now()

	g.finish("egA", false, false, false, now) // attempted=false
	due := g.due([]string{"egA"}, now.Add(l1BaseInterval))
	if len(due) != 1 {
		t.Fatalf("跳过后 2 秒应重试，实际 %v", due)
	}
	// 但 2 秒内不重试
	due = g.due([]string{"egA"}, now.Add(l1BaseInterval-time.Second))
	if len(due) != 0 {
		t.Fatalf("2 秒内不该重试，实际 %v", due)
	}
}

// TestL1Sched_PruneOffline 验证掉线节点的调度状态被清理 ✅
func TestL1Sched_PruneOffline(t *testing.T) {
	g := newL1Sched()
	now := time.Now()

	g.finish("gone", true, true, false, now)
	g.finish("stay", true, true, false, now)
	if len(g.next) != 2 {
		t.Fatalf("应有 2 条调度记录，实际 %d", len(g.next))
	}
	g.due([]string{"stay"}, now) // gone 不在 ids 里 ⇒ 被清理
	if _, ok := g.next["gone"]; ok {
		t.Fatal("掉线节点的调度状态应被清理")
	}
	if len(g.next) != 1 {
		t.Fatalf("应剩 1 条，实际 %d", len(g.next))
	}
}
