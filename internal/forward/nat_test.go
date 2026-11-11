package forward

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// —— 测试用构造与校验工具 ——

// buildIPv4 造一个 IPv4 包。payload 是 L4 内容（含 L4 头）。
func buildIPv4(proto uint8, src, dst string, payload []byte) []byte {
	total := 20 + len(payload)
	pkt := make([]byte, total)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:], uint16(total))
	pkt[8] = 64
	pkt[9] = proto
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	copy(pkt[20:], payload)
	// IP 头校验和
	binary.BigEndian.PutUint16(pkt[10:], 0)
	binary.BigEndian.PutUint16(pkt[10:], csum(pkt[:20]))
	return pkt
}

// buildTCP 造 TCP 段（校验和先按伪首部算好）。
func buildTCP(srcIP, dstIP string, sport, dport uint16, payload []byte) []byte {
	seg := make([]byte, 20+len(payload))
	binary.BigEndian.PutUint16(seg[0:], sport)
	binary.BigEndian.PutUint16(seg[2:], dport)
	binary.BigEndian.PutUint16(seg[12:], 5<<12) // 数据偏移 5
	copy(seg[20:], payload)
	binary.BigEndian.PutUint16(seg[16:], 0)
	binary.BigEndian.PutUint16(seg[16:], l4sum(srcIP, dstIP, protoTCP, seg))
	return seg
}

// buildUDP 造 UDP 数据报。
func buildUDP(srcIP, dstIP string, sport, dport uint16, payload []byte) []byte {
	dg := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(dg[0:], sport)
	binary.BigEndian.PutUint16(dg[2:], dport)
	binary.BigEndian.PutUint16(dg[4:], uint16(len(dg)))
	copy(dg[8:], payload)
	binary.BigEndian.PutUint16(dg[6:], 0)
	binary.BigEndian.PutUint16(dg[6:], l4sum(srcIP, dstIP, protoUDP, dg))
	return dg
}

// buildICMP 造 ICMP echo 报文。
func buildICMP(srcIP, dstIP string, typ uint8, id, seq uint16, payload []byte) []byte {
	msg := make([]byte, 8+len(payload))
	msg[0] = typ
	binary.BigEndian.PutUint16(msg[4:], id)
	binary.BigEndian.PutUint16(msg[6:], seq)
	copy(msg[8:], payload)
	binary.BigEndian.PutUint16(msg[2:], 0)
	binary.BigEndian.PutUint16(msg[2:], csum(msg))
	return msg
}

// csum 计算 16 位反码和。
func csum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = (s & 0xffff) + (s >> 16)
	}
	return ^uint16(s)
}

// l4sum 按伪首部计算 L4 校验和。
func l4sum(srcIP, dstIP string, proto uint8, seg []byte) uint16 {
	ph := make([]byte, 12)
	copy(ph[0:4], net.ParseIP(srcIP).To4())
	copy(ph[4:8], net.ParseIP(dstIP).To4())
	ph[9] = proto
	binary.BigEndian.PutUint16(ph[10:], uint16(len(seg)))
	return csum(append(ph, seg...))
}

// verifyIPChecksum 校验 IP 头校验和。
func verifyIPChecksum(t *testing.T, pkt []byte) {
	t.Helper()
	h := make([]byte, 20)
	copy(h, pkt[:20])
	binary.BigEndian.PutUint16(h[10:], 0)
	if got, want := binary.BigEndian.Uint16(pkt[10:12]), csum(h); got != want {
		t.Fatalf("IP 头校验和错误: got %#04x want %#04x", got, want)
	}
}

// verifyL4Checksum 校验 L4 校验和（用改后的地址）。
func verifyL4Checksum(t *testing.T, pkt []byte) {
	t.Helper()
	h, ok := parseIPv4(pkt)
	if !ok {
		t.Fatal("包解析失败")
	}
	if h.proto == protoICMP {
		if got := csum(pkt[h.ihl:]); got != 0 {
			t.Fatalf("ICMP 校验和错误: 余数 %#04x", got)
		}
		return
	}
	src := net.IP(h.src[:]).String()
	dst := net.IP(h.dst[:]).String()
	seg := make([]byte, len(pkt)-h.ihl)
	copy(seg, pkt[h.ihl:])
	// 把校验和字段清零后再算
	var off int
	if h.proto == protoTCP {
		off = 16
	} else {
		off = 6
	}
	if len(seg) < off+2 {
		t.Fatal("L4 太短")
	}
	if binary.BigEndian.Uint16(seg[off:]) == 0 && h.proto == protoUDP {
		return // UDP 未计算
	}
	if got, want := binary.BigEndian.Uint16(seg[off:]), l4sum(src, dst, h.proto, zeroAt(seg, off)); got != want {
		t.Fatalf("L4 校验和错误: got %#04x want %#04x", got, want)
	}
}

func zeroAt(b []byte, off int) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	binary.BigEndian.PutUint16(c[off:], 0)
	return c
}

// —— 测试 ——

const (
	testInner  = "10.254.7.9"
	testClient = "10.99.0.2"
	testRemote = "203.0.113.9"
)

func newTestNAT(t *testing.T) *NAT {
	t.Helper()
	n, err := NewNAT(testInner)
	if err != nil {
		t.Fatalf("NewNAT: %v", err)
	}
	return n
}

func TestNAT_TCBRoundTrip(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()

	// 客户端 → 境外
	out := buildIPv4(protoTCP, testClient, testRemote,
		buildTCP(testClient, testRemote, 54321, 443, []byte("hello-world-payload")))
	orig := append([]byte(nil), out...)

	if !n.MapOutbound(out, now) {
		t.Fatal("出方向改写失败")
	}
	if got := net.IP(out[12:16]).String(); got != testInner {
		t.Fatalf("源地址应改为 %s，实际 %s", testInner, got)
	}
	if got := binary.BigEndian.Uint16(out[20:22]); got < natPortMin || got > natPortMax {
		t.Fatalf("分配的源端口 %d 不在范围内", got)
	}
	if got := binary.BigEndian.Uint16(out[22:24]); got != 443 {
		t.Fatalf("目的端口被改坏了: %d", got)
	}
	verifyIPChecksum(t, out)
	verifyL4Checksum(t, out)
	t.Logf("出方向: %s:%d → %s:%d",
		net.IP(out[12:16]), binary.BigEndian.Uint16(out[20:22]),
		net.IP(out[16:20]), binary.BigEndian.Uint16(out[22:24]))

	// 境外 → 内层地址（回程）
	in := buildIPv4(protoTCP, testRemote, testInner,
		buildTCP(testRemote, testInner, 443, binary.BigEndian.Uint16(out[20:22]), []byte("response!")))
	if !n.MapInbound(in, now) {
		t.Fatal("回程改写失败")
	}
	if got := net.IP(in[16:20]).String(); got != testClient {
		t.Fatalf("目的地址应还原为 %s，实际 %s", testClient, got)
	}
	if got := binary.BigEndian.Uint16(in[22:24]); got != 54321 {
		t.Fatalf("目的端口应还原为 54321，实际 %d", got)
	}
	verifyIPChecksum(t, in)
	verifyL4Checksum(t, in)

	_ = orig
}

func TestNAT_UDPRoundTrip(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()

	out := buildIPv4(protoUDP, testClient, testRemote,
		buildUDP(testClient, testRemote, 5555, 53, []byte("dns-query")))
	if !n.MapOutbound(out, now) {
		t.Fatal("UDP 出方向改写失败")
	}
	verifyIPChecksum(t, out)
	verifyL4Checksum(t, out)
	ourPort := binary.BigEndian.Uint16(out[20:22])

	in := buildIPv4(protoUDP, testRemote, testInner,
		buildUDP(testRemote, testInner, 53, ourPort, []byte("dns-answer")))
	if !n.MapInbound(in, now) {
		t.Fatal("UDP 回程改写失败")
	}
	if got := binary.BigEndian.Uint16(in[22:24]); got != 5555 {
		t.Fatalf("UDP 目的端口应还原为 5555，实际 %d", got)
	}
	verifyIPChecksum(t, in)
	verifyL4Checksum(t, in)
}

func TestNAT_ICMPRoundTrip(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()

	out := buildIPv4(protoICMP, testClient, testRemote,
		buildICMP(testClient, testRemote, icmpEchoRequest, 0x1234, 1, []byte("ping-data")))
	if !n.MapOutbound(out, now) {
		t.Fatal("ICMP 出方向改写失败")
	}
	ourID := binary.BigEndian.Uint16(out[24:26])
	if ourID == 0x1234 {
		t.Log("提示: ICMP id 恰好未变（端口分配巧合），不影响正确性")
	}
	verifyIPChecksum(t, out)
	verifyL4Checksum(t, out)

	in := buildIPv4(protoICMP, testRemote, testInner,
		buildICMP(testRemote, testInner, icmpEchoReply, ourID, 1, []byte("ping-data")))
	if !n.MapInbound(in, now) {
		t.Fatal("ICMP 回程改写失败")
	}
	if got := binary.BigEndian.Uint16(in[24:26]); got != 0x1234 {
		t.Fatalf("ICMP id 应还原为 0x1234，实际 %#04x", got)
	}
	if got := net.IP(in[16:20]).String(); got != testClient {
		t.Fatalf("目的地址应还原，实际 %s", got)
	}
	verifyIPChecksum(t, in)
	verifyL4Checksum(t, in)
}

func TestNAT_SameFlowSamePort(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()

	var first uint16
	for i := 0; i < 5; i++ {
		pkt := buildIPv4(protoTCP, testClient, testRemote,
			buildTCP(testClient, testRemote, 1234, 80, []byte("x")))
		if !n.MapOutbound(pkt, now) {
			t.Fatal("改写失败")
		}
		p := binary.BigEndian.Uint16(pkt[20:22])
		if i == 0 {
			first = p
		} else if p != first {
			t.Fatalf("同一条流应复用同一端口，第 %d 次得到 %d，首次 %d", i, p, first)
		}
	}
}

func TestNAT_DifferentFlowsDifferentPorts(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()
	seen := map[uint16]bool{}
	for i := 0; i < 50; i++ {
		pkt := buildIPv4(protoTCP, testClient, testRemote,
			buildTCP(testClient, testRemote, uint16(1000+i), 80, nil))
		if !n.MapOutbound(pkt, now) {
			t.Fatalf("第 %d 条流改写失败", i)
		}
		p := binary.BigEndian.Uint16(pkt[20:22])
		if seen[p] {
			t.Fatalf("端口 %d 被重复分配", p)
		}
		seen[p] = true
	}
	if len(seen) != 50 {
		t.Fatalf("应分配 50 个不同端口，实际 %d", len(seen))
	}
}

func TestNAT_GC(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()

	pkt := buildIPv4(protoTCP, testClient, testRemote,
		buildTCP(testClient, testRemote, 999, 80, nil))
	if !n.MapOutbound(pkt, now) {
		t.Fatal("改写失败")
	}
	if _, _, _, _, entries := n.Counters(); entries != 1 {
		t.Fatalf("应有 1 条表项，实际 %d", entries)
	}

	// 未超时：不回收
	if got := n.GC(now.Add(time.Minute)); got != 0 {
		t.Fatalf("不应回收，却回收了 %d 条", got)
	}
	// 超时：回收
	if got := n.GC(now.Add(natIdleTimeout + time.Second)); got != 1 {
		t.Fatalf("应回收 1 条，实际 %d", got)
	}
	if _, _, _, _, entries := n.Counters(); entries != 0 {
		t.Fatalf("回收后应为 0 条，实际 %d", entries)
	}
}

func TestNAT_RejectsGarbage(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()

	if n.MapOutbound([]byte{0x60}, now) {
		t.Error("IPv6 应被拒绝")
	}
	if n.MapOutbound(make([]byte, 10), now) {
		t.Error("过短的包应被拒绝")
	}
	// 非首片分片
	frag := buildIPv4(protoTCP, testClient, testRemote,
		buildTCP(testClient, testRemote, 1, 2, nil))
	binary.BigEndian.PutUint16(frag[6:], 0x0010) // MF + offset 0 → 实际 offset=1
	if n.MapOutbound(frag, now) {
		t.Error("分片应被拒绝")
	}
	// 未知协议
	unknown := buildIPv4(47, testClient, testRemote, []byte("gre"))
	if n.MapOutbound(unknown, now) {
		t.Error("未知协议应被拒绝")
	}
	// 回程找不到表项
	in := buildIPv4(protoTCP, testRemote, testInner,
		buildTCP(testRemote, testInner, 80, 29999, nil))
	if n.MapInbound(in, now) {
		t.Error("无表项的回程包应被拒绝")
	}
}

func TestNAT_OversizeDoesNotPanic(t *testing.T) {
	n := newTestNAT(t)
	now := time.Now()
	// TCP 头被截断
	bad := buildIPv4(protoTCP, testClient, testRemote, []byte{0x00, 0x01})
	if n.MapOutbound(bad, now) {
		t.Error("截断 TCP 应被拒绝")
	}
	// ICMP 头被截断
	bad2 := buildIPv4(protoICMP, testClient, testRemote, []byte{8, 0})
	if n.MapOutbound(bad2, now) {
		t.Error("截断 ICMP 应被拒绝")
	}
}

// BenchmarkNAT_SteadyFlow 测同一条流的稳态转发开销（真实场景的绝大多数包）。
func BenchmarkNAT_SteadyFlow(b *testing.B) {
	n, _ := NewNAT(testInner)
	now := time.Now()
	src := buildIPv4(protoTCP, testClient, testRemote,
		buildTCP(testClient, testRemote, 1234, 443, make([]byte, 1200)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pkt := make([]byte, len(src))
		copy(pkt, src) // 每次用干净的包，模拟真实收包
		n.MapOutbound(pkt, now)
	}
}

// BenchmarkNAT_InboundReply 测回程还原的开销。
func BenchmarkNAT_InboundReply(b *testing.B) {
	n, _ := NewNAT(testInner)
	now := time.Now()
	out := buildIPv4(protoTCP, testClient, testRemote,
		buildTCP(testClient, testRemote, 1234, 443, nil))
	n.MapOutbound(out, now)
	ourPort := binary.BigEndian.Uint16(out[20:22])
	src := buildIPv4(protoTCP, testRemote, testInner,
		buildTCP(testRemote, testInner, 443, ourPort, make([]byte, 1200)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pkt := make([]byte, len(src))
		copy(pkt, src)
		n.MapInbound(pkt, now)
	}
}

// BenchmarkNAT_NewFlow 测新建流的开销（含端口分配）。
func BenchmarkNAT_NewFlow(b *testing.B) {
	n, _ := NewNAT(testInner)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pkt := buildIPv4(protoTCP, testClient, testRemote,
			buildTCP(testClient, testRemote, uint16(1000+i%30000), 443, nil))
		n.MapOutbound(pkt, now)
	}
}
