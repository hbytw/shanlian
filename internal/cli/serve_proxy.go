package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"slx/internal/buildinfo"
	"slx/internal/cnlist"
	"slx/internal/config"
	"slx/internal/dnsfwd"
	"slx/internal/envdetect"
	"slx/internal/logx"
	"slx/internal/mesh"
	"slx/internal/picker"
	"slx/internal/relay"
)

// serveProxy 是 proxy 模式的主循环。
//
// 与 packet 模式的 serve 相比，这里没有 forward.Plane、没有用户态 NAT：
// 流量由 gvisor 终结，出口由反向连接的 agent 承担。
func serveProxy(
	ctx context.Context,
	log *slog.Logger,
	c *config.Config,
	env *envdetect.Env,
	nodeID string,
	roles []string,
	disc *mesh.Discovery,
	startedAt time.Time,
) int {

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := writePIDFile(); err != nil {
		log.Debug("pid 文件写入失败", logx.FieldEvent, "pid.write_failed", logx.FieldErr, err.Error())
	}
	defer removePIDFile()

	// SIGUSR1 = 立刻自查一次（防火墙重载后由 hooks 触发）
	refreshCh := make(chan os.Signal, 1)
	signal.Notify(refreshCh, refreshSignal())
	defer signal.Stop(refreshCh)

	discErr := make(chan error, 1)
	go func() { discErr <- disc.Run(runCtx) }()

	save := time.NewTicker(saveInterval)
	defer save.Stop()

	saveState := func() {
		if err := disc.Table().SaveState(mesh.DefaultStatePath, nodeID); err != nil {
			log.Debug("状态缓存写入失败",
				logx.FieldEvent, "state.save_failed", logx.FieldErr, err.Error())
		}
	}

	log.Info("进入运行态（proxy 模式）", logx.FieldEvent, "runtime.ready",
		logx.FieldNodeID, nodeID, "port", disc.Port(), "mode", config.ModeProxy)

	pp, err := setupProxyPlane(runCtx, log, c, env, nodeID, disc)
	if err != nil {
		log.Error("数据面启动失败",
			logx.FieldEvent, "plane.failed",
			logx.FieldLayer, logx.LayerForward,
			logx.FieldErr, err.Error())
		log.Warn("降级为仅发现模式，流量不会被接管",
			logx.FieldEvent, "plane.degraded",
			logx.FieldHint, "先解决上面的错误；slx doctor 可复查环境")
		return 1
	}
	defer pp.cleanup()

	go func() {
		if rerr := pp.Run(runCtx); rerr != nil && runCtx.Err() == nil {
			log.Error("数据面退出", logx.FieldEvent, "plane.exited", logx.FieldErr, rerr.Error())
		}
	}()

	// 分流看门狗：有 agent 在线才接管，没有就退回直连。
	go pp.dispatch.watch(runCtx, log, pp.HasEgress)

	// ZT 环路保护规则的独立看护。
	//
	// 刻意跟分流看门狗分开：分流可以被暂停（没有 agent 就退回直连），
	// 但这条保护规则必须一直在 —— 它是隧道不把自己吸进隧道的最后一道防线。
	go watchZTGuard(runCtx, log, pp.sys, pp.ztGuard)

	// SIGUSR1 → 立刻自查
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-refreshCh:
				if pp.dispatch != nil && pp.dispatch.refresh != nil {
					select {
					case pp.dispatch.refresh <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	// DNS 转发器：境外域名经出口解析，建立域名↔出口亲和
	pp.dns = startProxyDNS(runCtx, log, c, pp, nodeID)

	// L1 链路质量探测：给选路器喂「网关↔每个 agent」的 RTT 与丢包。
	// 没有它，多 agent 时选路器是瞎的，只会用第一个。
	go startProxyProbe(runCtx, log, pp.gateway, pp.picker, pp.bw)
	go startNetProbe(runCtx, log, pp.gateway, pp.picker)

	// 选路快照：供 slx routes 读取
	go writeProxySnapshotLoop(runCtx, pp.picker, pp.gateway)

	// 状态快照：供 slx status 读取（与 packet 模式同一个快照文件）
	go writeStatusLoop(runCtx, func(time.Time) StatusSnapshot {
		return collectProxyStatus(startedAt, nodeID, roles, env, disc, pp)
	})

	// 周期输出数据面与 agent 状态（有变化才打）
	statsTick := time.NewTicker(5 * time.Second)
	defer statsTick.Stop()
	var lastRead, lastWrite int64

	reported := false
	firstReport := time.NewTimer(5 * time.Second)
	defer firstReport.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("收到退出信号，正在停止", logx.FieldEvent, "runtime.shutdown")
			saveState()
			return 0

		case err := <-discErr:
			if err != nil {
				log.Error("发现通道异常退出",
					logx.FieldEvent, "discovery.crashed",
					logx.FieldLayer, logx.LayerMesh, logx.FieldErr, err.Error())
				return 1
			}
			return 0

		case <-firstReport.C:
			reportAgents(log, pp)
			reported = true

		case <-save.C:
			saveState()
			if !reported {
				reportAgents(log, pp)
				reported = true
			}

		case <-statsTick.C:
			read, write, werr, ns := pp.Stats()
			if read == lastRead && write == lastWrite {
				continue
			}
			log.Info("proxy 数据面计数",
				logx.FieldEvent, "plane.stats",
				"tun_in", read-lastRead,
				"tun_out", write-lastWrite,
				"write_err", werr,
				"tcp_ok", ns.TCPAccepted,
				"tcp_fail", ns.TCPFailed,
				"udp_ok", ns.UDPAccepted)
			lastRead, lastWrite = read, write
		}
	}
}

// reportAgents 输出一次 agent 接入汇总。
func reportAgents(log *slog.Logger, pp *proxyPlane) {
	ids := pp.gateway.AgentIDs()
	if len(ids) == 0 {
		log.Warn("尚无出口 agent 接入",
			logx.FieldEvent, "relay.no_agent",
			logx.FieldLayer, logx.LayerMesh,
			logx.FieldHint, fmt.Sprintf("在出口节点执行: slx agent --gateway <本机IP:%d>",
				agentPortOf(pp)))
		return
	}
	log.Info("已接入出口 agent",
		logx.FieldEvent, "relay.agents",
		"count", len(ids), "agents", shortIDs(ids))
}

// agentPortOf 从监听地址里取端口，仅用于提示文案。
func agentPortOf(pp *proxyPlane) int {
	_, portStr, err := net.SplitHostPort(pp.gateway.Addr())
	if err != nil {
		return 11901
	}
	if n, convErr := strconv.Atoi(portStr); convErr == nil {
		return n
	}
	return 11901
}

// shortIDs 截短 node_id 便于日志阅读。
func shortIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, id)
	}
	return out
}

// collectProxyStatus 组装 proxy 模式的运行状态快照。
//
// 与 packet 模式的 collectStatus 分开写，而不是塞一堆 if：
// 两种模式的关键指标完全不同（一个看 peer/选路，一个看 agent/协议栈），
// 硬塞会让两边都难读，也容易把不适用的故障判定误报出来。
func collectProxyStatus(
	startedAt time.Time,
	nodeID string,
	roles []string,
	env *envdetect.Env,
	disc *mesh.Discovery,
	pp *proxyPlane,
) StatusSnapshot {

	now := time.Now()
	s := StatusSnapshot{
		UpdatedAt: now,
		StartedAt: startedAt,
		Version:   buildinfo.Version,
		NodeID:    nodeID,
		Roles:     roles,
		Mode:      config.ModeProxy,
		TunName:   pp.dev.Name(),
		InnerMTU:  pp.dev.MTU(),
	}
	s.Env = EnvSummary{
		DefaultIface: env.DefaultIface,
		LanIface:     env.LANIface,
		Firewall:     string(env.Firewall),
		HasIPSet:     env.HasIPSet,
		HasTUN:       env.HasTUN,
	}
	for _, z := range env.ZTIfaces {
		s.Env.ZTIfaces = append(s.Env.ZTIfaces, z.Name)
	}

	if disc != nil {
		tbl := disc.Table()
		s.Peers.Total = tbl.Len()
		counts := tbl.Count(now)
		s.Peers.ByState = map[string]int{}
		for st, n := range counts {
			s.Peers.ByState[st.Chinese()] = n
		}
	}

	if pp.dispatch != nil {
		s.Split.Enabled = pp.dispatch.enabled
		s.Split.Ifaces = pp.dispatch.spc.LANIfaces
		if pp.dispatch.cn != nil {
			s.Split.CNEntries = len(pp.dispatch.cn.CIDRs)
			s.Split.CNSource = pp.dispatch.cn.Source
			s.Split.CNAgeSec = int(now.Sub(pp.dispatch.cn.Updated).Seconds())
		}
	}

	ids := pp.gateway.AgentIDs()
	read, write, _, ns := pp.Stats()
	icmpOK, icmpDrop := pp.ICMPStats()
	// 通道状态：直连 / 兜底各自的评分与熔断状态。
	for _, ci := range pp.gateway.ChannelInfos() {
		s.Channels = append(s.Channels, ChannelSummary{
			NodeID:  ci.NodeID,
			Kind:    ci.Kind,
			Remote:  ci.Remote,
			Score:   ci.Score,
			Primary: ci.Primary,
			Open:    ci.Open,
			Since:   ci.Since,
			Streams: ci.Streams,
		})
	}
	if pp.dns != nil {
		q, local, egress, fail := pp.dns.Stats()
		cacheHits, cacheMisses := pp.dns.CacheStats()
		s.DNS = DNSSummary{
			Enabled: true, Addr: pp.dns.Addr(),
			Queries: q, LocalHits: local, EgressHits: egress, Failures: fail,
			CacheHits: cacheHits, CacheMisses: cacheMisses,
			CacheEntries: pp.dns.CacheLen(),
		}
	}
	// 每台出口的实时指标来自选路器快照 —— 和 `slx routes` 是同一份数据，
	// 保证页面和命令行看到的东西一致。
	var details []AgentDetail
	if pp.picker != nil && len(ids) > 0 {
		remotes := pp.gateway.AgentRemotes()
		labels := agentLabels()
		snap := pp.picker.SnapshotState(ids, now)
		for _, c := range snap.Candidates {
			rem := remotes[c.NodeID]
			details = append(details, AgentDetail{
				ID:             shortIDs([]string{c.NodeID})[0],
				Remote:         rem,
				Label:          labelFor(rem, labels),
				RTTMS:          c.RTTMS,
				BWMbps:         c.BWMbps,
				Loss:           c.Loss,
				Score:          c.Score,
				Breaker:        c.Breaker,
				BreakerReason:  c.BreakerReason,
				TargetFailures: c.TargetFailures,
				Eligible:       c.Eligible,
				Note:           c.Note,
			})
		}
		// 按评分降序：评分最高的排最前，也就是「当前最该用的那台」。
		sort.SliceStable(details, func(i, j int) bool {
			return details[i].Score > details[j].Score
		})
	}

	s.Agents = AgentSummary{
		Details:     details,
		IcmpReplied: icmpOK,
		IcmpDropped: icmpDrop,
		Count:       len(ids),
		IDs:         shortIDs(ids),
		Listen:      pp.gateway.Addr(),
		TCPAccepted: ns.TCPAccepted,
		TCPFailed:   ns.TCPFailed,
		UDPAccepted: ns.UDPAccepted,
		TunRead:     read,
		TunWrite:    write,
	}

	// —— 故障判定 ——
	// 注意判据：proxy 模式下 ZeroTier 不是必需的，所以「没有 peer」只是提示，
	// 「没有 agent」才是致命 —— 没有 agent 就一条流量也出不去。
	if len(ids) == 0 {
		s.Findings = append(s.Findings, Finding{
			Code: "relay.no_agent", Severity: SeverityFatal, Layer: "组网",
			Title:  "没有任何出口 agent 接入",
			Detail: "网关已监听 " + pp.gateway.Addr() + "，但没有 agent 连进来；此时所有境外连接都发不出去",
			Action: "在出口节点执行: slx agent --gateway <网关可达地址:" +
				itoaSafe(agentPortOf(pp)) + ">；确认两端 network_key 一致、网关防火墙放行该端口",
		})
	} else if read > 20 && ns.TCPAccepted == 0 && ns.UDPAccepted == 0 {
		// 阈值 20：slx0 刚建好、内核会往里丢几个 IPv6 邻居发现之类的杂包，
		// 只凭「有包」就告警会误报。真正有连接尝试时这个数远大于 20。
		s.Findings = append(s.Findings, Finding{
			Code: "netstack.no_accept", Severity: SeverityDegraded, Layer: "转发",
			Title:  "有包进 TUN 但没有任何连接被接受",
			Detail: "说明内核确实把流量交给了 slx0，但用户态协议栈没能建连",
			Action: "查日志里的 netstack.tcp_failed / udp_failed；确认出口 agent 能直连目标",
		})
	}
	return s
}

// itoaSafe 把端口转成字符串，0 时回退 11901。
func itoaSafe(v int) string {
	if v <= 0 {
		return "11901"
	}
	return strconv.Itoa(v)
}

// writeProxySnapshotLoop 周期写选路快照，供 slx routes 读取。
//
// 候选来源是 agent 连接池而不是发现表 —— 与 proxyConnector.candidates 保持一致，
// 否则会出现「slx routes 说没有候选，但连接其实是通的」这种自相矛盾。
func writeProxySnapshotLoop(ctx context.Context, sel *picker.Selector, gw *relay.Gateway) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			_ = writeRouteSnapshot(sel.SnapshotState(gw.AgentIDs(), now))
		}
	}
}

// startProxyDNS 启动 proxy 模式的 DNS 转发器。
//
// 与 packet 模式共用 dnsfwd 与 dnsPolicy，差别只在两处注入：
//   - 候选出口来自 agent 连接池（而不是 mesh 发现表）；
//   - 「经出口解析」改成开一条 relay 流请 agent 解析。
//
// 这件事的价值不在「能解析」，而在 CDN 就近：同一域名在网关当地和出口当地
// 解析出来的 IP 可能属于不同边缘节点。解析在东京、流量却从法兰克福出去，
// 视频照样卡 —— 所以必须让出口那边去解析。
func startProxyDNS(ctx context.Context, log *slog.Logger, c *config.Config,
	pp *proxyPlane, nodeID string) *dnsfwd.Server {

	if c.DNSListen == "" {
		return nil // 未配置就不启用，不占端口
	}

	// CN 网段：决定「哪些域名该经出口解析」。
	//
	// 优先复用分流模块已下载的数据；proxy 模式下 dispatch 可能关闭
	// （没装分流规则），此时独立加载一次 —— 否则策略拿不到国内网段，
	// 会把所有域名都当成本地域名，DNS 就近完全失效（实测踩到）。
	var (
		cnOnce   sync.Once
		cnLoaded []*net.IPNet
	)
	cnNets := func() []*net.IPNet {
		if pp.dispatch != nil && pp.dispatch.cn != nil {
			return parseCIDRList(pp.dispatch.cn.CIDRs)
		}
		cnOnce.Do(func() {
			res, lerr := loadCNList(cnlist.DefaultSources, log)
			if lerr != nil {
				log.Warn("加载国内网段失败，DNS 归属判断将退化为全部经出口解析",
					logx.FieldEvent, "dns.cnlist_failed", logx.FieldErr, lerr.Error())
				return
			}
			cnLoaded = parseCIDRList(res.CIDRs)
			log.Info("已加载国内网段（供 DNS 归属判断）",
				logx.FieldEvent, "dns.cnlist_ready", "entries", len(cnLoaded))
		})
		return cnLoaded
	}

	cands := func() []string { return pp.gateway.AgentIDs() }
	pol := newDNSPolicy(log, pp.picker, nodeID, cnNets, cands)

	byEgress := func(egressID string) dnsfwd.Resolver {
		return func(domain string) ([]net.IP, error) {
			// 2 秒预算：超时后由转发器回落到本地解析器。
			// 宁可给一个可能被污染的地址，也不要让客户端拿不到任何 IP
			//（拿不到 IP，客户端连国内站点也打不开）。
			return pp.gateway.Resolve(egressID, domain, dnsClientTimeout)
		}
	}

	srv, err := dnsfwd.New(dnsfwd.Options{
		Addr: c.DNSListen,
		// 本地解析器也用面向客户端的预算（出口那 2 秒已经花掉了）。
		Local:    dnsfwd.SystemResolver(dnsClientTimeout),
		ByEgress: byEgress,
		Policy:   pol,
	})
	if err != nil {
		log.Warn("DNS 转发器创建失败",
			logx.FieldEvent, "dns.create_failed", logx.FieldErr, err.Error())
		return nil
	}
	if err := srv.Start(); err != nil {
		// 用 ERROR 而不是 WARN：转发器起不来时，若 dnsmasq 已指到这个端口，
		// 全屋 DNS 会直接不可用。这个后果值得 ERROR 级别。
		// 最常见的坑是端口被占（5353 被 avahi-daemon 占用是典型）。
		log.Error("DNS 转发器启动失败",
			logx.FieldEvent, "dns.start_failed", logx.FieldErr, err.Error(),
			logx.FieldHint, "端口可能被占用；换一个 dns_listen 地址再试（不要用 5353，那是 mDNS 保留端口）")
		return nil
	}

	log.Info("DNS 转发器已启动（proxy 模式）",
		logx.FieldEvent, "dns.ready", "addr", srv.Addr(),
		logx.FieldHint, "把路由器的 DNS 上游指向这里，境外域名会自动经出口解析")

	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	return srv
}

// agentLabels 解析出口位置标签。
//
// 格式：环境变量 SLX_AGENT_LABELS="192.168.99.6=香港,192.168.99.18=韩国-ORACLE1"
//
// 为什么用对端 ZT 地址做键：握手协议是二进制帧，加字段要升协议版本，
// 代价远大于收益。而出口的对端地址（ZT 地址）在 ZT 控制台里本来就能对上机器，
// 用它做键最省事也最稳。
//
// 没配标签时页面显示 ZT 地址本身，不影响使用。
func agentLabels() map[string]string {
	raw := strings.TrimSpace(os.Getenv("SLX_AGENT_LABELS"))
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		k, v, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

// labelFor 按对端地址查标签；地址带端口时只取主机部分。
func labelFor(remote string, labels map[string]string) string {
	if remote == "" || len(labels) == 0 {
		return ""
	}
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	return labels[host]
}
