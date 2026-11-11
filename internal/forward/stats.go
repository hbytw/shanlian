// Package forward 实现数据面：网关把 TUN 里的包送进隧道，出口把隧道里的包送进内核做 NAT。
//
// 数据流（双向）：
//
//	网关: PC 包 → 内核策略路由 → TUN → 加帧 → UDP(ZT) → 出口
//	出口: 解帧 → 写入 TUN → 内核转发 + MASQUERADE → 互联网
//	回程: 互联网 → 出口内核反 NAT → TUN → 加帧 → UDP(ZT) → 网关 → TUN → 内核反 NAT → PC
//
// 两端各做一次 SNAT（网关改内层地址，出口改公网地址），
// 这是 WireGuard 出口节点的标准做法，好处是 NAT 状态全交给内核 conntrack 维护，
// 我们不需要自己实现 NAT 表。
package forward

import (
	"net"
	"sync/atomic"
)

// Stats 是数据面计数器。
//
// 设计取舍：不用 atomic 直接累加每个包。
// 在 MIPS32/ARM32 上 64 位原子操作会退化成全局锁，每包一把锁会直接把吞吐打死。
// 改为「每方向各持本地计数，每秒发布一次快照」，读侧用原子读，竞争降低到 1 次/秒。
type Stats struct {
	// 以下字段由各自方向发布，读取方用 atomic 读
	TunRead, TunWrite, TunBytesRead, TunBytesWrite atomic.Uint64
	Sent, SendErr                                  atomic.Uint64
	NoRoute, Oversize, BadPacket                   atomic.Uint64
}

// Snapshot 是某一时刻的计数快照。
type Snapshot struct {
	TunRead       uint64
	TunWrite      uint64
	TunBytesRead  uint64
	TunBytesWrite uint64
	Sent          uint64
	SendErr       uint64
	NoRoute       uint64
	Oversize      uint64
	BadPacket     uint64
}

// Snapshot 读取当前计数。
func (s *Stats) Snapshot() Snapshot {
	return Snapshot{
		TunRead:       s.TunRead.Load(),
		TunWrite:      s.TunWrite.Load(),
		TunBytesRead:  s.TunBytesRead.Load(),
		Sent:          s.Sent.Load(),
		SendErr:       s.SendErr.Load(),
		NoRoute:       s.NoRoute.Load(),
		Oversize:      s.Oversize.Load(),
		BadPacket:     s.BadPacket.Load(),
		TunBytesWrite: s.TunBytesWrite.Load(),
	}
}

// localCounter 是单 goroutine 内的本地计数，定期发布给 Stats。
type localCounter struct {
	packets uint64
	bytes   uint64
	errs    uint64
}

// publish 把本地计数写入原子变量（每秒调用一次即可）。
func (c *localCounter) publish(p, b *atomic.Uint64) {
	if c.packets != 0 {
		p.Add(c.packets)
	}
	if c.bytes != 0 {
		b.Add(c.bytes)
	}
	c.packets, c.bytes = 0, 0
}

// ipv4Info 从 IPv4 包里取出源/目的地址。
//
// 直接读固定偏移而不是引入 IP 解析库：热路径上每个包都要调，
// 而 IPv4 头格式是固定且稳定的（源地址偏移 12，目的地址偏移 16）。
func ipv4Info(pkt []byte) (src, dst net.IP, ok bool) {
	if len(pkt) < 20 {
		return nil, nil, false
	}
	if pkt[0]>>4 != 4 {
		return nil, nil, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl {
		return nil, nil, false
	}
	return net.IP(pkt[12:16]), net.IP(pkt[16:20]), true
}

// isIPv6 判断是否为 IPv6 包（M2 暂不支持，但要知道收到了什么）。
func isIPv6(pkt []byte) bool {
	return len(pkt) > 0 && pkt[0]>>4 == 6
}
