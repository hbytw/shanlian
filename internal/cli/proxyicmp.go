package cli

import (
	"encoding/binary"
	"net"
	"time"
)

// ICMP echo 相关常量。
const (
	icmpProtoNum   = 1  // IPv4 协议号：ICMP
	icmpEchoReq    = 8  // echo request
	icmpEchoResp   = 0  // echo reply
	icmpHeaderLen  = 8  // type(1)+code(1)+checksum(2)+id(2)+seq(2)
	icmpMaxPending = 64 // 同时在途的 ping 上限（防止被 ping 洪水打爆）
)

// icmpEchoRequestTarget 判断一个 IP 包是否为 IPv4 ICMP echo 请求，
// 是则返回目标地址。
//
// 只接管 echo request：其它 ICMP（目的不可达、时间超时等）在 proxy 模式下
// 由 gvisor 自己产生，不需要外部参与。
func icmpEchoRequestTarget(pkt []byte) (net.IP, bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return nil, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+icmpHeaderLen {
		return nil, false
	}
	if pkt[9] != icmpProtoNum {
		return nil, false
	}
	if pkt[ihl] != icmpEchoReq || pkt[ihl+1] != 0 {
		return nil, false
	}
	dst := net.IP(pkt[16:20])
	return dst, true
}

// icmpEchoReply 用一个 ICMP echo 请求包合成对应的 echo 回复包。
//
// 交换源/目的地址，type 8→0，重算 ICMP 与 IP 头校验和。
// 注意 ICMP 校验和**只覆盖 ICMP 报文本身**（没有 TCP/UDP 那样的伪首部），
// 这是 M3 阶段踩过的坑，这里不能照抄 TCP 的算法。
func icmpEchoReply(req []byte) []byte {
	if len(req) < 20 || req[0]>>4 != 4 {
		return nil
	}
	ihl := int(req[0]&0x0f) * 4
	if ihl < 20 || len(req) < ihl+icmpHeaderLen {
		return nil
	}

	// 复制整个包（IP 头 + ICMP 报文），返回给客户端
	reply := make([]byte, len(req))
	copy(reply, req)

	// 交换源与目的地址
	copy(reply[12:16], req[16:20])
	copy(reply[16:20], req[12:16])

	// ICMP type 8 → 0（reply），code 保持 0
	reply[ihl] = icmpEchoResp
	reply[ihl+1] = 0

	// 重算 ICMP 校验和（先把校验和字段清零）
	reply[ihl+2] = 0
	reply[ihl+3] = 0
	icmpSum := checksumSum(reply[ihl:])
	binary.BigEndian.PutUint16(reply[ihl+2:], icmpSum)

	// 重算 IP 头校验和
	reply[10] = 0
	reply[11] = 0
	ipSum := checksumSum(reply[:ihl])
	binary.BigEndian.PutUint16(reply[10:], ipSum)

	return reply
}

// checksumSum 计算 RFC 1071 校验和。
func checksumSum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// tryICMPEcho 尝试接管一个 ICMP echo 请求。
//
// 返回值 true 表示「这个包已由我们处理」，调用方不应再送进 gvisor。
//
// 为什么必须异步：ping 要等 agent 往返（可能几百毫秒到 3 秒超时），
// 在 TUN 读循环里同步等会把整条数据面的包处理全卡住。
func (p *proxyPlane) tryICMPEcho(pkt []byte) bool {
	dst, ok := icmpEchoRequestTarget(pkt)
	if !ok {
		return false
	}

	// 过载保护：ping 洪水时直接丢弃，而不是无限开协程
	select {
	case p.icmpSem <- struct{}{}:
	default:
		p.icmpDropped.Add(1)
		return true
	}

	// 拷贝一份：pkt 的底层缓冲会被下一个 Read 复用
	reqCopy := make([]byte, len(pkt))
	copy(reqCopy, pkt)

	go func() {
		defer func() { <-p.icmpSem }()

		if !p.connector.PingHost(dst) {
			// 不可达就不回包，让客户端自己超时 —— 不能伪造「活着」
			return
		}
		reply := icmpEchoReply(reqCopy)
		if reply == nil {
			return
		}
		if _, err := p.dev.Write(reply); err != nil {
			p.tunWriteErr.Add(1)
			return
		}
		p.icmpReplied.Add(1)
	}()
	return true
}

// icmpPingTimeout 是网关等待 agent 回探测结果的上限。
//
// 略大于 agent 侧的单次 ping 超时（3s），留出流建立与网络抖动余量。
const icmpPingTimeout = 4 * time.Second
