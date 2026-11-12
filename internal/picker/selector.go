package picker

import (
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// 默认策略参数。
const (
	// DefaultMargin 是切换出口所需的评分优势。
	//
	// 8 分是「明显更好」的门槛：没有它，两个出口评分在噪声范围内来回抖动，
	// 用户会看到流反复断。宁可慢一点，也不要抖。
	DefaultMargin = 8.0

	// DefaultSwitchCooldown 是切换后的冷却期。
	DefaultSwitchCooldown = 30 * time.Second

	// 当前出口「已劣化」的硬阈值。
	//
	// 滞回与冷却的作用是「避免在健康出口之间来回跳」，
	// 不是为了死守一条已经烂掉的线路。
	// 实测踩到：韩国出口 617ms / 8.7% 丢包，只因比最优出口低 4.5 分
	// （不到 8 分门槛）就一直不切，用户看到的是 youtube 时通时不通。
	//
	// 这两个阈值用硬指标而不是评分，因为评分是加权综合值 ——
	// 带宽权重占 45%，一条烂线路可能因为「带宽估得高」而拿到不算太低的分。
	// 对用户来说，617ms + 8.7% 丢包就是「卡」，不看综合分。
	degradedLossRate = 0.05  // 5% 丢包
	degradedRTTMS    = 500.0 // 500ms
)

// Scored 是单个候选出口的打分明细（用于 explain）。
type Scored struct {
	NodeID   string  `json:"node_id"`
	Score    float64 `json:"score"`
	RTTMS    float64 `json:"rtt_ms"`
	BWMbps   float64 `json:"bw_mbps"`
	Loss     float64 `json:"loss"`
	Affinity bool    `json:"affinity"`
	Breaker  string  `json:"breaker"`
	// BreakerReason 是最近一次失败的简短原因（只在熔断/试探时展示）。
	// 例："L2 连不上 1.1.1.1（超时）"。
	BreakerReason string `json:"breaker_reason,omitempty"`
	// TargetFailures 是该出口累计的「用户目标不可达」次数。
	//
	// 它**不代表出口坏了**，只是说明有这么多用户请求的目标打不开。
	TargetFailures int    `json:"target_failures,omitempty"`
	Eligible       bool   `json:"eligible"`
	Note           string `json:"note,omitempty"`
}

// Decision 是一次选路的结果与依据。
type Decision struct {
	Egress string   `json:"egress"`
	Score  float64  `json:"score"`
	Source string   `json:"source"` // "路由表" / "实测择优" / "保持现状" / "兜底"
	Reason string   `json:"reason"`
	All    []Scored `json:"all,omitempty"`
}

// String 渲染成一行，便于日志。
func (d Decision) String() string {
	if d.Egress == "" {
		return "无可用出口"
	}
	return fmt.Sprintf("%s（%.1f 分，%s）", d.Egress, d.Score, d.Source)
}

// Selector 组合测量、熔断与路由表，负责「这个包走哪条路」。
//
// 并发：探测 goroutine 写入指标，数据面每包调用 Pick 读取。
// 用读写锁保护；Pick 是热路径，只做读锁 + 少量算术。
type Selector struct {
	mu sync.RWMutex

	weights  Weights
	margin   float64
	cooldown time.Duration

	// l1 是每个出口的网格质量（与目的地无关）。
	l1 map[string]*Quality
	// l2 是 (目的地, 出口) 的质量。
	l2 map[string]map[string]*Quality // destKey → egressID → Quality

	// transport 是每个出口当前的通道健康度（0~100）。
	//
	// 由 relay 侧的通道评分周期性喂进来。
	// 默认权重为 0 ⇒ 不参与打分 ⇒ 现有行为一字不变（决策：先不开，代码留好）。
	transport map[string]float64

	// breakers 是每个出口的 L1 熔断器（网关 ↔ 出口链路）。
	breakers map[string]*Breaker
	// gates 是每个出口的健康判定门 ✅✅
	//
	// 作用：把高频采样翻译成低频抗抖的判定 ✅✅
	// ⇒ 熔断不再由「连续 5 次采样失败（10 秒）」触发 ✅✅
	// ⇒ 而是由「连续 3 次判定失败（90 秒）」触发 ✅✅
	// 详见 healthgate.go ✅✅
	gates map[string]*HealthGate
	// l2Breakers 是每个出口的 L2 熔断器（出口 ↔ 公网）。
	//
	// 与 L1 **完全独立**：L1 成功不清 L2 的失败计数，反之亦然。
	// 见 l2BreakerThreshold 的说明。
	l2Breakers map[string]*Breaker
	// targetFailures 记录每个出口累计的「用户目标不可达」次数。
	//
	// 它**不参与任何判定**（尤其不推进熔断）：用户访问打不开的站点
	// 与「出口自己坏了」是两回事，混在一起会误杀唯一那台出口。
	targetFailures map[string]int
	table          *RouteTable

	// onlineSince 记录每个出口的接入时刻。
	//
	// 用途：最小存活时长保护（见 MinEgressUptime）——
	// 刚上线的出口不因探测抖动被熔断。
	// 没有登记过的出口（比如 packet 模式下的对等节点）不受这条保护。
	onlineSince map[string]time.Time

	// current 是当前选中的出口，用于滞回判断。
	current    string
	lastSwitch time.Time
}

// NewSelector 创建选路器。
func NewSelector(weights Weights) *Selector {
	return &Selector{
		weights:        weights.normalized(),
		margin:         DefaultMargin,
		cooldown:       DefaultSwitchCooldown,
		l1:             make(map[string]*Quality, 8),
		l2:             make(map[string]map[string]*Quality, 32),
		breakers:       make(map[string]*Breaker, 8),
		gates:          make(map[string]*HealthGate, 8),
		l2Breakers:     make(map[string]*Breaker, 8),
		targetFailures: make(map[string]int, 8),
		table:          NewRouteTable(),
		onlineSince:    make(map[string]time.Time, 8),
	}
}

// Table 暴露路由表（诊断用）。
func (s *Selector) Table() *RouteTable { return s.table }

// SetMargin 调整切换门槛（诊断/测试用）。
func (s *Selector) SetMargin(m float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.margin = m
}

// UpdateL1 记录一次网格层测量。
func (s *Selector) UpdateL1(egressID string, rttMS, lossRate, bwMbps float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.l1Locked(egressID)
	q.L1RTT.Update(rttMS)
	q.L1Loss.Update(lossRate)
	if bwMbps > 0 {
		q.L1BW.Update(bwMbps)
	}
	q.Updated = now
}

// UpdateL2 记录一次出口层测量。
//
// destKey 一般是域名的注册域（如 "googlevideo.com"），
// 用域名而不是 IP，因为 CDN 的 IP 会变、而域名代表用户的实际意图。
func (s *Selector) UpdateL2(egressID, destKey string, rttMS, lossRate, bwMbps float64, affinity bool, now time.Time) {
	if destKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.l2Locked(destKey, egressID)
	q.L2RTT.Update(rttMS)
	if lossRate >= 0 {
		q.L2Loss.Update(lossRate)
	}
	if bwMbps > 0 {
		q.L2BW.Update(bwMbps)
	}
	if affinity {
		q.Affinity = true
	}
	q.Updated = now
}

// MarkAffinity 标记「这个域名是经该出口解析的」。
//
// 用途：CDN 会让离得近的节点返回离得近的地址，
// 既然解析走的是这个出口，流量也走同一个出口才不会绕路。
func (s *Selector) MarkAffinity(egressID, destKey string) {
	if egressID == "" || destKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.l2Locked(destKey, egressID)
	q.Affinity = true
	q.Updated = time.Now()
}

// ReportHealth 把一次健康检查结果喂给熔断器。
func (s *Selector) ReportHealth(egressID string, ok bool, now time.Time) {
	s.ReportHealthWithReason(egressID, ok, "", now)
}

// ReportHealthWithReason 喂一次健康检查结果，并记录失败原因（用于状态展示）。
//
// # ★ 抗抖：采样与判定是两层 ✅✅
//
// 高频的探测采样（每 2 秒一次）与低频的熔断判定（每 30 秒一次）是分开的 ✅✅
//
//   - 每次调用先记一次采样 ✅✅
//   - **只有到了判定时点**才把结果报给熔断器 ✅✅
//   - 判定成功 ⇒ 报一次成功（清零失败计数 ✅✅）
//   - 连续 3 次判定失败 ⇒ 报一次失败 ⇒ 熔断 ✅✅
//
// ⇒ 判定耗时 90 秒，而不是「连续 5 次采样失败」的 10 秒 ✅✅
// ⇒ 单次抖动不再能熔断出口 ✅✅（这正是频繁切换的根源 ✅⚠️）
//
// # 最小存活时长保护
//
// 上线不足 MinEgressUptime 的出口，失败**不计入**熔断计数。
//
// 为什么要有这条：出口刚上线的那几分钟最容易抖 —— 隧道还在预热、
// 第一个探测流要重建、TCP 慢启动没建好。此时连续几次失败就把
// （往往也是唯一一台）出口熔断掉，等于「刚回来又熔断」，
// 正是线上「节点频繁熔断、网络基本不可用」的形态。
//
// 没有登记上线时刻的出口不受这条保护（行为与改动前完全一致）。
//
// # 失败原因
//
// reason 只是一句展示用的短语（例："L2 连不上 1.1.1.1（超时）"），
// 判定逻辑只看连续失败次数。
func (s *Selector) ReportHealthWithReason(egressID string, ok bool, reason string, now time.Time) {
	if egressID == "" {
		return
	}
	s.mu.Lock()
	if !ok && s.inGraceLocked(egressID, now) {
		// 还在最小存活时长内：这次失败不计入熔断计数。
		s.mu.Unlock()
		return
	}
	gate := s.gateLocked(egressID)
	b := s.breakerLocked(egressID)
	wasBroken := false
	if st, _ := b.State(now); st == "熔断" {
		wasBroken = true
	}
	s.mu.Unlock()

	// ★ 采样先记进门 ✅✅
	judged, healthy, tripped, _ := gate.Record(ok, now)
	if !judged {
		// 还没到判定时点 ⇒ 只记采样，**不推进**熔断计数 ✅✅
		return
	}

	// ★ 到判定点了 ⇒ 把**判定结果**报给熔断器 ✅✅
	//
	// ★ 注意区分两个概念 ✅✅：
	//   healthy=false（这次判定认为出口不健康）✅✅
	//   tripped（连续判定失败够次数了，该熔断了）✅✅
	//
	// ★ 曾经的错误 ✅⚠️：把 tripped 当成判定结果报上去 ✅⚠️
	//   ⇒ 第一次判定失败时 tripped=false ⇒ 报的是「成功」✅⚠️
	//   ⇒ 失败原因永远记不上、失败计数永远清零 ✅⚠️
	//   ⇒ 熔断器形同虚设 ✅⚠️
	//
	if !healthy {
		// 判定不健康 ⇒ 报一次失败 ✅✅
		// （熔断器自己的阈值是 1 ⇒ 一次失败就熔断 ✅✅）
		b.RecordWithReason(false, reason, now)
		if tripped && !wasBroken {
			// 出口刚进入熔断：让它的路由表条目立即失效
			s.table.Invalidate(egressID)
		}
		return
	}
	// 判定健康 ⇒ 报一次成功，清零失败计数 ✅✅
	b.RecordWithReason(true, "", now)
}

// ReportTargetFailure 记录一次「用户目标不可达」。
//
// # 与 ReportHealth 的根本区别
//
// 这里**不推进熔断计数**，也不影响任何判定。
//
// 用户访问打不开的站点（被墙、网站挂了、DNS 解析失败）是常态，
// 与「出口自己坏了」完全是两回事。混在一起的话，连续几次用户建连失败
// 就能把（往往也是唯一那台）出口熔断掉，表现为
// 「频繁熔断 + 网络基本不可用」—— 这正是线上遇到的主因之一。
//
// 这里只留一个计数，供状态页与日志观察。
func (s *Selector) ReportTargetFailure(egressID string) {
	if egressID == "" {
		return
	}
	s.mu.Lock()
	s.targetFailures[egressID]++
	s.mu.Unlock()
}

// TargetFailures 返回该出口累计的「目标不可达」次数。
//
// 这个计数**不代表出口坏了**，只说明「有这么多次用户请求的目标打不开」。
func (s *Selector) TargetFailures(egressID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.targetFailures[egressID]
}

// ReportL2Health 喂一次 L2（出口 ↔ 公网）探测结果。
//
// # 与 L1 完全独立
//
// L1（网关↔出口链路）每 2 秒一次，L2（出口↔公网）每 90 秒一次，
// 节奏差 45 倍。两者混用一个计数器时，L2 的失败会被下一次 L1 成功清掉 ——
// 于是 L2 永远无法单独把出口熔断，「链路正常但连不上公网」的出口
// 会一直留在候选集里（实测踩到过：L1 最快的那台实际什么都打不开）。
//
// 所以这里用**独立**的熔断器与阈值（见 l2BreakerThreshold）：
// L1 成功不会清掉 L2 的失败计数，反之亦然。
//
// 最小存活时长保护同样适用（见 ReportHealthWithReason）。
func (s *Selector) ReportL2Health(egressID string, ok bool, reason string, now time.Time) {
	s.mu.Lock()
	if !ok && s.inGraceLocked(egressID, now) {
		// 还在最小存活时长内：这次失败不计入熔断计数。
		s.mu.Unlock()
		return
	}
	b := s.l2BreakerLocked(egressID)
	wasBroken := false
	if st, _ := b.State(now); st == "熔断" {
		wasBroken = true
	}
	s.mu.Unlock()

	b.RecordWithReason(ok, reason, now)

	if !ok && wasBroken {
		// 出口刚进入熔断：让它的路由表条目立即失效
		s.table.Invalidate(egressID)
	}
}

// BreakerOf 返回该出口的 L1 熔断器（没有则 nil）。
//
// 供测试与诊断使用：看一个出口到底有没有被熔断、熔断到什么状态。
func (s *Selector) BreakerOf(egressID string) *Breaker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.breakers[egressID]
}

// L2BreakerOf 返回该出口的 L2 熔断器（没有则 nil）。
func (s *Selector) L2BreakerOf(egressID string) *Breaker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.l2Breakers[egressID]
}

// MarkOnline 记录出口的接入时刻。
//
// 由 agent 接入事件驱动。用途：最小存活时长保护 ——
// 上线不久就连续失败的出口先不熔断，给隧道预热留出时间。
func (s *Selector) MarkOnline(egressID string, now time.Time) {
	if egressID == "" {
		return
	}
	s.mu.Lock()
	s.onlineSince[egressID] = now
	s.mu.Unlock()
}

// OnlineSince 返回出口的接入时刻（没登记过返回 false）。
func (s *Selector) OnlineSince(egressID string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.onlineSince[egressID]
	return t, ok
}

// Uptime 返回出口已经在线多久（没登记过返回 0）。
func (s *Selector) Uptime(egressID string, now time.Time) time.Duration {
	since, ok := s.OnlineSince(egressID)
	if !ok {
		return 0
	}
	return now.Sub(since)
}

// inGraceLocked 判断该出口是否还在「最小存活时长」保护期内。
//
// 调用方必须持有 s.mu。
func (s *Selector) inGraceLocked(egressID string, now time.Time) bool {
	since, ok := s.onlineSince[egressID]
	if !ok {
		return false // 没登记上线时刻 → 不保护（保持改动前的行为）
	}
	return now.Sub(since) < MinEgressUptime
}

// ResetEgress 把某个出口的全部状态复位：熔断器、测量指标、路由表条目、上线时刻。
//
// # 什么时候用
//
// 出口（agent）掉线或重新接入时。
//
// # 为什么必须连熔断器一起清
//
// 熔断一旦打开就带冷却期（60s 起，指数退避到 10 分钟封顶），
// 而熔断状态是按 node_id 记的 —— agent 掉线再回来，node_id 不会变，
// 于是它带着最长 10 分钟的冷却期回来，继续被排除在候选集之外。
// 表现就是「刚回来又熔断」，而且退避越滚越长，出口永远回不来。
//
// 掉线的出口本来就不在候选集里（候选集来自在线 agent 列表），
// 所以清掉它的状态不会损失任何信息。
func (s *Selector) ResetEgress(egressID string) {
	s.mu.Lock()
	// 熔断器整个删掉：下次用到时新建，状态回到 closed。
	delete(s.breakers, egressID)
	delete(s.l2Breakers, egressID)
	// ★ 判定门也要清 ✅✅
	// ⇒ 否则它带着「连续判定失败 2 次」的状态回来 ✅⚠️
	// ⇒ 一次采样就凑够 3 次 ⇒ 刚上线就熔断 ✅⚠️
	// ⇒ 这正是「刚回来又熔断」的形态 ✅⚠️
	delete(s.gates, egressID)
	delete(s.targetFailures, egressID)
	delete(s.l1, egressID)
	for dest, m := range s.l2 {
		delete(m, egressID)
		if len(m) == 0 {
			delete(s.l2, dest)
		}
	}
	delete(s.onlineSince, egressID)
	if s.current == egressID {
		s.current = ""
	}
	s.mu.Unlock()
	s.table.Invalidate(egressID)
}

// InvalidateEgress 让某个出口的路由缓存与指标失效（出口下线时调用）。
//
// 与 ResetEgress 等价：出口既然下线了，它的熔断器也必须一起清掉
// —— 否则它带着冷却期回来会被继续排除（详见 ResetEgress 的说明）。
//
// 保留这个方法名是为了兼容既有调用点与测试。
func (s *Selector) InvalidateEgress(egressID string) {
	s.ResetEgress(egressID)
}

// Pick 为目的地选择一个出口。
//
// candidates 是当前健康、可用的出口节点 ID 列表（由调用方从对等表取）。
// destKey 是目的地域名（可为空，为空时只看 L1 质量）。
//
// 本方法不做网络 IO，可在数据面每包调用。
func (s *Selector) Pick(dst net.IP, destKey string, candidates []string, now time.Time) Decision {
	return s.pick(dst, destKey, candidates, now, true)
}

// pick 是 Pick 的实现。commit=false 时不写路由表、不改变当前出口（供 Explain 使用）。
func (s *Selector) pick(dst net.IP, destKey string, candidates []string, now time.Time, commit bool) Decision {
	if len(candidates) == 0 {
		return Decision{Source: "兜底", Reason: "没有可用出口"}
	}

	s.mu.RLock()
	if len(candidates) == 1 && candidates[0] == s.current {
		// 只有一个候选且就是当前用的，快速返回，连打分都不必
		d := Decision{Egress: candidates[0], Source: "保持现状", Reason: "唯一可用出口"}
		s.mu.RUnlock()
		return d
	}
	weights := s.weights
	margin := s.margin
	cooldown := s.cooldown
	cur := s.current
	curScore := s.scoreForLocked(cur, destKey)
	lastSwitch := s.lastSwitch

	// 当前出口是否已明显劣化（硬指标，不看综合评分）。
	// 劣化时下面的滞回门槛、路由缓存、切换冷却全部让路 ——
	// 该切就切，防抖不能变成「守着烂线路不动」。
	curRTT, _, curLoss, _, curHas := s.metricsForLocked(cur, destKey)
	curDegraded := cur != "" && curHas &&
		(curLoss > degradedLossRate || curRTT > degradedRTTMS)

	// 逐个打分
	all := make([]Scored, 0, len(candidates))
	best, bestScore := "", -1.0
	for _, id := range candidates {
		rtt, bw, loss, aff, has := s.metricsForLocked(id, destKey)
		// 通道健康度：权重默认 0 ⇒ 与改造前完全一致。
		tscore := s.transport[id]
		var sc float64
		if has {
			sc = ScoreWithTransport(rtt, bw, loss, aff, tscore, weights)
		} else {
			rtt, bw, loss = fallbackRTTMS, fallbackBWMbps, fallbackLoss
			sc = ScoreWithTransport(rtt, bw, loss, false, tscore, weights)
		}

		br := s.breakers[id]
		br2 := s.l2Breakers[id]
		view := s.breakerViewLocked(id, now)
		brName := view.name
		brReason := view.reason
		eligible := true
		if brName == "熔断" {
			eligible = false
		}
		// 熔断器的 Allow 才是有状态的判断，State 只用于展示。
		// L1 与 L2 任一不放行，这个出口就不可用。
		if br != nil && !br.Allow(now) {
			eligible = false
		}
		if br2 != nil && !br2.Allow(now) {
			eligible = false
		}

		st := Scored{
			NodeID: id, Score: round1(sc), RTTMS: round1(rtt), BWMbps: round1(bw),
			Loss: round3(loss), Affinity: aff, Breaker: brName,
			BreakerReason: brReason, Eligible: eligible,
			TargetFailures: s.targetFailures[id],
		}
		all = append(all, st)

		if eligible && sc > bestScore {
			best, bestScore = id, sc
		}
	}
	s.mu.RUnlock()

	if best == "" {
		// 全部熔断：退化到「最好的那个不健康出口」，总比完全不通强
		sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
		if len(all) > 0 {
			return Decision{
				Egress: all[0].NodeID, Score: all[0].Score,
				Source: "兜底", Reason: "全部出口熔断，退化选择评分最高者",
				All: all,
			}
		}
		return Decision{Source: "兜底", Reason: "无可用出口", All: all}
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })

	// —— 路由表优先 ——
	// 如果这个目的地精确匹配过某条已生效的选择，就直接用，
	// 保证同一站点在一段时间内走同一条路（CDN 亲和 + 避免抖动）。
	//
	// 但缓存不能压过明显更好的新选择：只有「缓存分数仍具竞争力」
	// （差距在 margin 之内）时才沿用，否则说明情况已经变了，该重新选。
	if dst != nil {
		if eg, bits, _, ok := s.table.Lookup(dst, now); ok {
			// 关键：用**当前实测分数**重新评估缓存里的出口，
			// 而不是用建表时记下的旧分数。
			// 旧分数会让一个已经劣化的出口看起来仍然有竞争力，
			// 导致永远不切换 —— 实测踩到过。
			fresh, freshEligible := -1.0, false
			for i := range all {
				if all[i].NodeID == eg {
					fresh = all[i].Score
					freshEligible = all[i].Eligible
					break
				}
			}
			// 当前出口已劣化时不再沿用缓存 —— 缓存的意义是「保持稳定」，
			// 而不是把一个坏选择继续锁 300 秒。
			if freshEligible && !curDegraded && (eg == best || fresh+margin >= bestScore) {
				return Decision{
					Egress: eg, Score: fresh,
					Source: "路由表",
					Reason: fmt.Sprintf("命中 /%d 缓存，当前实测 %.1f 分", bits, fresh),
					All:    all,
				}
			}
		}
	}

	// —— 保持现状（滞回）——
	// curDegraded 时整段跳过：宁可多切一次，也不要让用户卡在烂线路上。
	if cur != "" && cur != best && curScore > 0 && !curDegraded {
		if bestScore <= curScore+margin {
			return Decision{
				Egress: cur, Score: curScore,
				Source: "保持现状",
				Reason: fmt.Sprintf("新出口 %.1f 分未超过当前 %.1f 分 + %.1f 门槛",
					bestScore, curScore, margin),
				All: all,
			}
		}
		if now.Sub(lastSwitch) < cooldown {
			return Decision{
				Egress: cur, Score: curScore,
				Source: "保持现状",
				Reason: fmt.Sprintf("切换冷却中（还剩 %s）", cooldown-now.Sub(lastSwitch)),
				All:    all,
			}
		}
	}

	reason := "实测评分最高"
	if cur == "" {
		reason = "首次选路，实测评分最高"
	} else if curDegraded && cur != best {
		reason = fmt.Sprintf("当前出口已劣化（%.0fms / %.1f%% 丢包），切换到 %.1f 分的最优出口",
			curRTT, curLoss*100, bestScore)
	} else if cur != best {
		reason = fmt.Sprintf("新出口 %.1f 分超过当前 %.1f 分 + %.1f 门槛，执行切换",
			bestScore, curScore, margin)
	}

	// 记录结果，供后续查询（dry-run 时不写）
	if commit {
		if dst != nil {
			s.table.Set(dst.String()+"/32", best, bestScore, now)
		}
		if best != cur {
			s.mu.Lock()
			s.current = best
			s.lastSwitch = now
			s.mu.Unlock()
		}
	}

	return Decision{Egress: best, Score: bestScore, Source: "实测择优", Reason: reason, All: all}
}

// Explain 返回某个目的地的完整选路依据（只计算，不改变任何状态）。
func (s *Selector) Explain(dst net.IP, destKey string, candidates []string, now time.Time) Decision {
	return s.pick(dst, destKey, candidates, now, false)
}

// Current 返回当前选中的出口。
func (s *Selector) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// —— 内部 ——

func (s *Selector) l1Locked(egressID string) *Quality {
	q := s.l1[egressID]
	if q == nil {
		q = &Quality{}
		s.l1[egressID] = q
	}
	return q
}

func (s *Selector) l2Locked(destKey, egressID string) *Quality {
	m := s.l2[destKey]
	if m == nil {
		m = make(map[string]*Quality, 4)
		s.l2[destKey] = m
	}
	q := m[egressID]
	if q == nil {
		q = &Quality{}
		m[egressID] = q
	}
	return q
}

func (s *Selector) breakerLocked(egressID string) *Breaker {
	b := s.breakers[egressID]
	if b == nil {
		// ★ 阈值是 1 ✅✅
		//
		// 因为抗抖已经交给 HealthGate 做了 ✅✅
		// ⇒ 熔断器收到一次「判定失败」就该立刻熔断 ✅✅
		// ⇒ 再让它自己累计 5 次，等于把判定时间又拉长 5 倍 ✅⚠️
		//
		// ⇒ 实际熔断耗时 = HealthGate 的判定间隔 × 判定次数
		//   = 30 秒 × 3 = 90 秒 ✅✅
		b = NewBreakerWithThreshold(l1BreakerThreshold)
		s.breakers[egressID] = b
	}
	return b
}

// gateLocked 返回该出口的健康判定门 ✅✅
//
// 必须是调用方持锁 ✅✅
func (s *Selector) gateLocked(egressID string) *HealthGate {
	g := s.gates[egressID]
	if g == nil {
		g = NewHealthGate()
		s.gates[egressID] = g
	}
	return g
}

func (s *Selector) l2BreakerLocked(egressID string) *Breaker {
	b := s.l2Breakers[egressID]
	if b == nil {
		b = NewBreakerWithThreshold(l2BreakerThreshold)
		s.l2Breakers[egressID] = b
	}
	return b
}

// breakerView 是某个出口的熔断视图（把 L1 与 L2 两套计数汇总起来）。
type breakerView struct {
	// name 是 正常 / 熔断 / 试探。
	name string
	// reason 是判定依据（异常状态下是各来源的原因，逗号分隔）。
	reason string
	// until 是恢复时间（熔断中才有意义）。
	until time.Time
}

// breakerViewLocked 汇总该出口的熔断状态。
//
// L1（网关↔出口链路）与 L2（出口↔公网）是两套独立计数，
// 但对使用者来说是一个「这个出口能不能用」的结论，所以在这里合并：
// 任一套熔断 → 整体不可用；两套都正常 → 正常。
//
// 调用方必须持有 s.mu（读或写锁）。
func (s *Selector) breakerViewLocked(egressID string, now time.Time) breakerView {
	v := breakerView{name: "正常"}
	var reasons []string

	for _, b := range []*Breaker{s.breakers[egressID], s.l2Breakers[egressID]} {
		if b == nil {
			continue
		}
		name, until := b.State(now)
		switch name {
		case "熔断":
			if v.name != "熔断" {
				v.name = "熔断"
				v.until = until
			} else if until.After(v.until) {
				v.until = until
			}
		case "试探":
			if v.name == "正常" {
				v.name = "试探"
			}
		}
		if name != "正常" {
			if r := b.Reason(); r != "" {
				reasons = append(reasons, r)
			}
		}
	}

	v.reason = strings.Join(reasons, "；")
	if v.reason == "" {
		// 都没熔断，但可能最近失败过：取一条原因出来做备注。
		for _, b := range []*Breaker{s.l2Breakers[egressID], s.breakers[egressID]} {
			if b == nil {
				continue
			}
			if r := b.Reason(); r != "" {
				v.reason = r
				break
			}
		}
	}
	return v
}

// metricsForLocked 把 L1（按出口）与 L2（按目的地+出口）合成端到端有效指标。
//
// 注意**不构造 Quality 副本** —— EWMA 内含互斥锁，复制结构体会触发 vet 告警，
// 而且复制出来的锁是无效的。这里只读值。
func (s *Selector) metricsForLocked(egressID, destKey string) (rttMS, bwMbps, loss float64, affinity, has bool) {
	var l1, l2 *Quality
	l1 = s.l1[egressID]
	if destKey != "" {
		if m := s.l2[destKey]; m != nil {
			l2 = m[egressID]
		}
	}
	if l1 == nil && l2 == nil {
		return 0, 0, 0, false, false
	}

	// RTT：两层相加；未测过的那层不重复计入
	rttMS = 0
	if l1 != nil {
		rttMS += l1.L1RTT.Or(fallbackRTTMS)
	} else {
		rttMS += fallbackRTTMS
	}
	if l2 != nil {
		if v, ok := l2.L2RTT.Get(); ok {
			rttMS += v
		}
	}

	// 带宽：取最小值（木桶效应）
	bwMbps = fallbackBWMbps
	if l1 != nil {
		bwMbps = l1.L1BW.Or(fallbackBWMbps)
	}
	if l2 != nil {
		if v, ok := l2.L2BW.Get(); ok && v > 0 {
			if v < bwMbps {
				bwMbps = v
			}
		}
	}

	// 丢包：两层「至少丢一次」的并集概率
	l1loss, l2loss := 0.0, 0.0
	if l1 != nil {
		l1loss = l1.L1Loss.Or(0)
	}
	if l2 != nil {
		l2loss = l2.L2Loss.Or(0)
	}
	loss = 1 - (1-l1loss)*(1-l2loss)
	if loss < 0 {
		loss = 0
	}
	if loss > 1 {
		loss = 1
	}

	if l2 != nil {
		affinity = l2.Affinity
	}
	return rttMS, bwMbps, loss, affinity, true
}

func (s *Selector) scoreForLocked(egressID, destKey string) float64 {
	if egressID == "" {
		return 0
	}
	rtt, bw, loss, aff, has := s.metricsForLocked(egressID, destKey)
	if !has {
		return 0
	}
	return Score(rtt, bw, loss, aff, s.weights)
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// UpdateTransport 记录某个出口当前的通道健康度（0~100）。
//
// 由 relay 侧的通道探测循环调用。
//
// 传 0 表示「通道不可用」—— 选路时该出口会被降权，
// 但**不会被熔断**（熔断是 relay 通道级与 picker agent 级各自的事，不要混）。
//
// 默认权重为 0 ⇒ 这份数据当前不影响选路结果，只是先接好线。
func (s *Selector) UpdateTransport(egressID string, score float64) {
	if egressID == "" {
		return
	}
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return
	}
	s.mu.Lock()
	if s.transport == nil {
		s.transport = make(map[string]float64, 8)
	}
	s.transport[egressID] = score
	s.mu.Unlock()
}

// TransportOf 返回某个出口的通道健康度（没有则返回 0）。
func (s *Selector) TransportOf(egressID string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transport[egressID]
}
