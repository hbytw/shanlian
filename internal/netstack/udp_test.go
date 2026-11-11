package netstack

import (
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// —— UDP 回归测试 ——
//
// 覆盖审计报告「根因 A」的两条连带问题：
//
//	B1：UDP 建连在 TUN 读协程里同步阻塞 ⇒ 整个数据面卡死；
//	B2：失败时不建端点 ⇒ demux 找不到匹配端点 ⇒ 每个包重新阻塞一次（按包放大）。

// udpTestConnector 是可控的 Connector 替身。
type udpTestConnector struct {
	mu       sync.Mutex
	udpCalls int
	tcpCalls int

	// udpBlock 非 nil 时 ConnectUDP 会一直等它关闭（模拟「出口迟迟不回 ACK」）。
	udpBlock chan struct{}
	// udpDelay 非零时 ConnectUDP 先睡这么久（模拟「出口慢」）。
	udpDelay time.Duration
	// udpErr 非 nil 时 ConnectUDP 直接失败。
	udpErr error
	// udpConnFactory 造成功时返回的连接（nil = 用一对内存管道）。
	udpConnFactory func() net.Conn
}

func (c *udpTestConnector) ConnectTCP(net.IP, uint16) (net.Conn, bool, error) {
	c.mu.Lock()
	c.tcpCalls++
	c.mu.Unlock()
	return nil, true, errors.New("netstack: 测试里不建 TCP")
}

func (c *udpTestConnector) ConnectUDP(net.IP, uint16) (net.Conn, bool, error) {
	c.mu.Lock()
	c.udpCalls++
	block := c.udpBlock
	delay := c.udpDelay
	err := c.udpErr
	factory := c.udpConnFactory
	c.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, false, err
	}
	if factory != nil {
		return factory(), false, nil
	}
	a, b := net.Pipe()
	go func() {
		// 另一端读到关闭为止，保持管道打开。
		buf := make([]byte, 512)
		for {
			if _, rerr := b.Read(buf); rerr != nil {
				return
			}
		}
	}()
	return a, false, nil
}

func (c *udpTestConnector) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.udpCalls
}

// ipv4Checksum 算 IPv4 头的校验和（字段本身要置 0 再算）。
func ipv4Checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// buildUDPIPv4Packet 造一个「客户端 → 境外目标」的 IPv4/UDP 包。
//
// 校验和说明：IPv4 头必须算对；UDP 校验和填 0 ——
// RFC 768 允许 IPv4 省略 UDP 校验和，gvisor 的 header.UDPValid 也接受
// （netProto == IPv4 且 checksum == 0 时直接判为合法）。
func buildUDPIPv4Packet(src, dst net.IP, srcPort, dstPort uint16, payload []byte) []byte {
	total := 20 + 8 + len(payload)
	b := make([]byte, total)
	b[0] = 0x45 // 版本 4 + 首部长度 5 个 32 位字
	b[1] = 0    // TOS
	binary.BigEndian.PutUint16(b[2:4], uint16(total))
	binary.BigEndian.PutUint16(b[4:6], 0) // ID
	binary.BigEndian.PutUint16(b[6:8], 0) // flags / 分片偏移
	b[8] = 64                             // TTL
	b[9] = 17                             // 协议号 = UDP
	copy(b[12:16], src.To4())
	copy(b[16:20], dst.To4())
	binary.BigEndian.PutUint16(b[10:12], ipv4Checksum(b[:20]))

	udpHdr := b[20:28]
	binary.BigEndian.PutUint16(udpHdr[0:2], srcPort)
	binary.BigEndian.PutUint16(udpHdr[2:4], dstPort)
	binary.BigEndian.PutUint16(udpHdr[4:6], uint16(8+len(payload)))
	// udpHdr[6:8] 保持 0：省略 UDP 校验和（IPv4 合法）。
	copy(b[28:], payload)
	return b
}

// TestUDPPushDoesNotBlockDataPlane 验证 **UDP 建连不会阻塞数据面**。
//
// 这是审计报告「根因 A」的回归测试。
//
// gvisor 的 UDP forwarder 是**同步回调**
// （pkg/tcpip/transport/udp/forwarder.go:47 直接调 f.handler(...)），
// 而 TCP forwarder 是 `go f.handler(...)`。
// ⇒ 旧代码里 ConnectUDP 直接跑在 TUN 读协程上，
// 出口慢 2 秒 ⇒ 整个数据面停 2 秒（所有 TCP 包也读不进来）。
//
// 修法：handler 只登记四元组 + 建端点，建连放 goroutine。
func TestUDPPushDoesNotBlockDataPlane(t *testing.T) {
	// 出口慢 2 秒（模拟「等 agent 回 yamux ACK」）。
	conn := &udpTestConnector{udpDelay: 2 * time.Second}
	proxy := newTestStack(t, conn)

	pkt := buildUDPIPv4Packet(
		net.IPv4(192, 168, 1, 100),
		net.IPv4(8, 8, 8, 8),
		5353, 53,
		[]byte("dns-query"),
	)

	start := time.Now()
	if !proxy.PushPacket(pkt) {
		t.Fatal("协议栈应当接收这个 UDP 包")
	}
	elapsed := time.Since(start)

	// 出口会阻塞 2 秒。旧代码下 PushPacket 要等它。
	if elapsed > 300*time.Millisecond {
		t.Fatalf("PushPacket 耗时 %s：UDP 建连仍然占着数据面（应立即返回）", elapsed)
	}
}

// TestUDPFailedFlowIsNotRetriedPerPacket 验证 **建连失败不会按包放大**。
//
// 审计报告「根因 A」连带问题 B2：
// 旧代码在失败时直接 return，**不建端点** ⇒ gvisor 的 demux 找不到匹配端点
// ⇒ 回落 defaultHandler ⇒ **同一个四元组的每个包都重新阻塞一次**。
//
// 修法：① 端点先建好（后续包直接进端点接收队列）；
// ② 失败后进入冷却，冷却期内同四元组的包直接丢弃。
func TestUDPFailedFlowIsNotRetriedPerPacket(t *testing.T) {
	conn := &udpTestConnector{udpErr: errors.New("出口连不上")}
	proxy := newTestStack(t, conn)

	pkt := buildUDPIPv4Packet(
		net.IPv4(192, 168, 1, 100),
		net.IPv4(8, 8, 8, 8),
		5353, 53,
		[]byte("dns-query"),
	)

	const packets = 5
	for i := 0; i < packets; i++ {
		if !proxy.PushPacket(pkt) {
			t.Fatal("协议栈应当接收这个 UDP 包")
		}
		// 给建连 goroutine一点时间把失败记下来。
		time.Sleep(20 * time.Millisecond)
	}

	// 旧代码：5 个包 ⇒ 5 次建连尝试（每次都阻塞）。
	// 新代码：只应该尝试 1 次。
	if got := conn.calls(); got != 1 {
		t.Fatalf("建连尝试了 %d 次，期望 1 次（失败后必须冷却，不能按包放大）", got)
	}

	st := proxy.Stats()
	if st.UDPFailed != 1 {
		t.Fatalf("UDP 失败计数 = %d，期望 1", st.UDPFailed)
	}
	if st.UDPDropped == 0 {
		t.Fatal("冷却期内被丢弃的包应该有计数（UDPDropped）")
	}
}

// TestUDPSameTupleCreatesSingleFlow 验证 **同一个四元组只建一条流**。
//
// 场景：客户端连续发好几个数据报（同一个四元组）。
// 期望：只有一条 UDP 流、一个出口连接；后续包进同一条流的端点队列。
func TestUDPSameTupleCreatesSingleFlow(t *testing.T) {
	conn := &udpTestConnector{}
	proxy := newTestStack(t, conn)

	pkt := buildUDPIPv4Packet(
		net.IPv4(192, 168, 1, 100),
		net.IPv4(8, 8, 8, 8),
		5353, 53,
		[]byte("dns-query"),
	)
	for i := 0; i < 4; i++ {
		if !proxy.PushPacket(pkt) {
			t.Fatal("协议栈应当接收这个 UDP 包")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := conn.calls(); got != 1 {
		t.Fatalf("建连尝试了 %d 次，期望 1 次（同四元组只建一条流）", got)
	}

	proxy.udpMu.Lock()
	n := len(proxy.udpFlows)
	proxy.udpMu.Unlock()
	if n != 1 {
		t.Fatalf("UDP 四元组表里有 %d 条记录，期望 1 条", n)
	}
}

// TestUDPDifferentTuplesCreateSeparateFlows 验证不同四元组互不干扰。
func TestUDPDifferentTuplesCreateSeparateFlows(t *testing.T) {
	conn := &udpTestConnector{}
	proxy := newTestStack(t, conn)

	for i := 0; i < 3; i++ {
		pkt := buildUDPIPv4Packet(
			net.IPv4(192, 168, 1, 100),
			net.IPv4(8, 8, 8, 8),
			uint16(5353+i), 53,
			[]byte("dns-query"),
		)
		if !proxy.PushPacket(pkt) {
			t.Fatal("协议栈应当接收这个 UDP 包")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := conn.calls(); got != 3 {
		t.Fatalf("建连尝试了 %d 次，期望 3 次（三个不同四元组）", got)
	}
}

// TestUDPFlowSurvivesStackClose 验证栈关闭时 UDP 中继 goroutine 能退出。
//
// 没有 goroutine 泄漏是硬要求：Close() 会等 s.wg，
// 如果中继 goroutine 还挂在 FramedPipe 的读上，退出就会卡死。
func TestUDPFlowSurvivesStackClose(t *testing.T) {
	conn := &udpTestConnector{}
	proxy := newTestStack(t, conn)

	pkt := buildUDPIPv4Packet(
		net.IPv4(192, 168, 1, 100),
		net.IPv4(8, 8, 8, 8),
		5353, 53,
		[]byte("dns-query"),
	)
	if !proxy.PushPacket(pkt) {
		t.Fatal("协议栈应当接收这个 UDP 包")
	}
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		proxy.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("协议栈关闭超时：UDP 中继 goroutine 泄漏了")
	}
}

// TestUDPIdleFlowIsReaped 验证 **空闲超时的 UDP 四元组会被回收**。
//
// 回归：审计报告「根因 B3」—— UDP 四元组永久占着 yamux 流与 NAT 映射。
// 现在靠「多久没有数据流动」来判断该关掉它。
func TestUDPIdleFlowIsReaped(t *testing.T) {
	conn := &udpTestConnector{}
	proxy := newTestStack(t, conn)

	pkt := buildUDPIPv4Packet(
		net.IPv4(192, 168, 1, 100),
		net.IPv4(8, 8, 8, 8),
		5353, 53,
		[]byte("dns-query"),
	)
	if !proxy.PushPacket(pkt) {
		t.Fatal("协议栈应当接收这个 UDP 包")
	}
	time.Sleep(50 * time.Millisecond)

	// 假装这条流已经很久没有数据流动。
	proxy.udpMu.Lock()
	for _, f := range proxy.udpFlows {
		f.lastActive.Store(time.Now().Add(-time.Hour).UnixNano())
	}
	proxy.udpMu.Unlock()

	proxy.reapIdleUDPFlows(time.Now())

	proxy.udpMu.Lock()
	n := len(proxy.udpFlows)
	proxy.udpMu.Unlock()
	if n != 0 {
		t.Fatalf("空闲超时后还剩 %d 条 UDP 四元组记录，期望 0 条", n)
	}
}

// TestUDPActiveFlowIsNotReaped 验证 **还在传数据的 UDP 四元组不会被误回收**。
func TestUDPActiveFlowIsNotReaped(t *testing.T) {
	conn := &udpTestConnector{}
	proxy := newTestStack(t, conn)

	pkt := buildUDPIPv4Packet(
		net.IPv4(192, 168, 1, 100),
		net.IPv4(8, 8, 8, 8),
		5353, 53,
		[]byte("dns-query"),
	)
	if !proxy.PushPacket(pkt) {
		t.Fatal("协议栈应当接收这个 UDP 包")
	}
	time.Sleep(50 * time.Millisecond)

	proxy.reapIdleUDPFlows(time.Now())

	proxy.udpMu.Lock()
	n := len(proxy.udpFlows)
	proxy.udpMu.Unlock()
	if n != 1 {
		t.Fatalf("刚建好的 UDP 流被回收了（剩 %d 条，期望 1 条）", n)
	}
}
