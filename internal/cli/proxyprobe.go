package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"slx/internal/logx"
	"slx/internal/picker"
	"slx/internal/relay"
)

// proxy 模式 L1 探测参数。
const (
	// proxProbeInterval 采样间隔。2 秒与 packet 模式的 L1 保持一致，
	// 让选路切换的灵敏度在两种模式下体感相同。
	proxProbeInterval = 2 * time.Second
	// proxProbeTimeout 单次采样超时。超过就记一次丢包，
	// 不阻塞其它 agent 的采样。
	//
	// 从 3 秒放宽到 6 秒：L1 每 2 秒跑一次，连续 5 次失败就熔断，
	// 3 秒超时在高延迟、高丢包的出口上极易连丢 5 次直接被判死。
	// 6 秒给了链路更多喘息空间，代价只是发现坏链路晚几秒。
	proxProbeTimeout = 6 * time.Second
	// proxLossWindow 丢包率统计窗口（最近 N 次采样）。
	//
	// 取 20 = 约 40 秒。太短会被抖动带偏，太长会掩盖正在恶化的链路。
	proxLossWindow = 20
)

// startProxyProbe 周期采样每个出口 agent 的 L1 链路质量并喂给选路器。
//
// proxy 模式下没有 mesh 探测可用（不转发裸包），所以 L1 改为在 relay 流上
// 做应用层 ping：这测到的是「网关到出口这条连接本身的往返」，
// 正是选路需要的口径。
func startProxyProbe(ctx context.Context, log *slog.Logger, gw *relay.Gateway, sel *picker.Selector, bw *bwTracker) {
	t := time.NewTicker(proxProbeInterval)
	defer t.Stop()

	windows := map[string][]bool{}
	lastGood := map[string]float64{} // 最近一次成功采样的 RTT，失败时沿用它

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			ids := gw.AgentIDs()

			// 带宽来自被动统计（真实中继流量的峰值），没有样本时传 0，
			// 选路器会用 fallbackBWMbps 兜底，不会被当成「零带宽」惩罚。
			if bw != nil {
				alive := make(map[string]bool, len(ids))
				for _, id := range ids {
					alive[id] = true
				}
				bw.prune(alive)
			}

			// 清掉已断开 agent 的状态，避免内存随重连次数增长
			if len(windows) > len(ids) {
				alive := make(map[string]bool, len(ids))
				for _, id := range ids {
					alive[id] = true
				}
				for id := range windows {
					if !alive[id] {
						delete(windows, id)
						delete(lastGood, id)
					}
				}
			}

			// 各 agent **并行**探测。
			//
			// 之前是串行：一台超时就要等满一个超时，
			// 6 台 agent 最坏一轮 36 秒 —— 而 ticker 只有 2 秒，
			// 于是实际探测周期被拉到十几秒，状态上的延迟/丢包全是旧账，
			// 熔断判定也跟着滞后。
			var wg sync.WaitGroup
			for _, id := range ids {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					probeL1Once(gw, sel, bw, log, id, now, windows, lastGood)
				}(id)
			}
			wg.Wait()
		}
	}
}

// shortID 截短 node_id 便于日志。
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// errStringSafe 把 nil error 渲染成 "-"。
func errStringSafe(err error) string {
	if err == nil {
		return "-"
	}
	return err.Error()
}

// —— L2 探测：出口能不能真的连到公网 ——
//
// L1 只测「网关 ↔ 出口」这一段（走 relay 控制流的 ping），
// 它测不出「出口 ↔ 互联网」的质量。实测踩到：某节点 L1 延迟 72ms、
// 评分排第一，但实际访问 google/youtube 全部失败；另一节点 L1 评分最低，
// 实际反而能用。只看 L1 必然选错。
//
// 做法：让出口对几个「全球可达且极少被墙」的地址做一次 TCP 443 连接
// （复用 PingHost —— 它在没有 ICMP 权限时会自动退化成 TCP 探测）。
// 结果直接喂给选路器的熔断器：连续失败的出口会被自动排除出候选集。
//
// 为什么不加新的协议消息：判断「能不能出网」不需要 HTTP 语义，
// TCP 443 能建连就足以区分「爱尔兰 0/8」和「韩国 5/8」这种差异。
const (
	// netProbeInterval 是 L2 探测周期。
	// 比 L1 慢得多：L1 要抓瞬时机遇，L2 关心的是分钟级的可用性。
	netProbeInterval = 90 * time.Second
	// netProbeTimeout 是单次探测超时。
	//
	// 从 6 秒放宽到 10 秒：高延迟 + 高丢包的出口 6 秒太紧，
	// 一次网络抖动就能把探测判成失败。放宽后误判明显下降，
	// 代价只是「坏出口被排除得晚一点」。
	netProbeTimeout = 10 * time.Second
)

// netProbeTargets 是 L2 探测目标。
//
// 选这三个的理由：
//   - 1.1.1.1 / 8.8.8.8：全球任播，境外出口基本都通；
//   - 223.5.5.5：国内出口的对照组，避免「出口只通国内」被误判为可用。
//
// 任一目标连通即认为出口能出网 —— 单个 IP 被挡不代表出口坏了。
var netProbeTargets = []string{"1.1.1.1", "8.8.8.8", "223.5.5.5"}

// startNetProbe 周期性检查每个出口的出网能力，结果驱动熔断器。
func startNetProbe(ctx context.Context, log *slog.Logger, gw *relay.Gateway, sel *picker.Selector) {
	go func() {
		// 启动后先等一会，让 L1 采样先跑起来
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}

		t := time.NewTicker(netProbeInterval)
		defer t.Stop()

		for {
			probeAllEgress(ctx, log, gw, sel)

			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// probeAllEgress 对每个在线出口做一轮 L2 探测。
//
// **按出口并行**：每个出口内部三个目标仍是串行（同一出口并发探测反而会
// 互相干扰），但出口之间并行 —— 串行时一轮最坏 3×10s×N，
// N=3 就接近 90 秒的探测周期了，探测频率被拖垮。
func probeAllEgress(ctx context.Context, log *slog.Logger, gw *relay.Gateway, sel *picker.Selector) {
	ids := gw.AgentIDs()
	if len(ids) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			probeOneEgress(ctx, log, gw, sel, id)
		}(id)
	}
	wg.Wait()
}

// probeOneEgress 对单个出口做一轮 L2 探测（三个目标依次试）。
func probeOneEgress(ctx context.Context, log *slog.Logger, gw *relay.Gateway,
	sel *picker.Selector, id string) {

	ok := false
	var lastErr error
	lastErrText := ""
	// reason 是这次失败的简短原因（给状态页显示用）。
	reason := ""
	for _, target := range netProbeTargets {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ip := net.ParseIP(target)
		if ip == nil {
			continue
		}
		reachable, err := gw.PingHost(id, ip, netProbeTimeout)
		if err != nil {
			lastErr = err
			lastErrText = err.Error()
			reason = probeFailureReason(target, err)
			continue
		}
		if !reachable {
			// 没报错但目标不应答：目标不可达（多半是被墙或出口断网）
			reason = "L2 目标 " + target + " 不可达"
			continue
		}
		ok = true
		reason = ""
		break
	}

	// 「该出口没有可用通道」是通道级熔断的结果，
	// 说明不了「这个出口连不上公网」⇒ 不推进 L2 熔断计数。
	if errors.Is(lastErr, relay.ErrNoUsableChannel) {
		log.Debug("L2 跳过：出口无可用通道",
			logx.FieldEvent, "probe.l2_skipped",
			logx.FieldNodeID, shortID(id))
		return
	}

	// 结果交给 **L2 专用**的熔断器：与 L1（网关↔出口链路）的计数完全独立。
	//
	// 独立是必须的：L1 每 2 秒成功一次就清零，若共用一个计数器，
	// L2 的失败永远攒不起来 ——「链路正常但连不上公网」的出口
	// 会一直留在候选集里（实测踩到：L1 最快的那台实际什么都打不开）。
	//
	// 上线不足 picker.MinEgressUptime 的出口，失败不计入熔断计数
	//（在选路器内部判定）—— 刚上线的出口最容易因抖动被误杀。
	now := time.Now()
	sel.ReportL2Health(id, ok, reason, now)

	if !ok {
		log.Warn("出口无法连到公网（L2 探测失败）",
			logx.FieldEvent, "probe.l2_failed",
			logx.FieldNodeID, id,
			logx.FieldErr, lastErrText,
			"reason", reason,
			"uptime", sel.Uptime(id, now).Round(time.Second).String(),
			logx.FieldHint, "该出口会被逐步排除出候选集；检查它的出网与防火墙")
	}
}

// probeFailureReason 把一次 L2 探测失败翻译成一句人能看懂的短原因。
//
// 例："L2 连不上 1.1.1.1（超时）"、"L2 连不上 8.8.8.8（连接被拒绝）"。
func probeFailureReason(target string, err error) string {
	if err == nil {
		return "L2 目标 " + target + " 不可达"
	}
	return "L2 连不上 " + target + "（" + probeErrPhrase(err) + "）"
}

// probeErrPhrase 把底层错误归成几个固定短语：超时 / 连接被拒绝 / 目标不可达 / 失败。
//
// 走字符串匹配而不是 errors.Is：这些错误是**对端 agent 传回来的文本**
// （经过 relay 中继），不是本机的 syscall.Errno，没有类型可断言。
func probeErrPhrase(err error) string {
	if err == nil {
		return "失败"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"),
		strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "超时"):
		return "超时"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "拒绝连接"):
		return "连接被拒绝"
	case strings.Contains(msg, "unreachable"), strings.Contains(msg, "no route to host"),
		strings.Contains(msg, "network is down"), strings.Contains(msg, "不可达"):
		return "目标不可达"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "dns"):
		return "域名解析失败"
	default:
		return "失败"
	}
}

// probeL1Once 对单个 agent 做一次 L1 采样并记账。
//
// # 「没有可用通道」不算链路丢包
//
// ErrNoUsableChannel 表示这台 agent 在线、但通道全被熔断了 ——
// 那是**通道级**的问题。记成丢包会立刻把 agent 级熔断也拖下水，
// 形成级联（出口被踢 → DNS 失败 → 更多熔断）。
// 所以这里直接跳过：不记丢包、不报告健康失败。
func probeL1Once(gw *relay.Gateway, sel *picker.Selector, bw *bwTracker,
	log *slog.Logger, id string, now time.Time,
	windows map[string][]bool, lastGood map[string]float64) {

	rtt, err := gw.Ping(id, proxProbeTimeout)
	ok := err == nil

	if errors.Is(err, relay.ErrNoUsableChannel) {
		// 该出口暂时没有可用通道（通道级熔断中）：不记丢包、不推进出口级熔断。
		log.Debug("L1 跳过：出口无可用通道",
			logx.FieldEvent, "probe.l1_skipped",
			logx.FieldNodeID, shortID(id))
		return
	}

	w := append(windows[id], ok)
	if len(w) > proxLossWindow {
		w = w[len(w)-proxLossWindow:]
	}
	windows[id] = w

	lost := 0
	for _, v := range w {
		if !v {
			lost++
		}
	}
	loss := float64(lost) / float64(len(w))

	rttMS := lastGood[id]
	if ok {
		rttMS = float64(rtt.Microseconds()) / 1000.0
		lastGood[id] = rttMS
	}
	// 失败时不把 0 传给 UpdateL1 —— EWMA 会被瞬间拉低，
	// 让一条刚抖一下的链路看起来「快到 0ms」，选路反而更偏爱它。
	bwMbps := 0.0
	if bw != nil {
		bwMbps = bw.Bandwidth(id)
	}
	sel.UpdateL1(id, rttMS, loss, bwMbps, now)
	// 失败原因一起带上：状态页上要能看出「为什么熔断」。
	reason := ""
	if !ok {
		reason = "L1 出口链路" + probeErrPhrase(err)
	}
	sel.ReportHealthWithReason(id, ok, reason, now)

	if !ok {
		log.Debug("L1 采样失败",
			logx.FieldEvent, "probe.l1_failed",
			logx.FieldNodeID, shortID(id),
			logx.FieldErr, errStringSafe(err))
	}
}
