package relay

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// hostPingTimeout 是单次主机可达性探测的默认超时。
const hostPingTimeout = 3 * time.Second

// tcpProbePorts 是 ICMP 不可用时的兜底探测端口。
//
// 443 在前：绝大多数想上网的站点都开 443；80 作为次选。
// 只用于「判断从出口能不能到达」，不参与数据面。
var tcpProbePorts = []string{"443", "80"}

var (
	icmpCapOnce sync.Once
	icmpCapOK   bool
)

// pingCacheTTL 是主机可达性结果的缓存时长。
//
// 为什么必须缓存：非特权 ICMP 不可用时会退化成 TCP 连接探测（要建三次握手，
// 跨境可能几百毫秒）。`ping -c 4` 一秒发一个，每次都重新探测会来不及回包，
// 表现为「通了但有丢包」。缓存 5 秒后连续 ping 只探测一次，其余直接复用，
// 结果稳定且不丢包。
const pingCacheTTL = 5 * time.Second

// pingCacheMax 限制缓存条目数，避免被大量目标撑爆内存。
const pingCacheMax = 512

type pingEntry struct {
	alive bool
	at    time.Time
}

var (
	pingCacheMu sync.Mutex
	pingCache   = map[string]pingEntry{}
)

// ICMPAvailable 报告本进程能否发送 ICMP echo。
//
// 结果缓存：只探测一次，避免每次 ping 都试一遍 socket。
func ICMPAvailable() bool {
	icmpCapOnce.Do(func() {
		for _, netw := range []string{"udp4", "ip4:icmp"} {
			c, err := icmp.ListenPacket(netw, "0.0.0.0")
			if err == nil {
				_ = c.Close()
				icmpCapOK = true
				return
			}
		}
	})
	return icmpCapOK
}

// pingHost 对目标做一次可达性探测，返回是否可达。
//
// 三级策略：
//  1. 非特权 ICMP（udp4 / ping socket）—— 需要内核 net.ipv4.ping_group_range 覆盖当前 gid；
//  2. 特权 ICMP（raw socket）—— 需要 root 或 CAP_NET_RAW；
//  3. TCP 连接探测（443/80）—— 前两者都不可用时的兜底。
//
// 为什么要有第 3 级：实测多数发行版默认 ping_group_range 是 "1 0"（即禁用），
// 而 agent 的卖点就是不依赖 root。没有兜底的话 proxy 模式下 ping 全超时，
// 用户会误判成整个网络挂了。
// 兜底的代价是「ICMP 被墙但 TCP 通」的主机会显示可达 —— 这对「能不能上网」
// 这个真正的问题来说反而是更准确的答案。
func pingHost(addr net.IP, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = hostPingTimeout
	}
	if addr == nil || addr.To4() == nil {
		return false // 暂只支持 IPv4
	}

	key := addr.String()
	if alive, ok := cacheGet(key); ok {
		return alive
	}

	alive := false
	if ICMPAvailable() {
		for _, network := range []string{"udp4", "ip4:icmp"} {
			if ok, err := pingOnce(network, addr, timeout); err == nil {
				alive = ok
				cachePut(key, alive)
				return alive
			}
		}
	}
	alive = tcpProbe(addr, timeout)
	cachePut(key, alive)
	return alive
}

// cacheGet 取缓存结果。
func cacheGet(key string) (bool, bool) {
	pingCacheMu.Lock()
	defer pingCacheMu.Unlock()
	e, ok := pingCache[key]
	if !ok || time.Since(e.at) > pingCacheTTL {
		return false, false
	}
	return e.alive, true
}

// cachePut 写缓存，并在条目过多时做一次整体清理。
func cachePut(key string, alive bool) {
	pingCacheMu.Lock()
	defer pingCacheMu.Unlock()
	if len(pingCache) >= pingCacheMax {
		now := time.Now()
		for k, v := range pingCache {
			if now.Sub(v.at) > pingCacheTTL {
				delete(pingCache, k)
			}
		}
		if len(pingCache) >= pingCacheMax {
			pingCache = map[string]pingEntry{} // 仍然满：整体重来
		}
	}
	pingCache[key] = pingEntry{alive: alive, at: time.Now()}
}

// tcpProbe 用 TCP 连接探测可达性（兜底）。
func tcpProbe(addr net.IP, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for _, port := range tcpProbePorts {
		remain := time.Until(deadline)
		if remain <= 0 {
			return false
		}
		d := net.Dialer{Timeout: remain}
		conn, err := d.Dial("tcp", net.JoinHostPort(addr.String(), port))
		if err == nil {
			_ = conn.Close()
			return true
		}
		// 连接被拒说明主机活着但端口没开 —— 对「可达性」而言仍算可达
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Timeout() {
			continue
		}
		if isConnRefused(err) {
			return true
		}
	}
	return false
}

// isConnRefused 判断错误是否为「连接被拒」。
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"connection refused", "actively refused"} {
		if containsFold(msg, s) {
			return true
		}
	}
	return false
}

// pingOnce 在指定网络类型上发一次 echo 并等回应。
func pingOnce(network string, addr net.IP, timeout time.Duration) (bool, error) {
	c, err := icmp.ListenPacket(network, "0.0.0.0")
	if err != nil {
		return false, err
	}
	defer func() { _ = c.Close() }()

	// 用随机 ID 避开同机其它 ping 程序的混淆
	var idBuf [2]byte
	if _, rerr := rand.Read(idBuf[:]); rerr != nil {
		return false, rerr
	}
	id := int(binary.BigEndian.Uint16(idBuf[:]))
	seq := 1

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: []byte("slx-ping"),
		},
	}
	pkt, err := msg.Marshal(nil)
	if err != nil {
		return false, err
	}

	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return false, err
	}
	if network == "udp4" {
		if _, werr := c.WriteTo(pkt, &net.UDPAddr{IP: addr}); werr != nil {
			return false, werr
		}
	} else {
		if _, werr := c.WriteTo(pkt, &net.IPAddr{IP: addr}); werr != nil {
			return false, werr
		}
	}

	buf := make([]byte, 1500)
	for {
		n, peer, rerr := c.ReadFrom(buf)
		if rerr != nil {
			return false, rerr // 超时也算 error，交给调用方判断
		}
		if !sameHost(peer, addr) {
			continue
		}
		reply, perr := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
		if perr != nil {
			continue
		}
		if reply.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok || echo.Seq != seq {
			continue
		}
		return true, nil
	}
}

// sameHost 判断回包是否来自目标（udp4 下是 UDPAddr，raw 下是 IPAddr）。
func sameHost(peer net.Addr, want net.IP) bool {
	switch a := peer.(type) {
	case *net.UDPAddr:
		return a.IP.Equal(want)
	case *net.IPAddr:
		return a.IP.Equal(want)
	}
	return false
}

// containsFold 是大小写无关的子串判断。
func containsFold(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	ls, lsub := toLowerASCII(s), toLowerASCII(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

// toLowerASCII 只处理 ASCII，足够应付错误串匹配。
func toLowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
