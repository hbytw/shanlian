package cli

import (
	"sync"
	"testing"
)

// asyncSink 假装协议栈：它**保留收到的切片引用**，稍后才真正去读。
//
// 这正是 gvisor 的行为：
//   - TCP：forwarder 回调在 goroutine 里跑（go f.handler(...)）；
//   - UDP：端点把包收进接收队列，之后才由中继协程读它的载荷。
//
// 所以它能复现「TUN 读循环复用了同一个 buf」造成的数据竞争。
type asyncSink struct {
	mu  sync.Mutex
	got [][]byte
}

func (s *asyncSink) PushPacket(pkt []byte) bool {
	s.mu.Lock()
	s.got = append(s.got, pkt)
	s.mu.Unlock()
	return true
}

func (s *asyncSink) first() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) == 0 {
		return nil
	}
	return s.got[0]
}

// TestPushTUNPacketCopiesBuffer 验证 **交给协议栈的包与 TUN 读缓冲解耦**。
//
// 回归：审计报告「根因 J / P2-5」—— TUN 读循环复用同一个 buf，
// 而协议栈异步处理包 ⇒ 协议栈可能读到已经被下一个包覆盖的字节
// ⇒ 数据竞争（-race 会报）+ 线上偶发包错乱。
//
// 修法：PushPacket 之前拷贝一份（netstack.PushPacket 里的
// buffer.MakeWithData 不拷贝，只是引用）。
func TestPushTUNPacketCopiesBuffer(t *testing.T) {
	sink := &asyncSink{}

	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = byte(i)
	}

	if !pushTUNPacket(sink, buf, len(buf)) {
		t.Fatal("协议栈应当接收这个包")
	}

	// 模拟下一次 Read 把源缓冲覆盖掉。
	for i := range buf {
		buf[i] = 0xff
	}

	got := sink.first()
	if got == nil {
		t.Fatal("协议栈没有收到包")
	}
	if len(got) != len(buf) {
		t.Fatalf("包长度 = %d，期望 %d", len(got), len(buf))
	}
	for i, b := range got {
		if b != byte(i) {
			t.Fatalf("第 %d 字节 = %d，期望 %d：协议栈拿到的是被覆盖的读缓冲（数据竞争）", i, b, byte(i))
		}
	}
}

// TestCopyTunPacketKeepsContent 验证拷贝保留原始字节。
func TestCopyTunPacketKeepsContent(t *testing.T) {
	src := []byte{0x45, 0x00, 0x00, 0x14}
	got := copyTunPacket(src)
	if len(got) != len(src) {
		t.Fatalf("长度 = %d，期望 %d", len(got), len(src))
	}
	for i := range src {
		if got[i] != src[i] {
			t.Fatalf("第 %d 字节 = %d，期望 %d", i, got[i], src[i])
		}
	}
	// 改源不影响拷贝。
	src[0] = 0xff
	if got[0] != 0x45 {
		t.Fatal("拷贝出来的包必须与源缓冲解耦")
	}
}
