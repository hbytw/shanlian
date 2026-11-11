package forward

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
)

// NodeInnerIP 从节点 ID 确定性派生隧道内层地址。
//
// 为什么每个节点都必须有唯一的内层地址：
// 出口靠「回程包的目的地址」判断该把包送回哪个网关。
// 如果所有网关共用同一个内层地址（早期设计想让网关都用 .1），
// 出口就无法区分它们；更糟的是，当目的地址等于出口自己的地址时，
// 内核会直接投递到本机，回程包根本读不到。
//
// 用 node_id 哈希派生：同一节点每次运行地址相同（重启不变），
// 不同节点之间靠哈希自然分散。
//
// CIDR 默认 /16 而不是 /24：/24 只有 253 个可用地址，
// 几个节点靠哈希撞车的概率就有十几个百分点；/16 有 6.5 万个地址，
// 十来个节点几乎不可能碰撞。万一真撞了，日志里会表现为
// 「找不到回程网关」，可按 node_id 手工排查。
//
// 第三段刻意取 1..254（避开 0 段）：0 段保留给未来可能的
// 「出口固定地址」用途，也避开常见的网关地址习惯。
func NodeInnerIP(cidr, nodeID string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("内层网段 %q 非法: %w", cidr, err)
	}
	base := ipnet.IP.To4()
	if base == nil {
		return "", fmt.Errorf("内层网段必须是 IPv4: %s", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	if bits-ones < 8 {
		return "", fmt.Errorf("内层网段太小（%s），至少需要 /24", cidr)
	}

	h := sha256.Sum256([]byte("slx inner-ip|" + nodeID))
	v := binary.BigEndian.Uint32(h[:4])

	third := byte(1 + v%254)        // 1..254，避开 0 段
	fourth := byte(1 + (v/254)%254) // 1..254，避开网络号与广播地址

	return net.IPv4(base[0], base[1], third, fourth).String(), nil
}

// PrefixFor 返回 CIDR 的掩码位数。
func PrefixFor(cidr string) (int, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, err
	}
	ones, _ := ipnet.Mask.Size()
	return ones, nil
}
