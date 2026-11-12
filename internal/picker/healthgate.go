package picker

import (
	"sync"
	"time"
)

// —— 健康判定门 ——
//
// 把「高频探测采样」翻译成「低频、抗抖的熔断判定」。
//
// # 为什么要拆成两层
//
// 原来的做法是：探测每 2 秒失败一次就直接推进熔断计数，
// 连续 5 次失败（= 10 秒）就熔断。
//
// **这个窗口太短了 **：
//
//   - 跨境链路的 RTT 抖动、ZeroTier 隧道的瞬时重传，
//     连续几次 ping 失败是常态而不是故障
//   - 一次 agent 重启会在 10 秒内产生一批 relay 层错误
//     ⇒ 出口被误判为坏 ⇒ 熔断 ⇒ 流量全压到别的出口 ⇒ 可能连锁
//   - 这正是「频繁熔断 + 网络基本不可用」的形态
//
// # 现在的做法
//
//	采样：仍然每 2 秒一次
//	      （选路器需要新鲜的 RTT 与丢包数据，否则延迟指标会陈旧 ）
//	判定：每 30 秒做一次 ，看最近一个窗口内的采样成功率
//	      成功率 ≥ 70% ⇒ 判定成功
//	      成功率 < 70% ⇒ 判定失败
//	熔断：连续 3 次判定失败（= 90 秒）才熔断
//
// ⇒ 判定耗时从 10 秒变成 90 秒，抗抖动能力提升 9 倍
// ⇒ 而真正断掉的出口，90 秒内也会被排除出候选集
//
//	（这期间直连通道还在顶上，可接受 ）
//
// # 为什么不需要「硬失败快速隔离」
//
// 曾经的顾虑：agent 被 kill 时要不要等 90 秒？
//
// 答案是不需要 ：**agent 掉线时它自己就从候选集里消失了**
// （候选集来自在线 agent 列表 ）⇒ 不需要熔断器来处理这件事
//
// ⇒ 所以判定节奏恒定即可
// ⇒ 曾经实现过「窗口全失败就立刻判定」的逃生门
//
//	⇒ 结果是每次全失败都判定一次 ⇒ 熔断反而更快 ⇒ **抗抖完全失效**
//	⇒ 已删除
//
// 判定参数
//
// ★ 用 var 而不是 const ：测试需要把间隔调短
//
//	（否则每个测试都要把时间线拖到 90 秒 ）
//	生产代码不要改这三个值 —— 要调就改这里
var (
	// healthJudgeInterval 是健康判定的间隔（秒）。
	healthJudgeInterval = 30 * time.Second
	// healthJudgeFailThreshold 是连续多少次判定失败才熔断。
	healthJudgeFailThreshold = 3
	// healthJudgeMinRatio 是判定成功所需的最低成功率。
	//
	// ★ 为什么是 0.7 而不是 0.5 ：
	//   30 秒窗口在 2 秒采样下是 15 个样本
	//   50% 意味着「15 个里挂了 7 个」也算健康  —— 那基本等于半残
	//   70% ⇒ 至少 11 个成功才算健康
	healthJudgeMinRatio = 0.7
	// healthGateBufferSize 是采样条数的上限（只是兜底，正常靠时间剪枝 ）
	healthGateBufferSize = 256
	// l1BreakerThreshold 是 L1 熔断器需要的连续失败次数
	//
	// ★ 它必须等于 healthJudgeFailThreshold
	//
	//   HealthGate 每 30 秒判定一次，连续 3 次判定不健康才报一次失败
	//   ⇒ 熔断器收到 3 次失败就该熔断
	//   ⇒ **两层的阈值必须一致 ** —— 否则会出现「判定门说要 3 次，
	//     熔断器 1 次就开」或反过来的错配
	//   （这个错配我犯过一次 ：熔断器阈值设成 1 ⇒ 30 秒就熔断
	//     ⇒ 抗抖效果只剩 1/3  ⇒ 已修正 ）
	//
	// ⇒ 实际熔断耗时 = 判定间隔 × 判定次数 = 30 秒 × 3 = **90 秒**
	//
	// （改动前是「连续 5 次采样失败」，而采样每 2 秒一次 ⇒ 只等 10 秒
	//   ⇒ 一次 agent 重启就能把出口误熔断 ⇒ 频繁切换 ）
	l1BreakerThreshold = healthJudgeFailThreshold
)

// HealthGate 是一个出口的健康判定门
//
// 它**不负责熔断**，只负责回答一个问题 ：
// 「到目前为止，这个出口的健康证据够不够判它坏？「
type HealthGate struct {
	mu sync.Mutex

	// interval 是两次判定之间的最小间隔
	interval time.Duration
	// needFails 是连续多少次判定失败才该熔断
	needFails int
	// minRatio 是判定成功所需的最低成功率
	minRatio float64

	// samples 是最近的采样（按时间倒序，最新的在前）
	// 每条记录：时刻 + 是否成功
	samples []gateSample
	// lastJudge 是上次判定的时间
	lastJudge time.Time
	// hasJudged 表示是否已经判定过至少一次
	hasJudged bool
	// streak 是连续判定失败次数
	streak int
}

// gateSample 是一条采样记录
type gateSample struct {
	at time.Time
	ok bool
}

// NewHealthGate 创建一个健康判定门
//
// 参数留空时用默认值 （judgeInterval / needFails / minRatio ）
func NewHealthGate() *HealthGate {
	return &HealthGate{
		interval:  healthJudgeInterval,
		needFails: healthJudgeFailThreshold,
		minRatio:  healthJudgeMinRatio,
	}
}

// Record 记录一次健康采样，并在到达判定时点时做一次判定
//
// 返回值 ：
//
//	judged  —— 本次调用是否恰好完成了一次判定
//	healthy —— 判定结果：true = 这次判定认为出口是健康的
//	           （**注意这不是「是否熔断」** —— 判定失败一次不代表立刻熔断 ）
//	tripped —— 是否已经达到熔断门槛（连续判定失败次数够了 ）
//	ratio  —— 窗口内的成功率（0~1 ），仅在 judged 为真时有意义
//
// 调用方据此决定要不要推进熔断计数 ：
//   - 没到判定时点 ⇒ 只记采样，**不推进**
//   - 判定健康 ⇒ 向熔断器报一次成功（清零失败计数 ）
//   - 判定不健康 ⇒ 向熔断器报一次失败（熔断器自己按阈值决定要不要熔断 ）
func (g *HealthGate) Record(ok bool, now time.Time) (judged, healthy, tripped bool, ratio float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 记采样
	g.samples = append(g.samples, gateSample{at: now, ok: ok})
	// 丢掉窗口外的旧样本
	g.pruneLocked(now)

	// 第一次调用只记时间，不判定
	// ⇒ 第一个判定要等一个完整窗口
	if !g.hasJudged {
		g.hasJudged = true
		g.lastJudge = now
		return false, false, false, 0
	}

	// ★ 判定严格按间隔走
	//
	// ★ 曾经的错误实现 ：窗口全失败时「立刻判定」
	//   ⇒ 2 秒采样下每次全失败都判定一次 ⇒ streak 疯涨
	//   ⇒ 熔断时间反而比改动前还短（10 秒 → 6 秒 ）⇒ **抗抖完全失效**
	//
	// ⇒ 现在：判定节奏恒定是 interval
	// ⇒ 窗口全失败时，这次判定就是失败（ratio=0 ）
	// ⇒ 但仍然要连续 needFails 次才熔断
	if now.Sub(g.lastJudge) < g.interval {
		return false, false, false, 0
	}

	// 做判定
	g.lastJudge = now
	ratio = g.ratioLocked(now)
	if ratio >= g.minRatio {
		g.streak = 0
		return true, true, false, ratio
	}
	g.streak++
	return true, false, g.streak >= g.needFails, ratio
}

// pruneLocked 丢掉判定窗口之外的旧采样
//
// ★ 为什么必须是**按时间**而不是按个数 ：
//
//	采样频率不是固定的 （L1 探测 2 秒一次，业务成功也会记采样 ）
//	⇒ 用固定长度的环形缓冲算成功率 ⇒ 窗口长度会随采样频率变化
//	⇒ 2 秒采样时 64 条缓冲覆盖的是 128 秒，不是 30 秒
//	⇒ 「窗口」必须是时间概念
//
// 必须是调用方持锁
func (g *HealthGate) pruneLocked(now time.Time) {
	cut := now.Add(-g.interval)
	i := 0
	for i < len(g.samples) && g.samples[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		g.samples = append(g.samples[:0], g.samples[i:]...)
	}
	// 兜底：万一采样极多，别让切片无限增长
	if len(g.samples) > healthGateBufferSize {
		g.samples = append(g.samples[:0], g.samples[len(g.samples)-healthGateBufferSize:]...)
	}
}

// ratioLocked 计算最近一个窗口内的成功率
//
// 必须是调用方持锁
func (g *HealthGate) ratioLocked(now time.Time) float64 {
	if len(g.samples) == 0 {
		// 窗口内一个样本都没有 ⇒ 没有证据 ⇒ 不判定为坏
		return 1
	}
	okCount := 0
	for _, s := range g.samples {
		if s.ok {
			okCount++
		}
	}
	return float64(okCount) / float64(len(g.samples))
}

// Streak 返回当前连续判定失败次数（供状态展示 ）
func (g *HealthGate) Streak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.streak
}

// Reset 清空判定状态（出口重新上线时调用 ）
func (g *HealthGate) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.samples = nil
	g.streak = 0
	g.hasJudged = false
	g.lastJudge = time.Time{}
}
