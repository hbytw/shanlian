package picker

import (
	"net"
	"testing"
	"time"
)

func TestEWMA_Basic(t *testing.T) {
	var e EWMA
	if _, ok := e.Get(); ok {
		t.Fatal("初始状态应无效")
	}
	if got := e.Or(42); got != 42 {
		t.Fatalf("无效时应返回兜底值 42，实际 %v", got)
	}
	e.Update(100)
	if v, ok := e.Get(); !ok || v != 100 {
		t.Fatalf("首个样本应直接生效，实际 %v ok=%v", v, ok)
	}
	e.Update(0) // α=0.3 → 100*0.7 + 0*0.3 = 70
	if v, _ := e.Get(); v < 69.9 || v > 70.1 {
		t.Fatalf("EWMA 应约为 70，实际 %v", v)
	}
}

func TestEWMA_IgnoresInvalid(t *testing.T) {
	var e EWMA
	e.Update(50)
	e.Update(nan())
	if v, _ := e.Get(); v != 50 {
		t.Fatalf("NaN 不应改变值，实际 %v", v)
	}
	e.Update(inf())
	if v, _ := e.Get(); v != 50 {
		t.Fatalf("Inf 不应改变值，实际 %v", v)
	}
}

func TestEWMA_History(t *testing.T) {
	var e EWMA
	for i := 1; i <= 15; i++ {
		e.Update(float64(i))
	}
	h := e.History()
	if len(h) != historyLen {
		t.Fatalf("历史长度应为 %d，实际 %d", historyLen, len(h))
	}
	// 最近 10 个：6..15
	if h[0] != 6 || h[len(h)-1] != 15 {
		t.Fatalf("历史应保留最近 10 个样本，实际 %v", h)
	}
}

func TestEWMA_Reset(t *testing.T) {
	var e EWMA
	e.Update(10)
	e.Reset()
	if _, ok := e.Get(); ok {
		t.Fatal("Reset 后应无效")
	}
}

func TestScore_MonotonicInBandwidth(t *testing.T) {
	w := DefaultWeights()
	prev := -1.0
	for _, bw := range []float64{1, 5, 50, 100, 1000} {
		s := Score(50, bw, 0, false, w)
		if s < prev {
			t.Fatalf("带宽越大分应越高：bw=%v 得分 %v < 前一个 %v", bw, s, prev)
		}
		prev = s
	}
}

func TestScore_MonotonicInRTT(t *testing.T) {
	w := DefaultWeights()
	prev := 101.0
	for _, rtt := range []float64{5, 50, 100, 500} {
		s := Score(rtt, 50, 0, false, w)
		if s > prev {
			t.Fatalf("延迟越大分应越低：rtt=%v 得分 %v > 前一个 %v", rtt, s, prev)
		}
		prev = s
	}
}

func TestScore_Bounds(t *testing.T) {
	w := DefaultWeights()
	if s := Score(0, 1e9, 0, true, w); s > 100 {
		t.Fatalf("总分不应超过 100，实际 %v", s)
	}
	if s := Score(1e6, 0, 1, false, w); s < 0 {
		t.Fatalf("总分不应为负，实际 %v", s)
	}
}

func TestScore_AffinityBonus(t *testing.T) {
	w := DefaultWeights()
	a := Score(50, 50, 0, false, w)
	b := Score(50, 50, 0, true, w)
	if b <= a {
		t.Fatalf("亲和应有加成：无亲和 %v 有亲和 %v", a, b)
	}
	if b-a > w.AffinityBonus+0.01 {
		t.Fatalf("加成不应超过 %v，实际 %v", w.AffinityBonus, b-a)
	}
}

func TestScore_ExtremeGapNotDominant(t *testing.T) {
	// 软上限的意义：1Gbps 不应该把 10ms 的机器远远甩开，
	// 两者分数应当接近，而不是带宽方碾压。
	w := DefaultWeights()
	fast := Score(10, 50, 0, false, w)       // 低延迟、中带宽
	huge := Score(200, 1000, 0.01, false, w) // 高延迟、超大带宽
	gap := fast - huge
	if gap < 0 {
		gap = -gap
	}
	if gap > 15 {
		t.Fatalf("软上限失效：两者差距 %.1f 分过大（fast=%.1f huge=%.1f）", gap, fast, huge)
	}
}

// —— 熔断 ——

func TestBreaker_TripsAfterThreshold(t *testing.T) {
	b := NewBreaker()
	now := time.Now()
	for i := 0; i < breakerThreshold; i++ {
		if !b.Allow(now) {
			t.Fatalf("第 %d 次失败前不应熔断", i)
		}
		b.Record(false, now)
	}
	if b.Allow(now) {
		t.Fatal("连续失败达到阈值后应熔断")
	}
}

func TestBreaker_HalfOpenThenRecover(t *testing.T) {
	b := NewBreaker()
	now := time.Now()
	for i := 0; i < breakerThreshold; i++ {
		b.Record(false, now)
	}
	if b.Allow(now) {
		t.Fatal("应处于熔断")
	}
	// 冷却结束 → 允许一次试探
	later := now.Add(breakerCooldownBase + time.Second)
	if !b.Allow(later) {
		t.Fatal("冷却结束后应允许试探")
	}
	if st, _ := b.State(later); st != "试探" {
		t.Fatalf("应进入试探状态，实际 %q", st)
	}
	// 试探成功 → 恢复
	b.Record(true, later)
	if st, _ := b.State(later); st != "正常" {
		t.Fatalf("试探成功应恢复正常，实际 %q", st)
	}
	if !b.Allow(later) {
		t.Fatal("恢复后应允许")
	}
}

func TestBreaker_BackoffGrows(t *testing.T) {
	b := NewBreaker()
	now := time.Now()
	var prev time.Duration
	for round := 0; round < 3; round++ {
		// 制造一次熔断
		for i := 0; i < breakerThreshold; i++ {
			b.Record(false, now)
		}
		_, until := b.State(now)
		d := until.Sub(now)
		if round > 0 && d <= prev {
			t.Fatalf("冷却时间应递增：第 %d 轮 %v <= 上一轮 %v", round, d, prev)
		}
		prev = d
		// 跳过冷却，进入半开再失败
		now = now.Add(d + time.Second)
		if !b.Allow(now) {
			t.Fatalf("第 %d 轮冷却后应允许试探", round)
		}
		b.Record(false, now) // 试探失败 → 再次熔断
	}
}

func TestBreaker_SuccessResetsCounter(t *testing.T) {
	b := NewBreaker()
	now := time.Now()
	b.Record(false, now)
	b.Record(false, now)
	b.Record(true, now) // 成功清零
	b.Record(false, now)
	b.Record(false, now)
	if !b.Allow(now) {
		t.Fatal("中间成功过就不该熔断")
	}
}

// —— 路由表 ——

func mustCIDR(t *testing.T, s string) net.IP {
	t.Helper()
	ip, _, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("解析 %q 失败: %v", s, err)
	}
	return ip
}

func TestRouteTable_LongestPrefixWins(t *testing.T) {
	rt := NewRouteTable()
	now := time.Now()
	rt.Set("1.2.0.0/16", "egA", 80, now)
	rt.Set("1.2.3.0/24", "egB", 90, now)
	rt.Set("1.2.3.4/32", "egC", 95, now)

	eg, bits, _, ok := rt.Lookup(net.ParseIP("1.2.3.4"), now)
	if !ok || eg != "egC" || bits != 32 {
		t.Fatalf("应命中 /32 → egC，实际 %q /%d ok=%v", eg, bits, ok)
	}
	eg, bits, _, ok = rt.Lookup(net.ParseIP("1.2.3.9"), now)
	if !ok || eg != "egB" || bits != 24 {
		t.Fatalf("应命中 /24 → egB，实际 %q /%d", eg, bits)
	}
	eg, bits, _, ok = rt.Lookup(net.ParseIP("1.2.9.9"), now)
	if !ok || eg != "egA" || bits != 16 {
		t.Fatalf("应命中 /16 → egA，实际 %q /%d", eg, bits)
	}
	if _, _, _, ok := rt.Lookup(net.ParseIP("9.9.9.9"), now); ok {
		t.Fatal("不应命中")
	}
}

func TestRouteTable_TTLExpiry(t *testing.T) {
	rt := NewRouteTable()
	now := time.Now()
	rt.Set("1.2.3.4/32", "egA", 80, now)

	if _, _, _, ok := rt.Lookup(net.ParseIP("1.2.3.4"), now); !ok {
		t.Fatal("未过期时应命中")
	}
	later := now.Add(routeTTL + time.Second)
	if _, _, _, ok := rt.Lookup(net.ParseIP("1.2.3.4"), later); ok {
		t.Fatal("过期后不应命中")
	}
}

func TestRouteTable_Invalidate(t *testing.T) {
	rt := NewRouteTable()
	now := time.Now()
	rt.Set("1.0.0.0/8", "egA", 80, now)
	rt.Set("2.0.0.0/8", "egA", 80, now)
	rt.Set("3.0.0.0/8", "egB", 80, now)
	if n := rt.Invalidate("egA"); n != 2 {
		t.Fatalf("应失效 2 条，实际 %d", n)
	}
	if _, _, _, ok := rt.Lookup(net.ParseIP("3.0.0.1"), now); !ok {
		t.Fatal("egB 的条目不应受影响")
	}
}

func TestRouteTable_BadPrefixIgnored(t *testing.T) {
	rt := NewRouteTable()
	rt.Set("not-a-cidr", "egA", 80, time.Now())
	if rt.Len() != 0 {
		t.Fatalf("非法前缀应被忽略，实际 %d 条", rt.Len())
	}
}

// —— 选路器 ——

func TestSelector_PicksBestScored(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	// egA 快，egB 慢
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL1("egB", 200, 0.1, 5, now)

	d := s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, now)
	if d.Egress != "egA" {
		t.Fatalf("应选 egA，实际 %q（%s）", d.Egress, d.Reason)
	}
	if d.Source != "实测择优" {
		t.Fatalf("来源应为实测择优，实际 %q", d.Source)
	}
	if len(d.All) != 2 {
		t.Fatalf("应给出 2 个候选明细，实际 %d", len(d.All))
	}
}

func TestSelector_HysteresisKeepsCurrent(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 50, 0, 50, now)
	s.UpdateL1("egB", 50, 0, 50, now)

	// 第一次选路选中某个
	d := s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, now)
	first := d.Egress

	// 另一个只略微更好（未超过 margin），应保持现状
	s.UpdateL1(first, 60, 0, 50, now.Add(time.Second))
	other := "egA"
	if first == "egA" {
		other = "egB"
	}
	s.UpdateL1(other, 55, 0, 50, now.Add(time.Second))

	d = s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, now.Add(2*time.Second))
	if d.Egress != first {
		t.Fatalf("轻微优势不应切换：期望保持 %q，实际 %q（%s）", first, d.Egress, d.Reason)
	}
}

func TestSelector_SwitchesWhenClearlyBetter(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL1("egB", 300, 0.3, 2, now)

	// 先选 egA
	d := s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, now)
	if d.Egress != "egA" {
		t.Fatalf("首次应选 egA")
	}

	// 让 egA 变得极差、egB 极好。
	// 注意要喂多轮：EWMA 每次只吸收 30%，单次样本不足以翻转排序。
	later := now.Add(time.Minute)
	for i := 0; i < 6; i++ {
		s.UpdateL1("egA", 500, 0.5, 1, later)
		s.UpdateL1("egB", 10, 0, 200, later)
	}

	d = s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, later)
	if d.Egress != "egB" {
		t.Fatalf("明显更好时应切换：实际 %q（%s）", d.Egress, d.Reason)
	}
}

func TestSelector_BreakerExcludesEgress(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 10, 0, 200, now) // 评分很高
	s.UpdateL1("egB", 200, 0.2, 5, now)

	// 把 egA 熔断
	// ★ 采样与判定分离 ⇒ 需要推进到判定
	tripBreaker(t, s, "egA", now)

	d := s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, now)
	if d.Egress != "egB" {
		t.Fatalf("熔断的出口应被排除，实际 %q（%s）", d.Egress, d.Reason)
	}
	for _, c := range d.All {
		if c.NodeID == "egA" && c.Eligible {
			t.Fatal("egA 应标记为不可用")
		}
	}
}

func TestSelector_AllBrokenFallsBack(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 10, 0, 100, now)
	s.UpdateL1("egB", 20, 0, 50, now)
	for _, id := range []string{"egA", "egB"} {
		tripBreaker(t, s, id, now)
	}
	d := s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA", "egB"}, now)
	if d.Egress == "" {
		t.Fatal("全部熔断也应返回一个兜底出口")
	}
	if d.Source != "兜底" {
		t.Fatalf("来源应为兜底，实际 %q", d.Source)
	}
}

func TestSelector_RouteTableReuse(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL1("egB", 30, 0, 90, now)

	ip := net.ParseIP("1.2.3.4")
	d1 := s.Pick(ip, "", []string{"egA", "egB"}, now)
	if d1.Egress != "egA" {
		t.Fatalf("首次应选 egA，实际 %q", d1.Egress)
	}

	// 让 egB 略好一点，但因为路由表已缓存，应继续用 egA
	later := now.Add(5 * time.Second)
	s.UpdateL1("egB", 10, 0, 200, later)
	d2 := s.Pick(ip, "", []string{"egA", "egB"}, later)
	if d2.Source != "路由表" {
		t.Fatalf("应命中路由表，实际来源 %q（%s）", d2.Source, d2.Reason)
	}
}

func TestSelector_AffinityFromL2(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 60, 0, 50, now)
	s.UpdateL1("egB", 60, 0, 50, now)
	// 目的地经 egB 解析 → 亲和 egB
	s.UpdateL2("egB", "youtube.com", 30, 0, 40, true, now)

	d := s.Pick(nil, "youtube.com", []string{"egA", "egB"}, now)
	if d.Egress != "egB" {
		t.Fatalf("有亲和的出口应胜出：实际 %q（%s）", d.Egress, d.Reason)
	}
}

func TestSelector_ExplainDoesNotMutate(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 20, 0, 100, now)
	s.UpdateL1("egB", 300, 0, 2, now)
	ip := net.ParseIP("1.2.3.4")

	if cur := s.Current(); cur != "" {
		t.Fatalf("初始不应有当前出口，实际 %q", cur)
	}
	s.Explain(ip, "", []string{"egA", "egB"}, now)
	if cur := s.Current(); cur != "" {
		t.Fatalf("Explain 不应改变当前出口，实际 %q", cur)
	}
	if s.Table().Len() != 0 {
		t.Fatalf("Explain 不应写路由表，实际 %d 条", s.Table().Len())
	}
}

func TestSelector_InvalidateEgress(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()
	s.UpdateL1("egA", 20, 0, 100, now)
	s.Pick(net.ParseIP("1.2.3.4"), "", []string{"egA"}, now)
	if s.Table().Len() == 0 {
		t.Fatal("应已写入路由表")
	}
	s.InvalidateEgress("egA")
	if s.Table().Len() != 0 {
		t.Fatalf("失效后路由表应为空，实际 %d", s.Table().Len())
	}
	if s.Current() != "" {
		t.Fatal("当前出口应被清空")
	}
}

func TestSelector_NoCandidates(t *testing.T) {
	s := NewSelector(DefaultWeights())
	d := s.Pick(net.ParseIP("1.2.3.4"), "", nil, time.Now())
	if d.Egress != "" {
		t.Fatalf("无候选时不应返回出口，实际 %q", d.Egress)
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

func inf() float64 {
	var one float64 = 1
	return one / zeroFloat
}

var zeroFloat float64
