package picker

import (
	"net"
	"sync"
	"time"
)

// —— 熔断 ——

// 熔断参数。
const (
	// breakerThreshold 保留给直接使用 Breaker 的场景。
	breakerThreshold = 5
	// l2BreakerThreshold 是 L2（出口 ↔ 公网）连续失败多少次后熔断。
	//
	// 比 L1 小，原因有两个：
	//   - L2 一轮要三个目标**全**失败才算失败（见 cli/proxyprobe.go），
	//     证据本身就很强；
	//   - L2 每 90 秒才跑一次，阈值大一点要等很久才生效。
	//     2 次 = 连续两轮（约 3 分钟）出网能力不可用，才把出口排除出候选集。
	//
	// L1 与 L2 是**两套独立计数**：L1 成功不清 L2 的失败，反之亦然。
	// 混在一个计数器里时，L2 的失败会被下一次 L1 成功清掉 ——
	// L2 永远无法单独熔断，「链路正常但连不上公网」的出口会一直留在候选集里。
	l2BreakerThreshold = 2
	// breakerCooldownBase 是首次熔断的冷却时间。
	breakerCooldownBase = 60 * time.Second
	// breakerCooldownMax 是冷却时间的上限（指数退避封顶）。
	breakerCooldownMax = 10 * time.Minute

	// failureNoteTTL 是状态页「上次失败」留痕的展示窗口：
	// 只显示最近 30 分钟内的失败原因，更早的只留在日志里。
	// reason 本身仍永久保留（排障要用），过期只发生在**展示路径**。
	failureNoteTTL = 30 * time.Minute
)

// MinEgressUptime 是出口的「最小存活时长」。
//
// 上线不足这个时间的出口，探测失败**不计入**熔断计数。
//
// 为什么需要它：出口刚上线的那几分钟最容易抖 —— 隧道还在预热、
// 第一个探测流要重建、TCP 慢启动还没建好。此时连续几次失败就把
// 唯一一台出口熔断掉，等于「刚回来又熔断」，正是线上
// 「节点频繁熔断、网络基本不可用」的形态。
//
// 5 分钟是个折中：足够覆盖隧道预热与首轮探测，又不至于让
// 一个真的坏掉的出口无限期留在候选集里。
const MinEgressUptime = 5 * time.Minute

type breakerState int

const (
	breakerClosed   breakerState = iota // 正常
	breakerOpen                         // 熔断中
	breakerHalfOpen                     // 冷却结束，放一个试探
)

// Breaker 是单个出口的熔断器。
//
// 作用：出口已经坏了就别再往上试。没有它的话，每次选路都会挑到那个
// 「评分还停留在故障前」的出口，用户表现为反复超时。
type Breaker struct {
	mu       sync.Mutex
	state    breakerState
	fails    int
	openedAt time.Time
	cooldown time.Duration
	// trips 是累计熔断次数，用于指数退避。
	trips int
	// reason 是最近一次失败的简短原因，只用于展示。
	//
	// 不参与判定逻辑 —— 判定只看连续失败次数。
	// 成功时**不**清空：L1 探测每 2 秒成功一次，清空的话
	// L2 探测记录的原因会被立刻抹掉，状态页就永远看不到
	// 「这个出口连不上 1.1.1.1」这类真正有用的信息。
	//
	// ★ 展示层另有过期（failureNoteTTL，见 FreshReason）：
	//   reason 永久保留供排障，但状态页只显示最近 30 分钟内的 ——
	//   否则一次历史失败会在全绿的出口上挂一整天
	//   （2026-11-13 伟哥：「为什么香港还显示失败」）。
	reason string
	// lastFail 是最近一次失败的时刻（展示过期用；零值 = 从未失败）。
	lastFail time.Time
	// threshold 是熔断所需的连续失败次数。
	threshold int
}

// NewBreaker 创建熔断器（用默认阈值 breakerThreshold，供 L1 使用）。
func NewBreaker() *Breaker { return &Breaker{threshold: breakerThreshold} }

// NewBreakerWithThreshold 创建带自定义阈值的熔断器。
//
// 用途：L2 探测用独立阈值（见 l2BreakerThreshold）。
func NewBreakerWithThreshold(n int) *Breaker {
	if n <= 0 {
		n = breakerThreshold
	}
	return &Breaker{threshold: n}
}

// Allow 报告当前是否可以用这个出口。
//
// 熔断中但冷却已过 → 转为半开，允许试探。
//
// ★ 半开态**不限试探次数** （2026-11-12 审计确认 ）
//
//	注释原来写「允许一次试探」，实际是恒返回 true
//	⇒ 冷却一过就把全部流量放回刚失败的出口
//
//	现在这样写是**有意的** ：
//	- 半开 ⇒ 允许试探 ⇒ 若试探失败（判定不健康）⇒ 立刻重新熔断、冷却翻倍
//	- 若试探成功（判定健康）⇒ 关闭
//	⇒ 代价是「恢复需要等下一次判定」（≤30 秒）
//	⇒ 收益是不会在刚失败的出口上一次性倒大量流量
func (b *Breaker) Allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true
	case breakerOpen:
		if now.Sub(b.openedAt) >= b.cooldown {
			b.state = breakerHalfOpen
			return true
		}
		return false
	default: // halfOpen
		return true
	}
}

// Record 记录一次健康检查结果。
func (b *Breaker) Record(success bool, now time.Time) {
	b.RecordWithReason(success, "", now)
}

// RecordWithReason 记录一次健康检查结果，并记住失败原因（用于状态展示）。
//
// reason 是一句人能看懂的短语，例如：
//
//	"L2 连不上 1.1.1.1（超时）"
//	"L1 出口链路超时"
//
// 它只是展示信息：判定逻辑只看连续失败次数。
//
// # ★ open 状态下的一次成功，不能直接关闭熔断
//
// 这是 2026-11-12 审计发现的严重缺陷
//
// 熔断**不停止 L1 探测**（探测遍历的是全部 agent ），
// 而 L1 是 relay ping，链路边本来就是通的 ⇒ 每 2 秒成功一次
//
// ⇒ 原来的写法里，一次成功就把 breaker 直接 closed、trips=0、cooldown=0
// ⇒ ⇒ **熔断后约 30 秒（一次判定）就复活**
// ⇒ ⇒ **指数退避（60→120→240→480→600）永远升不上去**
//
// ⇒ 现在：★★
//
//	open 状态下的一次成功：只清失败计数，**不关闭、不清退避**
//	                ⇒ 冷却期（≥60 秒）真正生效
//	                ⇒ 连续坏的出口退避会一级级升上去
//	halfOpen 状态下的一次成功：试探通过 ⇒ 关闭，**并清退避**
//	                  （这时才是真的恢复了 ）
func (b *Breaker) RecordWithReason(success bool, reason string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if success {
		b.fails = 0
		switch b.state {
		case breakerOpen:
			// ★ 还在冷却期 ⇒ 一次成功不算恢复
			//   保持 open、保持退避计数
			//   ⇒ 冷却期走完 → Allow() 转 halfOpen → 那时才可能关闭
			return
		case breakerHalfOpen:
			// 试探通过 ⇒ 真的恢复了 ⇒ 关闭并清退避
			b.state = breakerClosed
			b.trips = 0
			b.cooldown = 0
		}
		return
	}

	b.fails++
	if reason != "" {
		b.reason = reason
	}
	b.lastFail = now
	switch b.state {
	case breakerHalfOpen:
		// 试探失败：立刻重新熔断，冷却时间翻倍
		b.trip(now)
	case breakerClosed:
		if b.fails >= b.threshold {
			b.trip(now)
		}
	}
}

// Reason 返回最近一次失败的原因（没有则为空串）。
func (b *Breaker) Reason() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reason
}

// FreshReason 返回「仍在展示窗口内」的失败原因。
//
// 与 Reason() 的区别只在过期：超过 failureNoteTTL 的历史失败不再展示
// （但 reason 数据保留，Reason() 照旧可读）。状态页的留痕走这个 ——
// 一次 30 分钟前的失败不该让全绿的出口「看起来还是坏的」。
func (b *Breaker) FreshReason(now time.Time) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reason == "" || b.lastFail.IsZero() {
		return ""
	}
	if now.Sub(b.lastFail) > failureNoteTTL {
		return ""
	}
	return b.reason
}

func (b *Breaker) trip(now time.Time) {
	b.state = breakerOpen
	b.openedAt = now
	b.trips++
	b.cooldown = breakerCooldownBase << uint(minInt(b.trips-1, 4)) // 60s,120s,240s,480s,600s 封顶
	if b.cooldown > breakerCooldownMax {
		b.cooldown = breakerCooldownMax
	}
}

// State 返回可读状态，用于 explain 与日志。
func (b *Breaker) State(now time.Time) (name string, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return "正常", time.Time{}
	case breakerOpen:
		return "熔断", b.openedAt.Add(b.cooldown)
	default:
		return "试探", time.Time{}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// —— 目的地路由表 ——

// routeTTL 是路由表项的存活时间。
//
// CDN 的解析结果会变，不能长期固化；5 分钟是个折中：
// 足够覆盖一次视频播放，又不至于让过期的选择一直生效。
const routeTTL = 300 * time.Second

type routeEntry struct {
	prefix *net.IPNet
	egress string
	score  float64
	added  time.Time
}

// RouteTable 是「目的地前缀 → 出口」的缓存。
//
// 查找按前缀长度从长到短（/32 → /24 → /16 → …），命中第一个未过期的条目。
//
// ★ 但目前**只有 /32 条目**
//
//	Set 的唯一调用方（selector.go 的 pick）写的是 `dst.String()+"/32"`
//	⇒ 查找虽然支持 /24，但没有任何地方写入 /24 ⇒ 那条路径是死的
//
// ⇒ 注释原来写「整段 CDN 的结论复用」，与实际不符 （2026-11-12 审计修正 ）
// ⇒ 真要用 /24 复用，需要在 Set 时按网段归并 —— 现在不做
type RouteTable struct {
	mu      sync.RWMutex
	entries map[string]*routeEntry // 键 = 规范化的 CIDR 字符串
}

// NewRouteTable 创建路由表。
func NewRouteTable() *RouteTable {
	return &RouteTable{entries: make(map[string]*routeEntry, 64)}
}

// Set 记录一条选择结果。
//
// prefix 可以是 "1.2.3.4/32" 这样的具体地址，也可以是整段。
func (t *RouteTable) Set(prefix, egress string, score float64, now time.Time) {
	_, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[ipnet.String()] = &routeEntry{
		prefix: ipnet, egress: egress, score: score, added: now,
	}
	// 顺手清理过期项，避免表无限增长
	for k, e := range t.entries {
		if now.Sub(e.added) > routeTTL {
			delete(t.entries, k)
		}
	}
}

// Lookup 查这个地址应该走哪个出口。
//
// 返回命中的前缀长度，便于 explain 说明「依据是 /24 的实测结果」。
func (t *RouteTable) Lookup(ip net.IP, now time.Time) (egress string, bits int, score float64, ok bool) {
	if ip == nil {
		return "", 0, 0, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	bestBits := -1
	for _, e := range t.entries {
		if now.Sub(e.added) > routeTTL {
			continue
		}
		if !e.prefix.Contains(ip) {
			continue
		}
		ones, _ := e.prefix.Mask.Size()
		if ones > bestBits {
			bestBits = ones
			egress, bits, score, ok = e.egress, ones, e.score, true
		}
	}
	return
}

// Invalidate 让某个出口的全部条目失效（出口故障时调用）。
func (t *RouteTable) Invalidate(egress string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for k, e := range t.entries {
		if e.egress == egress {
			delete(t.entries, k)
			n++
		}
	}
	return n
}

// Len 返回条目数。
func (t *RouteTable) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}
