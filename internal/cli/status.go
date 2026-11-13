package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"slx/internal/buildinfo"
	"slx/internal/config"
	"slx/internal/envdetect"
	"slx/internal/forward"
	"slx/internal/mesh"
	"slx/internal/picker"
)

// statusStatePath 是守护进程状态快照文件。
func statusStatePath() string {
	if p := os.Getenv("SLX_STATUS_STATE"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "slx-status.json")
}

// StatusSnapshot 是 `slx status` 展示的全部信息。
//
// 设计原则：**CLI 只读文件，不连守护进程**。
// 这样 status 在守护进程卡死、甚至已经崩溃时依然可用 ——
// 恰恰是排障最需要它的时候。
type StatusSnapshot struct {
	UpdatedAt time.Time `json:"updated_at"`
	StartedAt time.Time `json:"started_at"`

	Version  string   `json:"version"`
	NodeID   string   `json:"node_id"`
	Roles    []string `json:"roles"`
	InnerIP  string   `json:"inner_ip"`
	TunName  string   `json:"tun_name"`
	InnerMTU int      `json:"inner_mtu"`

	// Mode 是数据面模式：packet（裸包转发）/ proxy（反向连接 + 用户态协议栈）。
	// 旧版快照没有这个字段，读到时按 packet 处理，保证向后兼容。
	Mode string `json:"mode,omitempty"`

	Peers  PeerSummary      `json:"peers"`
	Plane  forward.Snapshot `json:"plane"`
	Split  SplitSummary     `json:"split"`
	Route  RouteSummary     `json:"route"`
	Agents AgentSummary     `json:"agents,omitempty"`
	DNS    DNSSummary       `json:"dns,omitempty"`
	// Channels 是每条 relay 通道的实时状态（直连 / 兜底）。
	//
	// 为什么单列一块：Agents.Details 里的延迟是 **agent 级 L1 探测**的结果，
	// 走的是「当前选中的那条通道」。而运维真正想看的是
	// 「直连通道到底比 ZeroTier 好多少」「哪条通道被熔断了」。
	// 两套数字混在一张表里会互相误导（现场就踩过这个坑）。
	Channels []ChannelSummary `json:"channels,omitempty"`
	// 以下为 packet 模式专用；proxy 模式下为空
	Findings []Finding  `json:"findings,omitempty"`
	Env      EnvSummary `json:"env"`
}

// DNSSummary 是 DNS 转发器概况。
type DNSSummary struct {
	Enabled    bool   `json:"enabled"`
	Addr       string `json:"addr,omitempty"`
	Queries    uint64 `json:"queries"`
	LocalHits  uint64 `json:"local_hits"`
	EgressHits uint64 `json:"egress_hits"`
	Failures   uint64 `json:"failures"`
	// CacheHits / CacheMisses 是内部短 TTL 缓存的命中与未命中数。
	//
	// 口径：queries = 命中 + 未命中；未命中 = 本地 + 经出口 + 失败。
	CacheHits   uint64 `json:"cache_hits"`
	CacheMisses uint64 `json:"cache_misses"`
	// CacheEntries 是当前缓存里的条目数。
	CacheEntries int `json:"cache_entries"`
}

// AgentSummary 是 proxy 模式下出口 agent 与用户态协议栈的概况。
type AgentSummary struct {
	// Count 当前在线的 agent 数量。
	Count int `json:"count"`
	// IDs 在线 agent 的 node_id（截短）。
	IDs []string `json:"ids,omitempty"`
	// Details 每台出口的实时指标（延迟 / 带宽 / 丢包 / 评分）。
	//
	// 为什么放在 Agents 里而不是单独一块：这些指标就是「这条出口现在怎么样」，
	// 和「接入了几个」是同一个问题的两个层次，放一起页面上才能对齐着看。
	Details []AgentDetail `json:"details,omitempty"`
	// Listen agent 反向连接监听地址。
	Listen string `json:"listen,omitempty"`
	// 用户态协议栈计数：这三个数直接回答「连接有没有被拦下来并成功中继」。
	TCPAccepted int64 `json:"tcp_accepted"`
	TCPFailed   int64 `json:"tcp_failed"`
	UDPAccepted int64 `json:"udp_accepted"`
	// TCP 失败的分类归因：三者之和 = TCPFailed
	//   connect  ⇒ 出口连不上（查出口/选路 ）
	//   endpoint ⇒ 协议栈端点建不起来（查协议栈 ）
	//   limit    ⇒ 并发超限（查负载 ）
	TCPFailConnect  int64 `json:"tcp_fail_connect"`
	TCPFailEndpoint int64 `json:"tcp_fail_endpoint"`
	TCPFailLimit    int64 `json:"tcp_fail_limit"`
	// TCPActive 是当前在途的 TCP 中继连接数。
	TCPActive int64 `json:"tcp_active"`
	// TCPBytes/UDPBytes 是中继的字节数（双向合计）—— 判断「流量有没有真的走代理」最直接的证据
	TCPBytes int64 `json:"tcp_bytes"`
	UDPBytes int64 `json:"udp_bytes"`
	// UDPFailed 是 UDP 建连失败次数。
	UDPFailed int64 `json:"udp_failed"`
	// TunRead/TunWrite 是从 TUN 读/写的 IP 包数。
	TunRead  int64 `json:"tun_read"`
	TunWrite int64 `json:"tun_write"`
	// ICMP echo 接管情况：IcmpReplied 是成功回给客户端的，IcmpDropped 是过载丢弃的。
	IcmpReplied int64 `json:"icmp_replied"`
	IcmpDropped int64 `json:"icmp_dropped"`
}

// ChannelSummary 是单条 relay 通道的状态。
type ChannelSummary struct {
	// NodeID 是这条通道对应的出口（截短显示用原始值，这里保留完整）。
	NodeID string `json:"node_id"`
	// Kind 是通道类型："tls"（公网直连）/ "zt"（ZeroTier 兜底）。
	Kind string `json:"kind"`
	// Remote 是对端地址。
	Remote string `json:"remote,omitempty"`
	// Score 是通道健康分（0~100）。
	Score float64 `json:"score"`
	// Primary 表示这是当前承载业务流量的主通道。
	Primary bool `json:"primary"`
	// Open 表示这条通道被熔断了（Open 是熔断器状态，不是「打开」）。
	Open bool `json:"open"`
	// Since 是这条通道建立的时间。
	Since string `json:"since,omitempty"`
	// Streams 是这条通道当前打开的 yamux 流数（开流卡住的前兆指标）。
	Streams int `json:"streams"`
	// RTTMs / Succ / BWMbps 是该通道的实时探测指标（EWMA）：
	// 往返时延、成功率（0~1）、吞吐（Mbps）。
	// RTT/Succ 无样本时为兜底值；BW/BWUp/BWDown 无样本为 0（页面显示「—」）。
	RTTMs  float64 `json:"rtt_ms"`
	Succ   float64 `json:"succ"`
	BWMbps float64 `json:"bw_mbps"`
	// BWUp / BWDown 是实时上行/下行速率（Mbps，10 秒结算、无样本 0）。
	BWUp   float64 `json:"bw_up"`
	BWDown float64 `json:"bw_down"`
}

// AgentDetail 是单台出口的实时指标。
type AgentDetail struct {
	ID      string  `json:"id"`
	Remote  string  `json:"remote,omitempty"`  // 对端地址（ZT 地址）
	Label   string  `json:"label,omitempty"`   // 位置标签（配置的映射，可空）
	RTTMS   float64 `json:"rtt_ms"`            // 网关 ↔ 出口 往返延迟
	BWMbps  float64 `json:"bw_mbps"`           // 链路吞吐（被动估算）
	Loss    float64 `json:"loss"`              // 链路丢包率 0~1
	Score   float64 `json:"score"`             // 选路评分
	Breaker string  `json:"breaker,omitempty"` // 熔断状态：空 / 熔断 / 试探
	// BreakerReason 是最近一次失败的简短原因（熔断/试探时才有）。
	// 例："L2 连不上 1.1.1.1（超时）"。
	BreakerReason string `json:"breaker_reason,omitempty"`
	// TargetFailures 是累计的「用户目标不可达」次数。
	//
	// 它**不代表出口坏了**，只说明有这么多用户请求的目标打不开
	//（被墙 / 网站挂了）。这类失败不推进熔断计数。
	TargetFailures int `json:"target_failures,omitempty"`
	// LinkFailures 是累计的**链路层**失败次数（L1 探测失败 ）。
	//
	// ★ 链路失败**不推进熔断计数** —— 链路通 ≠ 出口可用
	//   这里只是观测：链路抖得厉害时能一眼看到
	LinkFailures int    `json:"link_failures,omitempty"`
	LinkReason   string `json:"link_reason,omitempty"`
	Eligible     bool   `json:"eligible"` // 是否参与选路
	Note         string `json:"note,omitempty"`
	// 评分拆解：三个维度各自的加权贡献（之和 = 加亲和前的 Score）。
	// 状态页用它解释「为什么只有 30-40 分」。
	ScoreBW   float64 `json:"score_bw"`
	ScoreRTT  float64 `json:"score_rtt"`
	ScoreLoss float64 `json:"score_loss"`
	// Affinity 是该出口是否对当前目标有 CDN 亲和加成（+15 分）。
	Affinity bool `json:"affinity,omitempty"`
	// UptimeSeconds 是该出口本次接入以来的秒数。
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
}

// PeerSummary 是对等节点概况。
type PeerSummary struct {
	Total   int            `json:"total"`
	ByState map[string]int `json:"by_state"`
	Egress  int            `json:"egress"`
}

// SplitSummary 是何流状态。
type SplitSummary struct {
	Enabled   bool     `json:"enabled"`
	Ifaces    []string `json:"ifaces,omitempty"`
	CNEntries int      `json:"cn_entries"`
	CNSource  string   `json:"cn_source,omitempty"`
	CNAgeSec  int      `json:"cn_age_sec"`
}

// RouteSummary 是选路概况。
type RouteSummary struct {
	Current    string          `json:"current"`
	Candidates []picker.Scored `json:"candidates,omitempty"`
	Routes     int             `json:"routes"`
}

// EnvSummary 是运行环境概况。
type EnvSummary struct {
	DefaultIface string   `json:"default_iface,omitempty"`
	ZTIfaces     []string `json:"zt_ifaces,omitempty"`
	LanIface     string   `json:"lan_iface,omitempty"`
	Firewall     string   `json:"firewall,omitempty"`
	HasIPSet     bool     `json:"has_ipset"`
	HasTUN       bool     `json:"has_tun"`
}

// —— 故障自动分类 ——

// Severity 是问题严重程度。
type Severity string

const (
	SeverityFatal    Severity = "致命" // 完全不可用
	SeverityDegraded Severity = "降级" // 能用但有问题
	SeverityWarn     Severity = "提示" // 值得注意
)

// Finding 是一条诊断结论。
//
// 每条结论都必须带**可执行的下一步**：只说"有问题"而不说怎么办，
// 对用户没有价值。
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Action   string   `json:"action"`
	Layer    string   `json:"layer,omitempty"`
}

// String 渲染成一行。
func (f Finding) String() string {
	s := fmt.Sprintf("[%s] %s", f.Severity, f.Title)
	if f.Detail != "" {
		s += "\n    现象：" + f.Detail
	}
	if f.Action != "" {
		s += "\n    处理：" + f.Action
	}
	return s
}

// ClassifyFaults 根据状态快照自动归类故障。
//
// 判据按「从整体到局部」的顺序排列：先看能不能通，再看哪一段不通。
// 每个分支都对应一个真实踩过或设计时预判的故障。
func ClassifyFaults(s *StatusSnapshot, now time.Time) []Finding {
	var out []Finding

	add := func(f Finding) { out = append(out, f) }

	// 1. 完全没有任何对端 → 组网失败
	if s.Peers.Total == 0 {
		add(Finding{
			Code: "mesh.no_peers", Severity: SeverityFatal, Layer: "组网",
			Title:  "局域网内没有发现其他 slx 节点",
			Detail: "本机在 ZeroTier 网段上广播了 HELLO，但一个回应都没有",
			Action: "确认其它节点已启动 slx；确认它们与本机在同一个 ZeroTier 网络里；" +
				"若 ZeroTier 关了广播，请把它们的内网地址写进配置的 peers 列表",
		})
		return out // 后面的判断都依赖对端，没对端就没意义了
	}

	// 2. 有对端但没有可用出口
	if s.Peers.Egress == 0 {
		add(Finding{
			Code: "mesh.no_egress", Severity: SeverityFatal, Layer: "组网",
			Title:  "发现了节点，但没有任何一台可用作出口",
			Detail: fmt.Sprintf("共 %d 个节点，均未上报出口能力或已失联", s.Peers.Total),
			Action: "确认至少一台 VPS 的 slx 正常运行；用 `slx peers` 查看各节点状态",
		})
	}

	// 3. 出口都在熔断
	if len(s.Route.Candidates) > 0 {
		allBroken := true
		for _, c := range s.Route.Candidates {
			if c.Eligible {
				allBroken = false
				break
			}
		}
		if allBroken {
			add(Finding{
				Code: "route.all_breakers_open", Severity: SeverityFatal, Layer: "选路",
				Title:  "所有出口都在熔断中",
				Detail: "健康检查连续失败，说明链路本身已经不通",
				Action: "检查 ZeroTier 链路（`slx peers` 看延迟）；确认 VPS 没被限速或关机",
			})
		}
	}

	// 4. 分流没启用
	if !s.Split.Enabled {
		add(Finding{
			Code: "split.disabled", Severity: SeverityDegraded, Layer: "分流",
			Title:  "境外流量分流未生效",
			Detail: "数据面在跑，但没有接管国外流量，所有网站都走直连",
			Action: "确认有可用出口（`slx peers`）；确认国内网段能下载（`slx doctor`）；" +
				"低端设备内存不足时也会跳过分流",
		})
	}

	// 5. 分流启用了但数据面完全没流量
	if s.Split.Enabled && s.Plane.TunRead == 0 && s.Plane.TunWrite == 0 {
		add(Finding{
			Code: "split.no_traffic", Severity: SeverityDegraded, Layer: "分流",
			Title:  "分流规则已装好，但没有任何流量进来",
			Detail: "可能是局域网设备的网关没指向本机，或者分流接口选错了",
			Action: "确认客户端的默认网关是本机；用 `slx doctor` 复查分流接口",
		})
	}

	// 6. 有发出的包但完全没有回来的包 → 典型「能发不能收」
	if s.Plane.Sent > 10 && s.Plane.TunWrite == 0 {
		add(Finding{
			Code: "forward.one_way", Severity: SeverityFatal, Layer: "转发",
			Title:  "流量发得出去、完全收不到回来",
			Detail: fmt.Sprintf("已发 %d 包，回程 0 包", s.Plane.Sent),
			Action: "这是反向路径过滤的典型症状。检查 `slx doctor` 的反向路径过滤项；" +
				"确认 net.ipv4.conf.all.rp_filter 已被设为 0",
		})
	}

	// 7. 有路由失败的包
	if s.Plane.NoRoute > 0 {
		pct := 0.0
		if total := s.Plane.Sent + s.Plane.NoRoute; total > 0 {
			pct = float64(s.Plane.NoRoute) / float64(total) * 100
		}
		sev := SeverityWarn
		if pct > 20 {
			sev = SeverityDegraded
		}
		add(Finding{
			Code: "forward.no_route", Severity: sev, Layer: "转发",
			Title:  fmt.Sprintf("%d 个包因为找不到可用出口被丢弃（%.1f%%）", s.Plane.NoRoute, pct),
			Detail: "出口在探测为可用之前，或者全部处于熔断时会出现",
			Action: "若持续出现，用 `slx routes` 看候选评分；`slx explain <目标>` 看具体依据",
		})
	}

	// 8. 国内网段数据太旧
	if s.Split.Enabled && s.Split.CNAgeSec > 7*24*3600 {
		add(Finding{
			Code: "split.cnlist_stale", Severity: SeverityWarn, Layer: "分流",
			Title:  fmt.Sprintf("国内网段数据已 %d 天没更新", s.Split.CNAgeSec/86400),
			Detail: "过旧的数据可能把新分配的国内 IP 误判成境外",
			Action: "确认本机能访问 ispip.clang.cn；`slx doctor` 会重新拉取",
		})
	}

	// 9. 丢包率偏高
	if len(s.Route.Candidates) > 0 {
		for _, c := range s.Route.Candidates {
			if c.Eligible && c.Loss > 0.10 {
				add(Finding{
					Code: "link.high_loss", Severity: SeverityWarn, Layer: "链路",
					Title:  fmt.Sprintf("出口 %s 丢包率 %.0f%%", c.NodeID, c.Loss*100),
					Detail: "ZeroTier 链路质量差，会明显拉低吞吐",
					Action: "检查该 VPS 的带宽与负载；考虑在配置里排除它",
				})
				break
			}
		}
	}

	// 10. 延迟过高
	if len(s.Route.Candidates) > 0 {
		for _, c := range s.Route.Candidates {
			if c.Eligible && c.RTTMS > 400 {
				add(Finding{
					Code: "link.high_rtt", Severity: SeverityWarn, Layer: "链路",
					Title:  fmt.Sprintf("出口 %s 延迟 %.0fms 偏高", c.NodeID, c.RTTMS),
					Detail: "可能是地理距离远，也可能是链路拥塞",
					Action: "若有多台出口，`slx explain` 看看是否选了更近的那台",
				})
				break
			}
		}
	}

	// 11. 环境缺能力
	if !s.Env.HasTUN {
		add(Finding{
			Code: "env.no_tun", Severity: SeverityFatal, Layer: "环境",
			Title:  "系统没有 /dev/net/tun",
			Action: "加载 tun 模块（modprobe tun），或换用支持 TUN 的内核",
		})
	}
	if s.Split.Enabled && !s.Env.HasIPSet {
		add(Finding{
			Code: "env.no_ipset", Severity: SeverityFatal, Layer: "环境",
			Title:  "缺少 ipset 命令，但分流却在跑",
			Action: "安装 ipset（OpenWrt: opkg install ipset；CentOS: yum install ipset）",
		})
	}

	return out
}

// WorstSeverity 返回最严重的一级（用于状态码与颜色）。
func WorstSeverity(fs []Finding) Severity {
	worst := Severity("")
	for _, f := range fs {
		switch f.Severity {
		case SeverityFatal:
			return SeverityFatal
		case SeverityDegraded:
			worst = SeverityDegraded
		case SeverityWarn:
			if worst == "" {
				worst = SeverityWarn
			}
		}
	}
	return worst
}

// —— 采集与落盘 ——

// collectStatus 把当前运行时状态汇总成快照。
func collectStatus(
	startedAt time.Time,
	nodeID string,
	roles []string,
	innerIP, tunName string,
	innerMTU int,
	env *envdetect.Env,
	disc *mesh.Discovery,
	dp *dataPlane,
	c *config.Config,
	now time.Time,
) StatusSnapshot {
	s := StatusSnapshot{
		UpdatedAt: now, StartedAt: startedAt,
		Version: buildinfo.Version, NodeID: nodeID,
		Roles: roles, InnerIP: innerIP, TunName: tunName, InnerMTU: innerMTU,
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
		counts := tbl.Count(now)
		s.Peers.Total = tbl.Len()
		s.Peers.ByState = map[string]int{}
		for st, n := range counts {
			s.Peers.ByState[st.Chinese()] = n
		}
		for _, p := range tbl.EgressCandidates(now) {
			if p.NodeID != nodeID {
				s.Peers.Egress++
			}
		}
	}

	if dp != nil {
		if dp.plane != nil {
			s.Plane = dp.plane.Stats.Snapshot()
		}
		if dp.dispatch != nil {
			s.Split.Enabled = dp.dispatch.enabled
			s.Split.Ifaces = dp.dispatch.spc.LANIfaces
			if dp.dispatch.cn != nil {
				s.Split.CNEntries = len(dp.dispatch.cn.CIDRs)
				s.Split.CNSource = dp.dispatch.cn.Source
				s.Split.CNAgeSec = int(now.Sub(dp.dispatch.cn.Updated).Seconds())
			}
		}
		if dp.picker != nil {
			var ids []string
			if disc != nil {
				for _, p := range disc.Table().EgressCandidates(now) {
					if p.NodeID != nodeID {
						ids = append(ids, p.NodeID)
					}
				}
			}
			rs := dp.picker.SnapshotState(ids, now)
			s.Route.Current = rs.Current
			s.Route.Candidates = rs.Candidates
			s.Route.Routes = len(rs.Routes)
		}
	}

	s.Findings = ClassifyFaults(&s, now)
	return s
}

// writeStatusSnapshot 原子写入状态快照。
func writeStatusSnapshot(s StatusSnapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := statusStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readStatusSnapshot 读状态快照。
func readStatusSnapshot() (StatusSnapshot, error) {
	var s StatusSnapshot
	b, err := os.ReadFile(statusStatePath())
	if err != nil {
		return s, fmt.Errorf("读不到运行状态：%w\n提示：守护进程可能没在运行，或还没产出第一份快照", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("状态文件损坏：%w", err)
	}
	return s, nil
}

// writeStatusLoop 定期落盘状态快照。
func writeStatusLoop(ctx context.Context, fn func(time.Time) StatusSnapshot) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			_ = writeStatusSnapshot(fn(now))
		}
	}
}

// —— slx status ——

// StatusCmd 实现 `slx status`。
func StatusCmd(args []string) int {
	s, err := readStatusSnapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	now := time.Now()

	fmt.Printf("闪连 SLX %s\n", s.Version)
	mode := s.Mode
	if mode == "" {
		mode = "packet" // 旧快照无此字段，按 packet 解释
	}
	fmt.Printf("节点 %s    角色 %s    模式 %s\n", s.NodeID, strings.Join(s.Roles, "+"), mode)
	fmt.Printf("运行 %s（自 %s）\n\n",
		humanAge(now.Sub(s.StartedAt)), s.StartedAt.Format("01-02 15:04:05"))

	if mode == "proxy" {
		printProxyStatus(&s)
		return finishStatus(&s)
	}

	// 组网
	fmt.Println("组网：")
	if s.Peers.Total == 0 {
		fmt.Println("  （无其他节点）")
	} else {
		var parts []string
		for st, n := range s.Peers.ByState {
			parts = append(parts, fmt.Sprintf("%s %d", st, n))
		}
		sort.Strings(parts)
		fmt.Printf("  共 %d 个节点（%s），其中可用出口 %d 台\n",
			s.Peers.Total, strings.Join(parts, "，"), s.Peers.Egress)
	}

	// 分流
	fmt.Println("\n分流：")
	if s.Split.Enabled {
		fmt.Printf("  已启用   接口 %s   国内网段 %d 条（%s，%s前更新）\n",
			strings.Join(s.Split.Ifaces, ","), s.Split.CNEntries,
			orDash(s.Split.CNSource), humanAge(time.Duration(s.Split.CNAgeSec)*time.Second))
	} else {
		fmt.Println("  未启用（境外流量走直连）")
	}

	// 选路
	fmt.Println("\n选路：")
	if s.Route.Current != "" {
		fmt.Printf("  当前出口 %s\n", s.Route.Current)
	} else {
		fmt.Println("  当前出口 （尚未选择）")
	}
	if len(s.Route.Candidates) > 0 {
		sort.SliceStable(s.Route.Candidates, func(i, j int) bool {
			return s.Route.Candidates[i].Score > s.Route.Candidates[j].Score
		})
		for _, c := range s.Route.Candidates {
			mark := "  "
			if c.NodeID == s.Route.Current {
				mark = "→ "
			}
			fmt.Printf("    %s%s  %.1f 分  %.0fms  %.1fM  丢包 %.0f%%\n",
				mark, c.NodeID, c.Score, c.RTTMS, c.BWMbps, c.Loss*100)
		}
	}

	// 流量
	fmt.Println("\n流量：")
	fmt.Printf("  TUN 收 %s 包 / 发 %s 包\n",
		humanCount(s.Plane.TunRead), humanCount(s.Plane.TunWrite))
	if s.Plane.TunBytesRead > 0 || s.Plane.TunBytesWrite > 0 {
		fmt.Printf("  流量   ↓ %s  ↑ %s\n",
			humanBytes(s.Plane.TunBytesRead), humanBytes(s.Plane.TunBytesWrite))
	}
	if s.Plane.NoRoute > 0 || s.Plane.SendErr > 0 || s.Plane.BadPacket > 0 {
		fmt.Printf("  异常   无路由 %d  发送失败 %d  坏包 %d\n",
			s.Plane.NoRoute, s.Plane.SendErr, s.Plane.BadPacket)
	}

	// 结论
	return finishStatus(&s)
}

// finishStatus 统一输出「结论」段并给出退出码。
func finishStatus(s *StatusSnapshot) int {
	fmt.Println()
	if len(s.Findings) == 0 {
		fmt.Println("结论：一切正常")
		return 0
	}
	fmt.Printf("结论：发现 %d 个问题\n", len(s.Findings))
	for _, f := range s.Findings {
		fmt.Println()
		for _, line := range strings.Split(f.String(), "\n") {
			fmt.Println(" " + line)
		}
	}
	if WorstSeverity(s.Findings) == SeverityFatal {
		return 1
	}
	return 0
}

// printProxyStatus 输出 proxy 模式的专属状态段。
//
// proxy 模式没有 forward.Plane（不转发裸包），也没有 mesh peer 参与数据传输，
// 所以不能用 packet 模式的模板渲染 —— 关键信息变成了
// 「有几个 agent 在线」和「用户态协议栈有没有真的把连接拦下来并中继成功」。
func printProxyStatus(s *StatusSnapshot) {
	a := s.Agents

	fmt.Println("出口：")
	if a.Count == 0 {
		fmt.Println("  （暂无 agent 接入）")
		if a.Listen != "" {
			fmt.Printf("  监听 %s   在出口节点执行: slx agent --gateway <本机IP:端口>\n", a.Listen)
		}
	} else {
		fmt.Printf("  已接入 %d 个   监听 %s\n", a.Count, orDash(a.Listen))
		if len(a.Details) > 0 {
			fmt.Printf("    %-14s %6s %8s %8s %6s  %s\n", "节点", "评分", "延迟", "带宽", "丢包", "状态")
			for _, d := range a.Details {
				state := "正常"
				switch {
				case d.Breaker == "熔断":
					state = "熔断"
					if d.BreakerReason != "" {
						// 把判定依据一起显示：只写「熔断」没人知道该查什么。
						state = "熔断（" + d.BreakerReason + "）"
					}
				case d.Breaker == "试探":
					state = "试探"
				case !d.Eligible:
					state = "不参与"
				case d.Note != "":
					state = d.Note
				}
				bw := fmt.Sprintf("%.1fM", d.BWMbps)
				if d.BWMbps <= 0 {
					bw = "-"
				}
				rtt := fmt.Sprintf("%.0fms", d.RTTMS)
				if d.RTTMS <= 0 {
					rtt = "-"
				}
				// 目标不可达次数单列：它不代表出口坏了，
				// 只是说明有这些用户请求的目标打不开（被墙 / 网站挂了）。
				if d.TargetFailures > 0 {
					state = fmt.Sprintf("%s（目标打不开 %d 次）", state, d.TargetFailures)
				}
				// 链路失败次数也单列：链路抖 ≠ 出口坏了，
				// 它不推进熔断计数，只是让人看得见链路在抖。
				if d.LinkFailures > 0 {
					state = fmt.Sprintf("%s（链路失败 %d 次）", state, d.LinkFailures)
				}
				fmt.Printf("    %-14s %6.1f %8s %8s %5.1f%%  %s\n",
					d.ID, d.Score, rtt, bw, d.Loss*100, state)
			}
		} else {
			for _, id := range a.IDs {
				fmt.Printf("    %s\n", id)
			}
		}
	}

	fmt.Println("\n分流：")
	if s.Split.Enabled {
		fmt.Printf("  已启用   接口 %s   国内网段 %d 条（%s，%s前更新）\n",
			strings.Join(s.Split.Ifaces, ","), s.Split.CNEntries,
			orDash(s.Split.CNSource), humanAge(time.Duration(s.Split.CNAgeSec)*time.Second))
	} else {
		fmt.Println("  未启用（境外流量走直连）")
	}

	if len(s.Channels) > 0 {
		fmt.Println("\n通道（relay）：")
		for _, c := range s.Channels {
			mark := " "
			if c.Primary {
				mark = "*"
			}
			state := ""
			if c.Open {
				state = "  熔断"
			}
			fmt.Printf("  %s %-12s %-22s 评分 %5.1f%s\n",
				mark, shortID(c.NodeID), c.Kind+" "+c.Remote, c.Score, state)
		}
		fmt.Println("  （* = 当前承载业务流量的主通道）")
	}
	if s.DNS.Enabled {
		fmt.Println("\nDNS：")
		fmt.Printf("  监听 %s   查询 %d 次（本地 %d / 经出口 %d / 失败 %d）\n",
			s.DNS.Addr, s.DNS.Queries, s.DNS.LocalHits, s.DNS.EgressHits, s.DNS.Failures)
		fmt.Printf("  缓存 命中 %d / 未命中 %d   条目 %d\n",
			s.DNS.CacheHits, s.DNS.CacheMisses, s.DNS.CacheEntries)
	}

	fmt.Println("\n代理数据面：")
	fmt.Printf("  TUN 收 %d 包 / 发 %d 包\n", a.TunRead, a.TunWrite)
	fmt.Printf("  TCP 接受 %d 条 / 失败 %d 条（在途 %d）    UDP 中继 %d 条 / 失败 %d 条\n",
		a.TCPAccepted, a.TCPFailed, a.TCPActive, a.UDPAccepted, a.UDPFailed)
	// ★ 失败要分类，不然看到一个总数根本不知道查哪
	if a.TCPFailed > 0 {
		fmt.Printf("  TCP 失败分类：出口连不上 %d / 端点建不起来 %d / 并发超限 %d\n",
			a.TCPFailConnect, a.TCPFailEndpoint, a.TCPFailLimit)
	}
	// ★ 字节数是判断「流量到底有没有真的走代理」最直接的证据
	if a.TCPBytes > 0 || a.UDPBytes > 0 {
		fmt.Printf("  中继字节：TCP %s / UDP %s\n",
			humanBytes(uint64(a.TCPBytes)), humanBytes(uint64(a.UDPBytes)))
	}
	if a.IcmpReplied > 0 || a.IcmpDropped > 0 {
		fmt.Printf("  ICMP echo 回复 %d 次", a.IcmpReplied)
		if a.IcmpDropped > 0 {
			fmt.Printf("（过载丢弃 %d 次）", a.IcmpDropped)
		}
		fmt.Println()
	}
	if a.TCPAccepted == 0 && a.UDPAccepted == 0 && a.TunRead > 0 {
		fmt.Println("  提示：有包进 TUN 但没有连接被接受，检查 agent 是否在线、分流规则是否完整")
	}
}

func humanCount(n uint64) string {
	if n < 10000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1f 万", float64(n)/1e4)
	}
	return fmt.Sprintf("%.2f 亿", float64(n)/1e8)
}

func humanBytes(n uint64) string {
	f := float64(n)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// Trace 实现 `slx trace <目标>`：把流量经过的每一跳摊开。
func Trace(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法：slx trace <域名或IP>")
		return 2
	}
	target := args[0]

	st, err := readStatusSnapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rs, _ := readRouteSnapshot()

	fmt.Printf("追踪 %s\n\n", target)

	// 1. 解析
	var ip net.IP
	if v := net.ParseIP(target); v != nil {
		ip = v
		fmt.Printf("  1. 目标 IP        %s（直接指定）\n", ip)
	} else {
		ips, rerr := net.LookupHost(target)
		if rerr != nil || len(ips) == 0 {
			fmt.Printf("  1. 解析           失败：%v\n", rerr)
			fmt.Println("\n结论：本机解析不出这个域名，后续无法判断。")
			fmt.Println("处理：确认本机 DNS 可用（`slx doctor` 会检查）")
			return 1
		}
		ip = net.ParseIP(ips[0])
		fmt.Printf("  1. 本机解析        %s → %s\n", target, ip)
	}

	// 2. 境内还是境外
	note := "境外（应走隧道）"
	if rs.Current != "" {
		note += "，当前出口 " + rs.Current
	}
	fmt.Printf("  2. 国内网段判定    %s\n", note)

	if st.Split.Enabled {
		fmt.Printf("  3. 分流规则        %s 上打标记 → 策略路由到 %s\n",
			strings.Join(st.Split.Ifaces, ","), st.TunName)
	} else {
		fmt.Printf("  3. 分流规则        未启用，直接走直连\n")
	}

	fmt.Printf("  4. 隧道            %s（内层 %s，MTU %d）\n", st.TunName, st.InnerIP, st.InnerMTU)

	if len(rs.Candidates) > 0 {
		fmt.Println("  5. 出口候选：")
		for _, c := range rs.Candidates {
			aff := ""
			if c.Affinity {
				aff = " [亲和]"
			}
			fmt.Printf("       %-18s %6.1f 分  %.0fms  %.1fM%s\n",
				c.NodeID, c.Score, c.RTTMS, c.BWMbps, aff)
		}
	} else {
		fmt.Println("  5. 出口候选        无")
	}

	if rs.Current != "" {
		fmt.Printf("  6. 出口 NAT        改写为内层地址后二次 NAT 出公网\n")
		fmt.Printf("  7. 回程            还原为目的地址 %s，转发回客户端\n", "客户端真实地址")
	}

	fmt.Println()
	if len(st.Findings) > 0 {
		fmt.Printf("注意：当前有 %d 个问题可能影响这条路径（`slx status` 查看）\n", len(st.Findings))
	} else {
		fmt.Println("结论：路径完整，各环节未见异常。")
	}
	return 0
}
