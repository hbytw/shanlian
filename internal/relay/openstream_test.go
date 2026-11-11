package relay

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// deadSession 造一个「对端存在但永远不读数据」的会话。
//
// 用途：模拟「对端不确认 SYN」的情形 —— yamux 的 OpenStream 会一直卡在
// 等 SYN-ACK 上（拿 synCh 槽位时没有超时）。
func deadSession(t *testing.T) *yamux.Session {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	sess, err := yamux.Client(client, yamuxConfig())
	if err != nil {
		t.Fatalf("建 yamux 客户端会话失败: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// TestOpenStreamWithTimeout_CallerNotBlocked 确认超时后调用方立刻返回。
//
// 这是审计报告「根因 C」的第一条：开流超时不能把请求方一起拖住
// （DNS 路径上尤其致命：2 秒的客户端预算会被 5 秒的开流吃掉）。
func TestOpenStreamWithTimeout_CallerNotBlocked(t *testing.T) {
	sess := deadSession(t)

	start := time.Now()
	st, err := openStreamWithTimeout(sess, 300*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		_ = st.Close()
		t.Fatal("对端不确认时应当超时失败")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("调用方被拖住 %s，应当立刻返回", elapsed)
	}
}

// TestOpenStreamWithTimeout_RejectsWhenAtCap 确认在途开流超上限时**快速失败**。
//
// 排队是错的：超时的根因是会话本身坏了，排队只会把超时往后推，
// 而且会把 yamux 的 synCh（256 槽）吃满 ⇒ 之后所有开流永久阻塞
// ⇒ 整台网关的数据面死掉。
func TestOpenStreamWithTimeout_RejectsWhenAtCap(t *testing.T) {
	// 把信号量填满。
	for i := 0; i < cap(openStreamSem); i++ {
		openStreamSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(openStreamSem); i++ {
			<-openStreamSem
		}
	}()

	sess := deadSession(t)
	start := time.Now()
	_, err := openStreamWithTimeout(sess, 5*time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("在途开流已达上限时应当失败")
	}
	if !strings.Contains(err.Error(), "在途开流已达上限") {
		t.Fatalf("错误信息应说明是上限拒绝，实际 = %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("被上限拒绝时耗时 %s，必须快速失败（不能排队）", elapsed)
	}
}

// TestOpenStreamWithTimeout_CapIsNotExhausted 确认上限本身远小于
// yamux 的 synCh 容量 —— 这是「不会锁死」的前提。
func TestOpenStreamWithTimeout_CapIsNotExhausted(t *testing.T) {
	// yamux 默认 AcceptBacklog/synCh 容量是 256。
	const yamuxSynCapacity = 256
	if cap(openStreamSem) >= yamuxSynCapacity {
		t.Fatalf("在途开流上限 %d 不该达到 yamux 的 synCh 容量 %d", cap(openStreamSem), yamuxSynCapacity)
	}
}
