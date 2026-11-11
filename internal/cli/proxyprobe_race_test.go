package cli

import (
	"sync"
	"testing"
	"time"
)

// TestL1Windows_ConcurrentRecord 是 2026-11-11 21:51 线上崩溃的回归测试。
//
// 现场：路由器上的 SLX 进程直接死于
//
//	fatal error: concurrent map writes
//
// 堆栈指向 probeL1Once 写 `windows` / `lastGood` 那两行。
// 成因：L1 采样被改成了「每个 agent 一个 goroutine 并行」，
// 而这两个 map 是所有 agent 共享的裸 map，没有任何锁。
//
// 这个测试要证明两件事：
//  1. 并发写不 panic、窗口内容自洽；
//  2. 采样结果不串号（每个 agent 的丢包率只反映它自己的样本）。
func TestL1Windows_ConcurrentRecord(t *testing.T) {
	win := newL1Windows()

	const (
		agents  = 8
		rounds  = 300
		okEvery = 3 // 每 3 次里成功 1 次 ⇒ 期望丢包率 ≈ 2/3
	)

	var wg sync.WaitGroup
	for a := 0; a < agents; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			id := string(rune('A' + a))
			for i := 0; i < rounds; i++ {
				ok := i%okEvery == 0
				var rtt time.Duration
				if ok {
					rtt = time.Duration(10+a) * time.Millisecond
				}
				win.record(id, ok, rtt)
			}
		}(a)
	}
	wg.Wait()

	// 每个 agent 的窗口长度都应该是上限，且内容自洽。
	win.mu.Lock()
	defer win.mu.Unlock()
	for a := 0; a < agents; a++ {
		id := string(rune('A' + a))
		samples := win.windows[id]
		if len(samples) != proxLossWindow {
			t.Fatalf("agent %s 的采样窗口长度 = %d，期望 %d", id, len(samples), proxLossWindow)
		}
		lost := 0
		for _, v := range samples {
			if !v {
				lost++
			}
		}
		loss := float64(lost) / float64(len(samples))
		if loss <= 0.5 || loss >= 0.8 {
			t.Fatalf("agent %s 的丢包率 = %.3f，期望在 0.5~0.8 之间（不该串号）", id, loss)
		}
		// 最后一次成功时的 RTT 必须是这个 agent 自己的。
		want := float64(10+a) * float64(time.Millisecond) / float64(time.Millisecond)
		if got := win.lastGood[id]; got != want {
			t.Fatalf("agent %s 的 lastGood = %v，期望 %v（不该串号）", id, got, want)
		}
	}
}

// TestL1Windows_PruneKeepsAlive 确认下线 agent 的状态会被清掉。
func TestL1Windows_PruneKeepsAlive(t *testing.T) {
	win := newL1Windows()
	win.record("a", true, 10*time.Millisecond)
	win.record("b", true, 20*time.Millisecond)

	win.prune(map[string]bool{"a": true})

	win.mu.Lock()
	defer win.mu.Unlock()
	if _, ok := win.windows["b"]; ok {
		t.Fatal("已下线的 agent 应当被清掉")
	}
	if _, ok := win.windows["a"]; !ok {
		t.Fatal("在线的 agent 不该被清掉")
	}
	if _, ok := win.lastGood["a"]; !ok {
		t.Fatal("在线 agent 的 lastGood 不该被清掉")
	}
}
