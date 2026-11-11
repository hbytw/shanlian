// Package dnsfwd 是一个极简的本地 DNS 转发器。
//
// # 它解决两个问题
//
//  1. **CDN 位置**：境外站点如果在家里解析，拿到的可能是很远节点的 IP；
//     经出口解析才能拿到贴着那个出口的 CDN 地址 —— 这直接决定视频卡不卡。
//  2. **域名↔出口亲和**：某个域名经哪个出口解析，后续就应该优先走那个出口，
//     否则会出现「解析到东京的 IP、却从法兰克福出去」的绕路。
//
// # 为什么自己实现而不用现成库
//
//   - 只需要 A 记录转发，不需要递归、DNSSEC、EDNS 等完整能力；
//   - 单文件二进制要尽量小，不想为一个简功能引入完整 DNS 库；
//   - 关键是**决策逻辑**（本地还是经出口），这块本来就得自己写。
//
// # 限制（都记录在案）
//
//   - 只返回 A 记录。AAAA 查询回空应答，让客户端自己退回 IPv4。
//   - 只支持 UDP。UDP 应答超过 512 字节时截断并置 TC 位，客户端会改用 TCP —— 但那会失败。
//     实际中一次返回几条 A 记录远达不到 512 字节。
//   - 不做 DNSSEC 校验（局域网内网关上做校验意义不大）。
package dnsfwd

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 常量
const (
	// maxUDP 是传统 DNS over UDP 的报文上限。
	maxUDP = 512
	// maxInFlightDNS 是同时处理的 DNS 查询上限。
	//
	// 超了怎么办：立刻回 SERVFAIL（快速失败），而不是排队。
	// 排队的后果是客户端 20 秒超时 —— 那比快速失败糟得多。
	maxInFlightDNS = 512
	// maxNameLen 是域名最大长度。
	maxNameLen = 255
	// defaultTTL 是本转发器给出应答的 TTL（秒）。
	//
	// 取 60 而不是常见的 300：走出口解析的结果带有「CDN 位置」属性，
	// 出口可能切换，TTL 太长会让客户端一直用旧出口的地址。
	defaultTTL = 60

	// cacheTTL 是解析结果在**转发器内部**的缓存时长（秒）。
	//
	// 为什么要有内部缓存：出口解析要走隧道（agent RTT 几十~几百毫秒
	// + 出口侧 DNS 往返），客户端首次访问一个新域名要等 1 秒左右，
	// 而同一个域名往往被反复访问（页面里的几十个资源）。
	//
	// 取 30 秒而不是更长的理由与 defaultTTL 一样：出口可能切换，
	// 缓存越久越容易让客户端拿着旧出口的地址。
	// 取 30 秒而不是更短：短于此值的收益不大，而命中率会明显掉。
	cacheTTL = 30 * time.Second

	// cacheMaxEntries 是缓存的最大条目数。
	//
	// 上限是为了防止被随机域名撑爆内存（DNS 放大攻击的老套路）。
	// 满了按 LRU 淘汰最久没用的那条。
	cacheMaxEntries = 1024
)

// 记录类型
const (
	typeA     = 1
	typeAAAA  = 28
	typeCNAME = 5
)

// Resolver 是「把域名解析成 IPv4 列表」的能力。
//
// 抽象出来是为了测试：生产代码注入真实的系统解析器和经出口的解析器，
// 测试里注入固定结果的假实现。
type Resolver func(domain string) ([]net.IP, error)

// Policy 决定一个域名该怎么解析。
type Policy interface {
	// ShouldResolveViaEgress 报告这个域名是否应该经出口解析。
	ShouldResolveViaEgress(domain string) bool
	// PickEgress 为域名挑一个出口；返回空表示没有可用出口。
	PickEgress(domain string) string
	// Observe 在每次解析完成后回调。
	// egressID 为空表示走的是本地解析；非空表示经该出口解析。
	//
	// 用途：策略层据此学习「这个域名是境内还是境外」，
	// 并在经出口解析时建立域名↔出口亲和。
	Observe(domain, egressID string, ips []net.IP)
}

// Server 是 DNS 转发器。
type Server struct {
	mu   sync.RWMutex
	addr string
	ln   net.PacketConn

	local    Resolver            // 本机解析
	byEgress map[string]Resolver // 已缓存的出口解析器（键 = node_id）
	// egressFn 按需构造出口解析器（首次用到某出口时才建，避免启动时依赖对端在线）
	egressFn func(string) Resolver

	policy Policy

	// cache 是解析结果的短 TTL 缓存（见 cache.go）。
	cache *resultCache

	// inFlight 是当前正在处理的查询数（并发上限保护）。
	inFlight atomic.Int64

	// 统计
	queries    uint64
	localHits  uint64
	egressHits uint64
	failures   uint64
	cacheHits  uint64
	cacheMiss  uint64

	stopped chan struct{}
}

// Options 构造参数。
type Options struct {
	// Addr 是监听地址，如 "127.0.0.1:5353"。默认只监听回环。
	Addr string
	// Local 是本机解析器（必填）。
	Local Resolver
	// ByEgress 返回某个出口的解析器（可为 nil，表示不支持经出口解析）。
	ByEgress func(nodeID string) Resolver
	// Policy 是解析策略（可为 nil，表示全部走本地）。
	Policy Policy
	// CacheTTL 是解析结果在转发器内部的缓存时长（0 表示用默认 30 秒）。
	CacheTTL time.Duration
	// CacheMax 是缓存条目数上限（0 表示用默认 1024）。
	CacheMax int
}

// New 创建转发器（不启动）。
func New(o Options) (*Server, error) {
	if o.Local == nil {
		return nil, errors.New("DNS 转发器需要本机解析器")
	}
	addr := o.Addr
	if addr == "" {
		addr = "127.0.0.1:5335"
	}
	return &Server{
		addr:     addr,
		local:    o.Local,
		byEgress: map[string]Resolver{},
		egressFn: o.ByEgress,
		policy:   o.Policy,
		cache:    newResultCache(o.CacheTTL, o.CacheMax),
		stopped:  make(chan struct{}),
	}, nil
}

// Start 绑定端口并开始服务。
//
// 绑定失败会返回错误 —— 常见原因是 53 端口已被 dnsmasq/systemd-resolved 占用。
// 我们默认用 5353 就是为了避开它们。
func (s *Server) Start() error {
	ln, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return fmt.Errorf("DNS 监听 %s 失败: %w", s.addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go s.serve(ln)
	return nil
}

// Addr 返回实际监听地址。
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ln == nil {
		return s.addr
	}
	return s.ln.LocalAddr().String()
}

// Close 停止服务。
func (s *Server) Close() {
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	select {
	case <-s.stopped:
	default:
		close(s.stopped)
	}
}

// Stats 返回计数快照。
func (s *Server) Stats() (queries, localHits, egressHits, failures uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queries, s.localHits, s.egressHits, s.failures
}

// CacheStats 返回缓存的命中/未命中计数。
//
// 口径：queries = 命中 + 未命中；未命中 = 本地 + 经出口 + 失败。
func (s *Server) CacheStats() (hits, misses uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cacheHits, s.cacheMiss
}

// CacheLen 返回当前缓存条目数（诊断用）。
func (s *Server) CacheLen() int { return s.cache.len() }

func (s *Server) serve(ln net.PacketConn) {
	buf := make([]byte, 1500)
	for {
		n, peer, err := ln.ReadFrom(buf)
		if err != nil {
			select {
			case <-s.stopped:
				return
			default:
			}
			// 其它读错误继续（UDP 无连接，单个错误不影响后续）
			continue
		}
		if n < 12 {
			continue
		}
		s.mu.Lock()
		s.queries++
		s.mu.Unlock()

		// 每个查询**独立处理**：一个慢查询绝不能拖死整个 DNS 服务。
		//
		// 现场事故（2026-11-11 P2）：出口解析卡住（网关侧死锁），
		// 而这里原来是**串行**处理 —— 第一个查询卡住，
		// 后面的查询全部排队等着，客户端 20 秒超时 = 全屋断网。
		//
		// 串行处理的前提是「每个查询都有上界」，但出口解析那条路
		// 一旦出问题就可能没有上界 —— 所以这里改成并发 + 有界。
		req := append([]byte(nil), buf[:n]...)
		if s.inFlight.Add(1) > maxInFlightDNS {
			s.inFlight.Add(-1)
			// 并发已满：立刻回 SERVFAIL（快速失败），
			// 让客户端早点重试，而不是干等到超时。
			s.replyFailure(ln, req, peer)
			continue
		}
		go func(req []byte, peer net.Addr) {
			defer s.inFlight.Add(-1)
			resp := s.handle(req)
			if len(resp) > 0 {
				_, _ = ln.WriteTo(resp, peer)
			}
		}(req, peer)
	}
}

// replyFailure 快速回一个 SERVFAIL。
func (s *Server) replyFailure(ln net.PacketConn, req []byte, peer net.Addr) {
	q, err := parseQuery(req)
	if err != nil {
		return
	}
	resp := buildResponse(q, nil, rcodeServerFailure, uint32(defaultTTL))
	if len(resp) > 0 {
		_, _ = ln.WriteTo(resp, peer)
	}
}

// handle 处理一个 DNS 查询，返回应答报文（可能为空表示不应答）。
func (s *Server) handle(req []byte) []byte {
	q, err := parseQuery(req)
	if err != nil {
		return nil
	}

	var ips []net.IP
	viaEgress := ""
	// ttl 是回给客户端的 TTL（秒）。
	//
	// **硬约束：TTL 不得超过本条结果在缓存里的剩余寿命** ——
	// 缓存 30 秒却告诉客户端 60 秒的话，客户端会在缓存失效后
	// 继续用那个地址（可能已经指向别的出口了）。
	ttl := uint32(defaultTTL)

	if q.qtype == typeA {
		// 缓存键统一小写：DNS 名字是大小写无关的，
		// 客户端一会儿 "WWW.Example.COM" 一会儿 "www.example.com" 很常见，
		// 不归一化就会白白多占缓存条目、少命中。
		cacheKey := strings.ToLower(q.name)

		// —— 快路径：命中缓存就完全不碰出口解析 ——
		//
		// 这是缓存存在的全部意义：出口解析要走隧道（agent RTT + 出口侧 DNS 往返），
		// 首次访问一个新域名要等 1 秒左右。命中时是纯内存操作，微秒级返回。
		if got, eg, ok, remaining := s.cache.get(cacheKey); ok {
			s.mu.Lock()
			s.cacheHits++
			s.mu.Unlock()

			ips = got
			viaEgress = eg
			ttl = ttlFor(remaining)

			if s.policy != nil {
				// 命中也要 Observe：否则「域名 ↔ 出口亲和」的学习就断了。
				s.policy.Observe(q.name, viaEgress, ips)
			}
			return buildResponse(q, ips, rcodeSuccess, ttl)
		}

		s.mu.Lock()
		s.cacheMiss++
		s.mu.Unlock()

		useEgress := false
		if s.policy != nil {
			useEgress = s.policy.ShouldResolveViaEgress(q.name)
		}
		if useEgress {
			eg := s.policy.PickEgress(q.name)
			if eg != "" {
				if r := s.egressResolver(eg); r != nil {
					if got, rerr := r(q.name); rerr == nil && len(got) > 0 {
						ips = got
						viaEgress = eg
					}
				}
			}
		}
		if len(ips) == 0 {
			got, lerr := s.local(q.name)
			if lerr != nil {
				s.mu.Lock()
				s.failures++
				s.mu.Unlock()
				// 失败**不进缓存**：一次抖动不该把域名钉死 30 秒。
				return buildResponse(q, nil, rcodeServerFailure, ttl)
			}
			ips = got
		}

		// 解析成功才进缓存。
		s.cache.put(cacheKey, ips, viaEgress)
		// 刚解析出来的结果，缓存寿命就是完整的 cacheTTL。
		ttl = ttlFor(s.cache.ttl)
	}
	// AAAA 查询返回空应答（NOERROR + 0 answers），让客户端退回 IPv4

	if s.policy != nil {
		s.policy.Observe(q.name, viaEgress, ips)
	}
	if viaEgress != "" {
		s.mu.Lock()
		s.egressHits++
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		s.localHits++
		s.mu.Unlock()
	}

	return buildResponse(q, ips, rcodeSuccess, ttl)
}

// ttlFor 把「缓存剩余寿命」换算成回给客户端的 TTL（秒）。
//
// 取 min(defaultTTL, 剩余寿命)：
//   - 剩余寿命比 defaultTTL 短时必须用它，否则客户端会拿着
//     缓存过期后的地址去连（出口可能已经切走了）；
//   - 剩余寿命更长时用 defaultTTL，客户端缓存得久一点也无妨。
//
// 向上取整：宁可多给 1 秒，也不要给 0 秒（0 会让客户端立刻重查，
// 缓存就白做了）。
func ttlFor(remaining time.Duration) uint32 {
	if remaining <= 0 {
		return 1
	}
	secs := uint32((remaining + time.Second - 1) / time.Second)
	if secs > defaultTTL {
		return defaultTTL
	}
	return secs
}

func (s *Server) egressResolver(nodeID string) Resolver {
	s.mu.RLock()
	r := s.byEgress[nodeID]
	fn := s.egressFn
	s.mu.RUnlock()
	if r != nil {
		return r
	}
	if fn == nil {
		return nil
	}
	// 按需构造并缓存
	r = fn(nodeID)
	if r != nil {
		s.mu.Lock()
		s.byEgress[nodeID] = r
		s.mu.Unlock()
	}
	return r
}

// SetEgressResolver 注册或更新某个出口的解析器。
func (s *Server) SetEgressResolver(nodeID string, r Resolver) {
	s.mu.Lock()
	s.byEgress[nodeID] = r
	s.mu.Unlock()
}

// —— DNS 报文处理 ——

const (
	rcodeSuccess       = 0
	rcodeFormatError   = 1
	rcodeServerFailure = 2
	rcodeNameError     = 3
)

type query struct {
	id     uint16
	name   string
	qtype  uint16
	qclass uint16
	// question 是原始问题段（含末尾 0x00），应答里要原样带回
	question []byte
	// rd 是客户端请求的递归标志，应答里要回显
	rd bool
}

// parseQuery 解析查询报文。
func parseQuery(b []byte) (query, error) {
	var q query
	if len(b) < 12 {
		return q, errors.New("报文太短")
	}
	q.id = binary.BigEndian.Uint16(b[0:2])
	flags := binary.BigEndian.Uint16(b[2:4])
	q.rd = flags&0x0100 != 0
	qdcount := binary.BigEndian.Uint16(b[4:6])
	if qdcount < 1 {
		return q, errors.New("没有查询段")
	}

	// 解析域名
	name, off, err := parseName(b, 12)
	if err != nil {
		return q, err
	}
	if len(name) > maxNameLen {
		return q, errors.New("域名过长")
	}
	if off+4 > len(b) {
		return q, errors.New("报文截断")
	}
	q.name = name
	q.qtype = binary.BigEndian.Uint16(b[off : off+2])
	q.qclass = binary.BigEndian.Uint16(b[off+2 : off+4])
	q.question = append([]byte(nil), b[12:off+4]...)
	return q, nil
}

// parseName 解析（可能带压缩指针的）域名。
func parseName(b []byte, off int) (string, int, error) {
	var labels []string
	start := off
	for {
		if off >= len(b) {
			return "", 0, errors.New("域名越界")
		}
		l := int(b[off])
		if l == 0 {
			off++
			break
		}
		if l&0xC0 == 0xC0 {
			// 压缩指针：查询段里极少见，但为稳妥处理
			if off+1 >= len(b) {
				return "", 0, errors.New("指针越界")
			}
			break
		}
		off++
		if off+l > len(b) {
			return "", 0, errors.New("标签越界")
		}
		labels = append(labels, string(b[off:off+l]))
		off += l
		if off-start > maxNameLen {
			return "", 0, errors.New("域名过长")
		}
	}
	return strings.Join(labels, "."), off, nil
}

// buildResponse 构造应答报文。
//
// 结构：原样带回问题段，然后追加 A 记录。A 记录的 name 用压缩指针 0xC00C
// 指回问题段的域名 —— 这是 DNS 最标准的做法，也是我们只需要 A 记录的前提。
func buildResponse(q query, ips []net.IP, rcode int, ttl uint32) []byte {
	// 过滤出 IPv4
	var v4 []net.IP
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			v4 = append(v4, ip4)
		}
	}
	if len(v4) > 16 {
		v4 = v4[:16] // 防止应答过大
	}

	out := make([]byte, 12, 12+len(q.question)+len(v4)*16+16)
	binary.BigEndian.PutUint16(out[0:2], q.id)
	// QR=1（应答），opcode=0，AA=0，TC=0，RD 回显，RA=1
	flags := uint16(0x8000)
	if q.rd {
		flags |= 0x0100
	}
	flags |= 0x0080 // RA
	flags |= uint16(rcode) & 0x000F
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[4:6], 1)               // QDCOUNT
	binary.BigEndian.PutUint16(out[6:8], uint16(len(v4))) // ANCOUNT
	binary.BigEndian.PutUint16(out[8:10], 0)              // NSCOUNT
	binary.BigEndian.PutUint16(out[10:12], 0)             // ARCOUNT

	out = append(out, q.question...)

	for _, ip := range v4 {
		rr := make([]byte, 16)
		binary.BigEndian.PutUint16(rr[0:2], 0xC00C) // 指向问题段的域名
		binary.BigEndian.PutUint16(rr[2:4], typeA)
		binary.BigEndian.PutUint16(rr[4:6], 1) // IN
		binary.BigEndian.PutUint32(rr[6:10], ttl)
		binary.BigEndian.PutUint16(rr[10:12], 4)
		copy(rr[12:16], ip)
		out = append(out, rr...)
	}

	// UDP 上限保护：超长则截断到 512 字节并把 TC 置位，
	// 让客户端改用 TCP 重试（虽然我们不支持 TCP，但至少行为符合协议）。
	if len(out) > maxUDP {
		out = out[:maxUDP]
		binary.BigEndian.PutUint16(out[2:4], flags|0x0200)
	}
	return out
}

// 回环上游的兜底：当系统 DNS 配置里只有回环地址时使用。
// 选国内可达、且对境外域名解析不劫持的两家。
var fallbackUpstreams = []string{"223.5.5.5", "119.29.29.29", "1.1.1.1"}

// systemUpstreams 读 /etc/resolv.conf 并滤掉回环地址。
//
// 为什么必须滤掉回环：OpenWrt 上 /etc/resolv.conf 就是 nameserver 127.0.0.1，
// 也就是 dnsmasq 自己。若同时把 dnsmasq 的上游指向本转发器，就形成
//
//	dnsmasq:53 → 本转发器:5353 → 127.0.0.1:53 → dnsmasq:53 → ...
//
// 的死循环。表现为「监听正常但永远不回应查询」，全屋 DNS 直接瘫痪。
// 真机踩到过：启用 dns_listen 并把 dnsmasq 上游指向它之后，国内国外全部解析失败。
func systemUpstreams() []string {
	return systemUpstreamsFrom("/etc/resolv.conf")
}

// systemUpstreamsFrom 是 systemUpstreams 的可测版本：路径可注入。
func systemUpstreamsFrom(path string) []string {
	var out []string
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "nameserver") {
				continue
			}
			fs := strings.Fields(line)
			if len(fs) < 2 {
				continue
			}
			ip := net.ParseIP(fs[1])
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue // 跳过 127.0.0.1 / ::1 等回环地址
			}
			out = append(out, ip.String())
		}
	}
	if len(out) == 0 {
		return fallbackUpstreams
	}
	return out
}

// SystemResolver 返回一个使用系统 DNS 的解析器，带超时。
//
// 显式指定上游并用 PreferGo，是为了绕开 net.DefaultResolver 直接读
// /etc/resolv.conf 的行为 —— 那样在 OpenWrt 上必然指回 dnsmasq 自己。
func SystemResolver(timeout time.Duration) Resolver {
	upstreams := systemUpstreams()

	var mu sync.Mutex
	next := 0

	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			srv := upstreams[next%len(upstreams)]
			next++
			mu.Unlock()
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "udp", net.JoinHostPort(srv, "53"))
		},
	}

	return func(domain string) ([]net.IP, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		addrs, err := r.LookupIPAddr(ctx, domain)
		if err != nil {
			return nil, err
		}
		var ips []net.IP
		for _, a := range addrs {
			if a.IP.To4() != nil {
				ips = append(ips, a.IP)
			}
		}
		return ips, nil
	}
}
