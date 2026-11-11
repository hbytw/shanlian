// 用户态 NAT：网关侧负责在隧道两端做地址与端口改写。
//
// # 为什么不用内核 conntrack
//
// 早期实现依赖内核的 SNAT + 自动 DNAT：
// 出方向 `-o slx0 -j SNAT --to-source <内层IP>`，回程由 conntrack 自动还原。
// 但在真实环境里回程包会在 netfilter 的 raw/mangle 之后就消失，
// 既不进 nat、也不进 INPUT/FORWARD（两台不同发行版的 VPS 与 WSL 上均可复现）。
// 手工构造同样字节的包却能正常转发，说明问题出在运行时规则状态与内核 NAT 的交互上，
// 难以在不深入内核调试的前提下稳定规避。
//
// 于是改为**完全自己管**：出方向记表并改写源地址/端口，
// 回程按表还原目的地址/端口 —— 写进 TUN 时目的地址已经是客户端的真实地址，
// 内核只需做一次普通转发，不涉及任何 NAT，也不依赖 conntrack。
//
// # 代价与限制
//
//   - 每包要改 IP 头与 L4 校验和；用 RFC 1624 增量更新，O(1) 而非重算整包。
//   - 只处理 IPv4。
//   - 非首片分片看不到端口，直接丢弃并计数（MSS 钳制后 TCP 不会分片）。
//   - ICMP 只处理 echo request/reply；ICMP 差错报文里嵌了原始头，暂不支持。
package forward

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	protoICMP = 1
	protoTCP  = 6
	protoUDP  = 17

	icmpEchoRequest = 8
	icmpEchoReply   = 0
)

// NAT 参数。
const (
	// natPortMin/Max 是分配源端口的范围。
	// 避开知名端口，也避开 Linux 默认临时端口段（32768-60999）。
	natPortMin = 20000
	natPortMax = 32000

	// natIdleTimeout 是表项空闲回收时间。
	natIdleTimeout = 300 * time.Second

	// natMaxEntries 是表项上限，防止异常流量打爆低端路由器的内存。
	natMaxEntries = 16384
)

// flowKey 唯一标识一条出方向流。
type flowKey struct {
	proto      uint8
	clientIP   [4]byte
	clientPort uint16
	dstIP      [4]byte
	dstPort    uint16
}

// natEntry 是一条 NAT 映射。
type natEntry struct {
	key      flowKey
	ourPort  uint16
	lastSeen time.Time
}

// NAT 是网关侧的用户态地址转换表。
//
// 并发：数据面两个方向（TUN 读、隧道收）各一个 goroutine，会同时访问。
// 用普通互斥锁即可 —— 每次操作只有一次 map 查找，
// 相比每包一次的校验和改写，锁开销可以忽略。
type NAT struct {
	mu       sync.Mutex
	outbound map[flowKey]*natEntry
	inbound  map[uint32]*natEntry // 键 = proto<<16 | ourPort
	nextPort uint16
	innerIP  [4]byte

	overflow   uint64
	fragments  uint64
	badPackets uint64
	misses     uint64
}

// NewNAT 创建 NAT 表。innerIP 为本节点的隧道内层地址，形如 "10.254.a.b"。
func NewNAT(innerIP string) (*NAT, error) {
	ip := net.ParseIP(innerIP).To4()
	if ip == nil {
		return nil, fmt.Errorf("内层地址 %q 不是合法的 IPv4", innerIP)
	}
	n := &NAT{
		outbound: make(map[flowKey]*natEntry, 256),
		inbound:  make(map[uint32]*natEntry, 256),
		nextPort: natPortMin,
	}
	copy(n.innerIP[:], ip)
	return n, nil
}

// Counters 返回统计快照。
func (n *NAT) Counters() (overflow, fragments, bad, misses uint64, entries int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.overflow, n.fragments, n.badPackets, n.misses, len(n.outbound)
}

// MapOutbound 把「客户端发往境外」的包改写成「本节点内层地址发往境外」。
//
// 返回 false 表示这个包无法处理，调用方应丢弃。原地修改，不分配内存。
func (n *NAT) MapOutbound(pkt []byte, now time.Time) bool {
	h, ok := parseIPv4(pkt)
	if !ok {
		n.mu.Lock()
		n.badPackets++
		n.mu.Unlock()
		return false
	}
	if h.fragOffset != 0 {
		n.mu.Lock()
		n.fragments++
		n.mu.Unlock()
		return false
	}

	key := flowKey{proto: h.proto}
	key.clientIP = h.src
	key.dstIP = h.dst

	switch h.proto {
	case protoTCP, protoUDP:
		if len(pkt) < h.ihl+4 {
			n.badPacket()
			return false
		}
		key.clientPort = binary.BigEndian.Uint16(pkt[h.ihl:])
		key.dstPort = binary.BigEndian.Uint16(pkt[h.ihl+2:])
	case protoICMP:
		if len(pkt) < h.ihl+8 || pkt[h.ihl] != icmpEchoRequest {
			n.badPacket()
			return false
		}
		key.clientPort = binary.BigEndian.Uint16(pkt[h.ihl+4:])
	default:
		n.badPacket()
		return false
	}

	n.mu.Lock()
	e, exists := n.outbound[key]
	if !exists {
		if len(n.outbound) >= natMaxEntries {
			n.overflow++
			n.mu.Unlock()
			return false
		}
		port, gotPort := n.allocPortLocked()
		if !gotPort {
			n.overflow++
			n.mu.Unlock()
			return false
		}
		e = &natEntry{key: key, ourPort: port, lastSeen: now}
		n.outbound[key] = e
		n.inbound[uint32(h.proto)<<16|uint32(port)] = e
	}
	e.lastSeen = now
	ourPort := e.ourPort
	n.mu.Unlock()

	rewriteSrc(pkt, h, key.clientPort, ourPort, h.src, n.innerIP)
	return true
}

// MapInbound 把「境外发往内层地址」的回程包还原成「境外发往客户端」。
func (n *NAT) MapInbound(pkt []byte, now time.Time) bool {
	h, ok := parseIPv4(pkt)
	if !ok {
		n.badPacket()
		return false
	}
	if h.fragOffset != 0 {
		n.mu.Lock()
		n.fragments++
		n.mu.Unlock()
		return false
	}

	var ourPort uint16
	switch h.proto {
	case protoTCP, protoUDP:
		if len(pkt) < h.ihl+4 {
			n.badPacket()
			return false
		}
		ourPort = binary.BigEndian.Uint16(pkt[h.ihl+2:])
	case protoICMP:
		if len(pkt) < h.ihl+8 || pkt[h.ihl] != icmpEchoReply {
			n.badPacket()
			return false
		}
		ourPort = binary.BigEndian.Uint16(pkt[h.ihl+4:])
	default:
		n.badPacket()
		return false
	}

	n.mu.Lock()
	e, exists := n.inbound[uint32(h.proto)<<16|uint32(ourPort)]
	if exists {
		e.lastSeen = now
	}
	n.mu.Unlock()
	if !exists {
		n.mu.Lock()
		n.misses++
		n.mu.Unlock()
		return false
	}

	rewriteDst(pkt, h, ourPort, e.key.clientPort, n.innerIP, e.key.clientIP)
	return true
}

// GC 回收空闲超时的表项，返回回收数量。
func (n *NAT) GC(now time.Time) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	removed := 0
	for k, e := range n.outbound {
		if now.Sub(e.lastSeen) > natIdleTimeout {
			delete(n.outbound, k)
			delete(n.inbound, uint32(e.key.proto)<<16|uint32(e.ourPort))
			removed++
		}
	}
	return removed
}

// —— 内部 ——

func (n *NAT) badPacket() {
	n.mu.Lock()
	n.badPackets++
	n.mu.Unlock()
}

// allocPortLocked 分配一个各协议都未被占用的源端口。调用方必须已持锁。
func (n *NAT) allocPortLocked() (uint16, bool) {
	span := natPortMax - natPortMin
	for i := 0; i <= span; i++ {
		p := n.nextPort
		n.nextPort++
		if n.nextPort > natPortMax {
			n.nextPort = natPortMin
		}
		// 三个协议都不占用才可用：否则回程按 (proto,port) 可能撞上另一条流
		conflict := false
		for _, proto := range [...]uint8{protoTCP, protoUDP, protoICMP} {
			if _, used := n.inbound[uint32(proto)<<16|uint32(p)]; used {
				conflict = true
				break
			}
		}
		if !conflict {
			return p, true
		}
	}
	return 0, false
}

// rewriteSrc 改写源地址与源端口（出方向）。
func rewriteSrc(pkt []byte, h ipv4Header, oldPort, newPort uint16, oldIP, newIP [4]byte) {
	copy(pkt[12:16], newIP[:])
	switch h.proto {
	case protoTCP, protoUDP:
		binary.BigEndian.PutUint16(pkt[h.ihl:], newPort)
	case protoICMP:
		binary.BigEndian.PutUint16(pkt[h.ihl+4:], newPort)
	}
	// IP 头校验和：源地址变了
	cur := binary.BigEndian.Uint16(pkt[10:12])
	binary.BigEndian.PutUint16(pkt[10:12],
		csumAdjust(csumAdjust(cur,
			binary.BigEndian.Uint16(oldIP[0:2]), binary.BigEndian.Uint16(newIP[0:2])),
			binary.BigEndian.Uint16(oldIP[2:4]), binary.BigEndian.Uint16(newIP[2:4])))
	fixL4Checksum(pkt, h, oldPort, newPort, oldIP, newIP)
}

// rewriteDst 改写目的地址与目的端口（回程）。
func rewriteDst(pkt []byte, h ipv4Header, oldPort, newPort uint16, oldIP, newIP [4]byte) {
	copy(pkt[16:20], newIP[:])
	switch h.proto {
	case protoTCP, protoUDP:
		binary.BigEndian.PutUint16(pkt[h.ihl+2:], newPort)
	case protoICMP:
		binary.BigEndian.PutUint16(pkt[h.ihl+4:], newPort)
	}
	cur := binary.BigEndian.Uint16(pkt[10:12])
	binary.BigEndian.PutUint16(pkt[10:12],
		csumAdjust(csumAdjust(cur,
			binary.BigEndian.Uint16(oldIP[0:2]), binary.BigEndian.Uint16(newIP[0:2])),
			binary.BigEndian.Uint16(oldIP[2:4]), binary.BigEndian.Uint16(newIP[2:4])))
	fixL4Checksum(pkt, h, oldPort, newPort, oldIP, newIP)
}

// fixL4Checksum 修正 TCP/UDP/ICMP 的校验和。
//
// L4 校验和覆盖「伪首部（含 IP 地址）+ L4 头与负载」，
// 所以地址或端口任一变化都要修。用增量更新，O(1)。
func fixL4Checksum(pkt []byte, h ipv4Header, oldPort, newPort uint16, oldIP, newIP [4]byte) {
	var lo int
	switch h.proto {
	case protoTCP:
		lo = h.ihl + 16
	case protoUDP:
		lo = h.ihl + 6
		// UDP 校验和为 0 表示"未计算"（IPv4 允许），此时无需修
		if lo+2 > len(pkt) || binary.BigEndian.Uint16(pkt[lo:lo+2]) == 0 {
			return
		}
	case protoICMP:
		lo = h.ihl + 2
	default:
		return
	}
	if lo+2 > len(pkt) {
		return
	}

	cs := binary.BigEndian.Uint16(pkt[lo : lo+2])
	if h.proto == protoICMP {
		// ICMP 的校验和只覆盖 ICMP 报文本身，**不含伪首部**，
		// 所以 IP 地址变化不影响它，只需按 id 的改动修正。
		if oldPort != newPort {
			cs = csumAdjust(cs, oldPort, newPort)
		}
		binary.BigEndian.PutUint16(pkt[lo:lo+2], cs)
		return
	}
	cs = csumAdjust(cs, binary.BigEndian.Uint16(oldIP[0:2]), binary.BigEndian.Uint16(newIP[0:2]))
	cs = csumAdjust(cs, binary.BigEndian.Uint16(oldIP[2:4]), binary.BigEndian.Uint16(newIP[2:4]))
	if oldPort != newPort {
		cs = csumAdjust(cs, oldPort, newPort)
	}
	binary.BigEndian.PutUint16(pkt[lo:lo+2], cs)
}

// ipv4Header 是解析出来的 IPv4 头关键字段。
type ipv4Header struct {
	ihl        int
	proto      uint8
	src        [4]byte
	dst        [4]byte
	fragOffset uint16
}

// parseIPv4 解析 IPv4 头，失败返回 false。
func parseIPv4(pkt []byte) (ipv4Header, bool) {
	var h ipv4Header
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return h, false
	}
	h.ihl = int(pkt[0]&0x0f) * 4
	if h.ihl < 20 || len(pkt) < h.ihl {
		return h, false
	}
	h.proto = pkt[9]
	copy(h.src[:], pkt[12:16])
	copy(h.dst[:], pkt[16:20])
	h.fragOffset = binary.BigEndian.Uint16(pkt[6:8]) & 0x1fff
	return h, true
}

// csumAdjust 用 RFC 1624 的增量更新修正 16 位反码校验和：
//
//	HC' = ~(~HC + ~m + m')
func csumAdjust(hc, old, new uint16) uint16 {
	sum := uint32(^hc&0xffff) + uint32(^old&0xffff) + uint32(new&0xffff)
	sum = (sum & 0xffff) + (sum >> 16)
	sum = (sum & 0xffff) + (sum >> 16)
	return ^uint16(sum)
}
