package cli

import (
	"net"
	"testing"
	"time"
)

// TestBWTrackerObserveAndRead 验证峰值保留与非法值处理。
func TestBWTrackerObserveAndRead(t *testing.T) {
	tr := newBWTracker()

	// 没有样本时返回 0（选路器会走 fallback，不会当成零带宽惩罚）
	if got := tr.Bandwidth("a"); got != 0 {
		t.Fatalf("无样本时应返回 0，实际 %.2f", got)
	}

	tr.observe("a", 10)
	if got := tr.Bandwidth("a"); got != 10 {
		t.Fatalf("期望 10，实际 %.2f", got)
	}

	// 更低的样本不应覆盖峰值（窗口内保留最好成绩）
	tr.observe("a", 5)
	if got := tr.Bandwidth("a"); got != 10 {
		t.Fatalf("低值不应覆盖峰值，期望 10，实际 %.2f", got)
	}

	// 更高的样本应更新
	tr.observe("a", 20)
	if got := tr.Bandwidth("a"); got != 20 {
		t.Fatalf("期望 20，实际 %.2f", got)
	}

	// 非法值忽略
	tr.observe("b", 0)
	tr.observe("", 100)
	if got := tr.Bandwidth("b"); got != 0 {
		t.Fatalf("0 样本应被忽略，实际 %.2f", got)
	}
}

// TestBWTrackerExpiry 验证超过窗口的峰值会过期。
//
// 过期很重要：链路降速后若不失效，选路器会一直高估这条出口的带宽，
// 继续把大流量往一条已经变慢的链路上压。
func TestBWTrackerExpiry(t *testing.T) {
	tr := newBWTracker()
	tr.observe("a", 50)

	tr.mu.Lock()
	tr.stats["a"].bestAt = time.Now().Add(-bwWindow - time.Second)
	tr.mu.Unlock()

	if got := tr.Bandwidth("a"); got != 0 {
		t.Fatalf("过期样本应返回 0，实际 %.2f", got)
	}
}

// TestBWTrackerPrune 验证断开 agent 的统计会被清理。
func TestBWTrackerPrune(t *testing.T) {
	tr := newBWTracker()
	tr.observe("a", 10)
	tr.observe("b", 20)
	tr.prune(map[string]bool{"a": true})
	if got := tr.Bandwidth("a"); got != 10 {
		t.Errorf("存活 agent 的样本不应被清理，实际 %.2f", got)
	}
	if got := tr.Bandwidth("b"); got != 0 {
		t.Errorf("已断开 agent 的样本应被清理，实际 %.2f", got)
	}
}

// TestCountedConnSettlesThroughput 验证够长够大的中继连接能结算出吞吐。
func TestCountedConnSettlesThroughput(t *testing.T) {
	tr := newBWTracker()
	a, b := net.Pipe()
	defer b.Close()

	c := wrapCounted(a, "node-x", tr)

	const total = bwMinBytes + 8192
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		read := 0
		for read < total {
			n, err := b.Read(buf)
			if err != nil {
				return
			}
			read += n
		}
	}()

	payload := make([]byte, 8192)
	written := 0
	start := time.Now()
	for written < total {
		chunk := payload
		if remain := total - written; remain < len(chunk) {
			chunk = chunk[:remain]
		}
		n, err := c.Write(chunk)
		if err != nil {
			t.Fatalf("写失败: %v", err)
		}
		written += n
	}
	// 补足最短时长（否则会被时长阈值挡掉）
	if elapsed := time.Since(start); elapsed < bwMinDuration {
		time.Sleep(bwMinDuration - elapsed)
	}
	_ = c.Close()
	<-done

	if got := tr.Bandwidth("node-x"); got <= 0 {
		t.Fatalf("应有带宽样本，实际 %.2f", got)
	}
}

// TestCountedConnIgnoresShortConn 验证过短/过小的连接不产生样本。
func TestCountedConnIgnoresShortConn(t *testing.T) {
	tr := newBWTracker()
	a, b := net.Pipe()
	defer b.Close()

	c := wrapCounted(a, "node-y", tr)
	go func() { _, _ = b.Write([]byte("hi")) }()
	buf := make([]byte, 16)
	_, _ = c.Read(buf)
	_ = c.Close() // 只传了几个字节、几乎零时长

	if got := tr.Bandwidth("node-y"); got != 0 {
		t.Fatalf("短连接不应产生带宽样本，实际 %.2f", got)
	}
}

// TestWrapCountedNilSafety 验证 nil 输入不会 panic（防御性）。
func TestWrapCountedNilSafety(t *testing.T) {
	tr := newBWTracker()
	if got := wrapCounted(nil, "x", tr); got != nil {
		t.Fatal("nil conn 应原样返回 nil")
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if got := wrapCounted(a, "x", nil); got != a {
		t.Fatal("nil tracker 时应原样返回 conn")
	}
}
