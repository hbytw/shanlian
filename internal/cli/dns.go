package cli

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"slx/internal/config"

	"slx/internal/dnsfwd"
	"slx/internal/logx"
	"slx/internal/mesh"
	"slx/internal/picker"
	"slx/internal/proto"

	"log/slog"
)

// DNS 转发器参数。
const (
	// dnsDefaultAddr 是默认监听地址。
	//
	// 用 5353 而不是 53：53 通常被 dnsmasq / systemd-resolved 占着，
	// 硬抢会破坏用户现有的解析。用户把上游指到这里即可。
	dnsDefaultAddr = "127.0.0.1:5335"

	// dnsResolveTimeout 是单次解析超时。
	dnsResolveTimeout = 5 * time.Second

	// dnsClientTimeout 是**面向客户端**的那条路径的超时预算。
	//
	// 为什么单独一套：客户端的 DNS 查询一般 5 秒就超时了。
	// 我们如果先等出口解析（5 秒）再回落本地（再 5 秒），
	// 客户端早就超时走人了 —— 表现是「DNS 失败 254 次」，
	// 客户端拿不到任何 IP，于是连国内站点也打不开（全屋断网）。
	//
	// 所以这里把出口尝试压到 2 秒：超时立刻回落本地解析，
	// 本地再最多 2 秒 —— 最坏 4 秒，仍在客户端的耐心之内。
	// 回落出来的地址可能是被污染的，但「拿到一个地址」远好过
	// 「一个地址都拿不到」。
	dnsClientTimeout = 2 * time.Second
)

// dnsPolicy 实现 dnsfwd.Policy。
//
// 核心判断：**这个域名该在本地解析，还是该经出口解析？**
//
// 判据来自实测而非硬编码：
//   - 第一次遇到某域名时先本地解析，看拿到的 IP 是否落在中国大陆网段；
//   - 若落在中国 → 记为「境内」，以后都本地解析（快，且拿到本地 CDN）；
//   - 若不落在 → 记为「境外」，以后经出口解析（拿到贴着出口的 CDN 地址），
//     同时建立「域名 ↔ 出口」亲和，让后续流量优先走同一个出口。
//
// 这样不需要维护任何域名白名单，也不需要用户配置。
type dnsPolicy struct {
	log *slog.Logger
	// candidates 返回当前可用出口的 node_id 列表。
	//
	// 由调用方注入：packet 模式来自 mesh 发现表，proxy 模式来自 agent 连接池。
	// 这样两种模式共用同一套「哪些域名该经出口解析 + 亲和」逻辑，
	// 不必为 proxy 再复制一份。
	candidates func() []string
	pick       *picker.Selector
	selfID     string

	// cnNets 是中国大陆网段，用于判断解析结果属于哪边。
	cnNets func() []*net.IPNet

	mu sync.RWMutex
	// foreign 缓存「已知是境外」的域名。
	foreign map[string]bool
	// local 记录已确认是国内域名的条目：这些用本地解析
	local map[string]bool
	// done 限制表的规模。
	maxEntries int
}

func newDNSPolicy(log *slog.Logger, pick *picker.Selector, selfID string,
	cnNets func() []*net.IPNet, candidates func() []string) *dnsPolicy {
	if log == nil {
		log = logx.Discard()
	}
	return &dnsPolicy{
		log: log, pick: pick,
		selfID: selfID, cnNets: cnNets, candidates: candidates,
		foreign:    make(map[string]bool, 512),
		local:      make(map[string]bool, 512),
		maxEntries: 8192,
	}
}

// ShouldResolveViaEgress 报告域名是否应经出口解析。
//
// 策略是「**默认走出口**，只有已确认是国内域名才用本地解析」。
//
// 为什么不能反过来（先本地、再学习）：那样每个**新域名**的第一次解析都会
// 泄露给本地运营商 DNS。DNS 泄露测试用的正是每次新生成的随机域名，
// 所以旧策略实测 4 次里泄露 3 次（只有被学过的那个走了出口）。
//
// 代价：国内域名第一次也要经出口解析（多几十毫秒），拿到 CN 网段的结果后
// 会记为 local，后续自动走本地。用一点首次延迟换「零泄露」，这个交换是对的。
func (p *dnsPolicy) ShouldResolveViaEgress(domain string) bool {
	// 没有可用出口时只能本地解析，否则 DNS 直接不可用
	if p.candidates != nil && len(p.candidates()) == 0 {
		return false
	}

	p.mu.RLock()
	isLocal := p.local[domain]
	p.mu.RUnlock()
	return !isLocal
}

// PickEgress 挑一个出口来解析。
func (p *dnsPolicy) PickEgress(domain string) string {
	if p.pick == nil || p.candidates == nil {
		return ""
	}
	ids := p.candidates()
	if len(ids) == 0 {
		return ""
	}
	d := p.pick.Pick(nil, domain, ids, time.Now())
	return d.Egress
}

// Observe 在每次解析后更新归属判断。
//
// 判定依据统一是「解析结果的 IP 是否全部落在中国大陆网段」：
//   - 全部是 CN  → 记为 local，后续走本地解析（省一次出口往返）
//   - 有非 CN    → 记为 foreign，后续继续走出口
//
// 经出口解析的结果同样参与判断 —— 出口在境外，解析国内域名也可能拿到 CN 地址。
func (p *dnsPolicy) Observe(domain, egressID string, ips []net.IP) {
	if domain == "" || len(ips) == 0 {
		return
	}

	// 经出口解析过 → 建立域名亲和，后续同一域名优先走同一出口
	if egressID != "" && p.pick != nil {
		p.pick.MarkAffinity(egressID, registrableDomain(domain))
	}

	nets := p.cnNets()
	if len(nets) == 0 {
		// 没有网段数据时不下结论，保持「未定」状态（仍默认走出口）
		return
	}

	allCN := true
	for _, ip := range ips {
		if !inAnyNet(ip, nets) {
			allCN = false
			break
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if allCN {
		if len(p.local) < p.maxEntries {
			p.local[domain] = true
		}
		delete(p.foreign, domain)
		return
	}

	if len(p.foreign) < p.maxEntries {
		p.foreign[domain] = true
	}
	delete(p.local, domain)
}

func inAnyNet(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// —— 经出口解析 ——

// egressResolver 返回一个「经指定出口解析域名」的解析器。
//
// 实现方式：发 ResolveReq 控制消息，出口用它自己的 DNS 解析后回 ResolveResp。
// 这样拿到的 IP 是贴着那个出口位置的 CDN 地址。
type resolveWaiter struct {
	ch chan proto.ResolveResp
}

type egressResolverHub struct {
	disc *mesh.Discovery
	log  *slog.Logger
	mu   sync.Mutex
	wait map[string]chan proto.ResolveResp // 键 = 小写域名
}

func newEgressResolverHub(disc *mesh.Discovery, log *slog.Logger) *egressResolverHub {
	if log == nil {
		log = logx.Discard()
	}
	return &egressResolverHub{
		disc: disc, log: log,
		wait: make(map[string]chan proto.ResolveResp, 16),
	}
}

// OnResolveResp 处理出口回来的解析结果。
func (h *egressResolverHub) OnResolveResp(resp proto.ResolveResp, from *net.UDPAddr) {
	key := lower(resp.Domain)
	h.mu.Lock()
	ch := h.wait[key]
	delete(h.wait, key)
	h.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// Resolver 返回一个绑定到某个出口的解析器。
func (h *egressResolverHub) Resolver(nodeID string) dnsfwd.Resolver {
	return func(domain string) ([]net.IP, error) {
		if h.disc == nil {
			return nil, fmt.Errorf("发现模块未就绪")
		}
		addr := h.disc.Table().AddrOf(nodeID)
		if addr == nil {
			return nil, fmt.Errorf("出口 %s 不在线", nodeID)
		}

		key := lower(domain)
		ch := make(chan proto.ResolveResp, 1)
		h.mu.Lock()
		h.wait[key] = ch
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			delete(h.wait, key)
			h.mu.Unlock()
		}()

		if err := h.disc.SendControl(addr, proto.MsgResolveReq, proto.ResolveReq{Domain: domain}); err != nil {
			return nil, err
		}
		select {
		case resp := <-ch:
			if resp.Err != "" {
				return nil, fmt.Errorf("出口解析失败: %s", resp.Err)
			}
			var ips []net.IP
			for _, s := range resp.IPs {
				if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
					ips = append(ips, ip)
				}
			}
			return ips, nil
		case <-time.After(dnsClientTimeout):
			// 超时后由转发器回落到本地解析器（见 dnsfwd.Server.handle）：
			// 宁可给一个可能被污染的地址，也不要让客户端拿不到任何 IP。
			return nil, fmt.Errorf("出口 %s 解析 %s 超时", nodeID, domain)
		}
	}
}

// onResolveReq 处理对端发来的解析请求（本节点作为出口时）。
//
// 关键：用**本机**的 DNS 解析，这样拿到的才是贴合本机位置的 CDN 地址。
func (p *prober) onResolveReq(req proto.ResolveReq, from *net.UDPAddr, disc *mesh.Discovery) {
	resp := proto.ResolveResp{Domain: req.Domain}
	if req.Domain == "" {
		resp.Err = "域名为空"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), dnsResolveTimeout)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, req.Domain)
		if err != nil {
			resp.Err = err.Error()
		} else {
			for _, a := range addrs {
				if a.IP.To4() != nil {
					resp.IPs = append(resp.IPs, a.IP.String())
				}
			}
			if len(resp.IPs) == 0 {
				resp.Err = "没有 A 记录"
			}
		}
	}
	_ = disc.SendControl(from, proto.MsgResolveResp, resp)
}

// startDNS 按配置启动 DNS 转发器。
func startDNS(ctx context.Context, log *slog.Logger, c *config.Config,
	disc *mesh.Discovery, dp *dataPlane, selfID string, pr *prober) {

	hub := newEgressResolverHub(disc, log)

	// 出口侧：收到 ResolveReq 时用本机 DNS 解析并回结果
	if pr != nil {
		disc.SetResolveHandlers(func(req proto.ResolveReq, from *net.UDPAddr) {
			pr.onResolveReq(req, from, disc)
		}, hub.OnResolveResp)
	} else {
		disc.SetResolveHandlers(func(req proto.ResolveReq, from *net.UDPAddr) {
			(&prober{log: log}).onResolveReq(req, from, disc)
		}, hub.OnResolveResp)
	}

	// CN 网段提供者：优先用分流模块已下载的数据，避免重复下载
	cnNets := func() []*net.IPNet {
		if dp == nil || dp.dispatch == nil || dp.dispatch.cn == nil {
			return nil
		}
		return parseCIDRList(dp.dispatch.cn.CIDRs)
	}

	// packet 模式的候选来源：mesh 发现表里的出口节点
	cands := func() []string {
		now := time.Now()
		var ids []string
		for _, c := range disc.Table().EgressCandidates(now) {
			if c.NodeID != selfID {
				ids = append(ids, c.NodeID)
			}
		}
		return ids
	}
	pol := newDNSPolicy(log, dp.picker, selfID, cnNets, cands)
	srv, err := dnsfwd.New(dnsfwd.Options{
		Addr: c.DNSListen,
		// 本地解析器也用面向客户端的预算：出口那 2 秒已经花掉了，
		// 本地再等 5 秒客户端就超时了。
		Local:    dnsfwd.SystemResolver(dnsClientTimeout),
		ByEgress: hub.Resolver,
		Policy:   pol,
	})
	if err != nil {
		log.Warn("DNS 转发器创建失败",
			logx.Event("dns.create_failed"), logx.FieldErr, err.Error())
		return
	}
	if err := srv.Start(); err != nil {
		log.Warn("DNS 转发器启动失败",
			logx.Event("dns.start_failed"), logx.FieldErr, err.Error(),
			logx.Hint("端口可能被占用；换一个 dns_listen 地址再试"))
		return
	}

	log.Info("DNS 转发器已启动",
		logx.Event("dns.ready"), "addr", srv.Addr(),
		logx.Hint("把路由器的 DNS 上游指向这里，境外域名会自动经出口解析"))

	go func() {
		<-ctx.Done()
		srv.Close()
	}()
}

// parseCIDRList 把字符串 CIDR 列表解析成网段。
func parseCIDRList(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func lower(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}
