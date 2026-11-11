// Package picker 实现智能选路。
//
// # 为什么要两层测量
//
//	L1 网格质量：网关 ↔ 出口（ZeroTier 链路），测 RTT / 丢包 / 链路吞吐
//	L2 出口质量：出口 → 境外站点，测 RTT / 下载速率 / 失败率
//
// 只测 L1 不知道 VPS 到目标站快不快（VPS 自己的出口可能拥堵或被限速）；
// 只测 L2 不知道家到 VPS 的链路质量（可能丢包严重）。所以必须两层都要。
//
// 端到端有效指标：
//
//	rtt(D,E)  = L1_rtt(E) + L2_rtt(D,E)
//	bw(D,E)   = min(L1_bw(E), L2_bw(D,E))
//	loss(D,E) = 1 - (1 - L1_loss(E)) * (1 - L2_loss(D,E))
//
// 关于「木桶效应」：带宽取 min 而不是加权，因为端到端吞吐由最慢的一段决定；
// 丢包则取「至少丢一次」的并集概率。
package picker

import (
	"math"
	"sync"
	"time"
)

// EWMAAlpha 是平滑系数。
//
// 取 0.3 是在「跟得上变化」和「不被单次抖动带偏」之间的折中：
// 单次异常样本只影响 30%，连续 5 次的趋势就能明显体现出来。
const EWMAAlpha = 0.3

// historyLen 是保留的样本数，用于展示趋势。
const historyLen = 10

// EWMA 是带历史样本的指数加权移动平均。
//
// 并发安全：探测 goroutine 写入、选路 goroutine 读取，必须加锁。
type EWMA struct {
	mu      sync.RWMutex
	alpha   float64
	value   float64
	valid   bool
	samples [historyLen]float64
	n       int
	pos     int
}

// NewEWMA 创建 EWMA。alpha 为 0 时用默认值。
func NewEWMA(alpha float64) EWMA {
	if alpha <= 0 || alpha > 1 {
		alpha = EWMAAlpha
	}
	return EWMA{alpha: alpha}
}

// Update 加入一个新样本。
func (e *EWMA) Update(sample float64) {
	if math.IsNaN(sample) || math.IsInf(sample, 0) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// 零值 EWMA 也要能用：alpha 未初始化时补上默认值，
	// 否则 (1-alpha)=1 会让值永远停在首个样本上。
	if e.alpha <= 0 {
		e.alpha = EWMAAlpha
	}
	if !e.valid {
		e.value = sample
		e.valid = true
	} else {
		e.value = e.alpha*sample + (1-e.alpha)*e.value
	}
	e.samples[e.pos] = sample
	e.pos = (e.pos + 1) % historyLen
	if e.n < historyLen {
		e.n++
	}
}

// Get 返回当前 EWMA 值与是否有效。
func (e *EWMA) Get() (float64, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.value, e.valid
}

// Or 返回值；无效时返回 fallback。
func (e *EWMA) Or(fallback float64) float64 {
	if v, ok := e.Get(); ok {
		return v
	}
	return fallback
}

// History 返回最近样本（时间顺序）。
func (e *EWMA) History() []float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]float64, 0, e.n)
	start := (e.pos - e.n + historyLen) % historyLen
	for i := 0; i < e.n; i++ {
		out = append(out, e.samples[(start+i)%historyLen])
	}
	return out
}

// Reset 清空历史（出口地址变化或长时间失联后调用，避免旧数据污染）。
func (e *EWMA) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.value = 0
	e.valid = false
	e.n = 0
	e.pos = 0
}

// —— 质量画像 ——

// Quality 是某个 (目的地, 出口) 二元组的实测质量。
//
// 零值可用：所有 EWMA 都是从「无数据」状态开始，调用方用 Or() 取兜底值。
type Quality struct {
	L1RTT  EWMA // 网关 ↔ 出口 RTT（毫秒）
	L1Loss EWMA // 网关 ↔ 出口 丢包率（0~1）
	L1BW   EWMA // 网关 ↔ 出口 链路吞吐（Mbps）

	L2RTT  EWMA // 出口 → 目的地 RTT（毫秒）
	L2Loss EWMA // 出口 → 目的地 丢包率（0~1）
	L2BW   EWMA // 出口 → 目的地 下载速率（Mbps）

	// Affinity 表示这个目的地是否是经该出口解析出来的（CDN 亲和）。
	Affinity bool

	// Updated 是最后一次有效更新的时间。
	Updated time.Time
}

// 兜底值：还没有任何测量时使用，避免「零数据」被当成「零延迟、满带宽」。
const (
	fallbackRTTMS  = 200 // 保守：假设 200ms
	fallbackBWMbps = 5   // 保守：假设 5 Mbps
	fallbackLoss   = 0.5 // 保守：假设一半丢包
)

// Effective 汇总端到端有效指标。
func (q *Quality) Effective() (rttMS, bwMbps, loss float64) {
	l1rtt := q.L1RTT.Or(fallbackRTTMS)
	l2rtt := q.L2RTT.Or(fallbackRTTMS)
	// 未知的那一层不重复计入：如果 L2 从没测过，就只用 L1 的估计，
	// 否则会凭空多算一倍延迟，让「没有数据的出口」显得比有数据的更差。
	if !q.L2RTT.isValid() {
		l2rtt = 0
	}
	rttMS = l1rtt + l2rtt

	l1bw := q.L1BW.Or(fallbackBWMbps)
	l2bw := q.L2BW.Or(fallbackBWMbps)
	if !q.L2BW.isValid() {
		l2bw = math.Inf(1) // 未测则不构成瓶颈
	}
	bwMbps = math.Min(l1bw, l2bw)

	l1loss := q.L1Loss.Or(0)
	l2loss := q.L2Loss.Or(0)
	loss = 1 - (1-l1loss)*(1-l2loss)
	if loss < 0 {
		loss = 0
	}
	if loss > 1 {
		loss = 1
	}
	return rttMS, bwMbps, loss
}

// isValid 报告是否已有有效样本。
func (e *EWMA) isValid() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.valid
}

// —— 打分 ——

// Weights 是打分权重与归一化参考值。
type Weights struct {
	BW   float64 // 带宽权重
	RTT  float64 // 延迟权重
	Loss float64 // 丢包权重

	// Transport 是「通道健康度」维度的权重。
	//
	// 默认 0 = 不参与打分（现有行为完全不变）。
	//
	// 通道维度回答的是「这个出口当前走的那条通道健不健康」
	// （ZeroTier 的 UDP 路径丢包 2%、公网直连干净），
	// 与「出口本身能不能出网」（L1/L2）是两回事。
	//
	// 决策：先不开，代码留好。启用时给一个非零权重即可。
	Transport float64

	BRefMbps      float64 // 带宽软上限：达到它算满分
	RRefMS        float64 // 延迟软上限
	AffinityBonus float64 // CDN 亲和加成（分）
}

// DefaultWeights 是默认权重。
//
// 带宽 0.45 权重最高：用户感知最强烈的是「卡不卡」而不是「快几毫秒」。
// 延迟 0.35：影响交互响应。
// 丢包 0.20：丢包通常已经被 TCP 重传掩盖成"慢"，所以权重稍低，
// 但它仍是判「通不通」的关键信号。
func DefaultWeights() Weights {
	return Weights{
		BW: 0.45, RTT: 0.35, Loss: 0.20,
		BRefMbps: 50, RRefMS: 100,
		AffinityBonus: 15,
	}
}

// normalized 把权重归一化到总和为 1。
func (w Weights) normalized() Weights {
	sum := w.BW + w.RTT + w.Loss + w.Transport
	if sum <= 0 {
		return DefaultWeights()
	}
	out := w
	out.BW /= sum
	out.RTT /= sum
	out.Loss /= sum
	out.Transport /= sum
	if out.BRefMbps <= 0 {
		out.BRefMbps = 50
	}
	if out.RRefMS <= 0 {
		out.RRefMS = 100
	}
	return out
}

// Score 计算 0~100 的评分。
//
// 用软上限归一化而不是线性截断：
//
//	s_bw  = bw / (bw + B_ref)     带宽再高也只有渐进提升（50Mbps 时 0.5，100Mbps 时 0.67）
//	s_rtt = R_ref / (R_ref + rtt) 延迟越低越好，同样渐进
//
// 好处是极端值不会主导评分：一台 1Gbps 的机器不会因为带宽就把一台 10ms 的机器碾死。
func Score(rttMS, bwMbps, loss float64, affinity bool, w Weights) float64 {
	w = w.normalized()

	if rttMS < 0 {
		rttMS = 0
	}
	if bwMbps < 0 || math.IsNaN(bwMbps) {
		bwMbps = 0
	}
	if math.IsInf(bwMbps, 1) {
		// 未测出上限：按软上限满分处理，但略低于真实满分，
		// 避免「没数据」比「实测很好」还占优。
		bwMbps = w.BRefMbps
	}
	if loss < 0 {
		loss = 0
	}
	if loss > 1 {
		loss = 1
	}

	sBW := bwMbps / (bwMbps + w.BRefMbps)
	sRTT := w.RRefMS / (w.RRefMS + rttMS)
	sLoss := 1 - loss

	score := 100 * (w.BW*sBW + w.RTT*sRTT + w.Loss*sLoss)
	if affinity {
		score += w.AffinityBonus
	}
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score
}

// ScoreOf 直接对一份质量画像打分。
func ScoreOf(q *Quality, w Weights) float64 {
	rtt, bw, loss := q.Effective()
	return Score(rtt, bw, loss, q.Affinity, w)
}

// ScoreWithTransport 在 Score 的基础上多带一个「通道健康度」维度。
//
// transport 是 0~100 的通道分（relay 侧的通道评分喂进来）。
// 传 0（没有通道信息）或权重为 0 时，行为与 Score 完全一致。
//
// 通道分只做**加成**（最多 +20 分），不改原有三项的相对关系 ——
// 这样即便通道维度配错了，也不会把选路整体带偏。
//
// 为什么另起一个函数而不是改 Score 的签名：
// Score 被 picker_test.go 与多个调用点使用，改签名会波及一批既有测试。
// 旧函数保留（内部委托新函数，transport=0），既有测试一个都不用改。
func ScoreWithTransport(rttMS, bwMbps, loss float64, affinity bool, transport float64, w Weights) float64 {
	base := Score(rttMS, bwMbps, loss, affinity, w)
	if transport <= 0 || w.Transport <= 0 {
		return base
	}
	if transport > 100 {
		transport = 100
	}
	bonus := w.Transport * (transport / 100 * 20)
	if base+bonus > 100 {
		return 100
	}
	return base + bonus
}
