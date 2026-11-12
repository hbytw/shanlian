package relay

import (
	"testing"
	"time"
)

// —— 2026-11-12 审计：黑洞代理 ——
//
// 「agent 在线」≠「agent 能用」✅✅
//
// 一台 agent 的通道可能全被熔断（冷却中）或正在重建 ✅✅
// ⇒ 它仍在 AgentIDs 里、评分仍然很高 ✅⚠️
// ⇒ 但每条连接都会失败 ⇒ 它是个黑洞代理 ✅⚠️
//
// ⇒ UsableAgentIDs 把它们排除 ✅✅

// newTestChannel 造一条指定状态的通道 ✅✅
func newTestChannel(kind ChannelKind, closed bool) *channel {
	c := &channel{kind: kind, breaker: newChannelBreaker()}
	if closed {
		c.closed.Store(true)
	}
	return c
}

// TestUsableAgentIDs_ExcludesChannelLessAgents 验证通道全没的 agent 被排除 ✅✅
func TestUsableAgentIDs_ExcludesChannelLessAgents(t *testing.T) {
	g := &Gateway{agents: map[string]*agentSession{}}

	// 一台有可用通道的 ✅✅
	ok := &agentSession{chans: []*channel{newTestChannel(ChannelZT, false)}}
	// 一台通道已关闭的（unusable ✅✅）
	dead := &agentSession{chans: []*channel{newTestChannel(ChannelZT, true)}}

	g.agents["eg-good"] = ok
	g.agents["eg-dead"] = dead

	got := g.UsableAgentIDs()
	if len(got) != 1 || got[0] != "eg-good" {
		t.Fatalf("应当只保留有可用通道的 eg-good，实际 %v", got)
	}
	if g.HasUsableChannel("eg-dead") {
		t.Fatal("通道已关闭的 agent 不该报告有可用通道")
	}
	if !g.HasUsableChannel("eg-good") {
		t.Fatal("有可用通道的 agent 应报告有可用通道")
	}
}

// TestUsableAgentIDs_ChannelBreakerOpenIsUnusable 验证通道熔断中的 agent 被排除 ✅✅
func TestUsableAgentIDs_ChannelBreakerOpenIsUnusable(t *testing.T) {
	g := &Gateway{agents: map[string]*agentSession{}}

	c := newTestChannel(ChannelZT, false)
	// 让通道级熔断器打开 ✅✅（要连续 threshold 次失败 ✅✅）
	for i := 0; i < c.breaker.threshold; i++ {
		c.breaker.Record(false, time.Now())
	}

	g.agents["eg-tripped"] = &agentSession{chans: []*channel{c}}

	if g.HasUsableChannel("eg-tripped") {
		t.Fatal("通道熔断中的 agent 不该报告有可用通道")
	}
	if len(g.UsableAgentIDs()) != 0 {
		t.Fatal("全部通道熔断时应当返回空")
	}
}

// TestUsableAgentIDs_UnknownAgent 验证未知 agent 不被误认为可用 ✅✅
func TestUsableAgentIDs_UnknownAgent(t *testing.T) {
	g := &Gateway{agents: map[string]*agentSession{}}
	if g.HasUsableChannel("nope") {
		t.Fatal("未知 agent 不该有可用通道")
	}
	if len(g.UsableAgentIDs()) != 0 {
		t.Fatal("没有 agent 时应返回空")
	}
}

// TestUsableAgentIDs_NoChannelAtAll 验证一条通道都没有的 agent 也被排除 ✅✅
func TestUsableAgentIDs_NoChannelAtAll(t *testing.T) {
	g := &Gateway{agents: map[string]*agentSession{}}
	g.agents["eg-none"] = &agentSession{}
	if g.HasUsableChannel("eg-none") {
		t.Fatal("没有通道的 agent 不该有可用通道")
	}
}

// TestChannelBreaker_BackoffEscalates 验证通道级熔断的指数退避能升上去 ✅✅
//
// ★ 2026-11-12 审计的同源问题 ✅⚠️
//
// 通道探测每 15 秒成功一次 ⇒ 原来的写法每次 success 都清零 ⇒
// 退避永远是 60 秒 ✅⚠️
func TestChannelBreaker_BackoffEscalates(t *testing.T) {
	b := newChannelBreaker()
	now := time.Now()

	// 连续失败到熔断 ✅✅
	for i := 0; i < b.threshold; i++ {
		b.Record(false, now)
	}
	if !b.open {
		t.Fatal("应当熔断")
	}
	if b.cooldown != 2*channelBreakerCooldownBase {
		t.Fatalf("首次熔断后冷却应已翻倍（为下一次准备），实际 %s", b.cooldown)
	}

	// ★ open 态的一次成功不该关闭，也不该重置退避 ✅✅
	base := b.cooldown
	b.Record(true, now.Add(time.Second))
	if !b.open {
		t.Fatal("open 态的一次成功不该关闭熔断")
	}
	if b.cooldown != base {
		t.Fatalf("open 态的成功不该重置退避，实际 %s（期望 %s）", b.cooldown, base)
	}

	// 冷却走完 → 半开 ✅✅
	after := now.Add(2 * time.Minute)
	if !b.Allow(after) {
		t.Fatal("冷却过后应当放行试探")
	}
	// 试探失败 ⇒ 重新熔断，冷却继续翻倍 ✅✅
	before := b.cooldown
	b.Record(false, after)
	if b.cooldown <= before {
		t.Fatalf("重新熔断后冷却应继续变长，实际 %s（之前 %s）", b.cooldown, before)
	}
}

// TestChannelBreaker_HalfOpenSuccessResetsBackoff 验证半开试探成功才重置退避 ✅✅
func TestChannelBreaker_HalfOpenSuccessResetsBackoff(t *testing.T) {
	b := newChannelBreaker()
	now := time.Now()

	for i := 0; i < b.threshold; i++ {
		b.Record(false, now)
	}
	// 冷却走完 → 半开（failures = threshold-1 ✅✅）
	after := now.Add(2 * time.Minute)
	if !b.Allow(after) {
		t.Fatal("冷却过后应当放行试探")
	}
	// 试探成功 ⇒ 真恢复 ⇒ 退避重置 ✅✅
	b.Record(true, after)
	if b.cooldown != channelBreakerCooldownBase {
		t.Fatalf("恢复后冷却应重置为 %s，实际 %s", channelBreakerCooldownBase, b.cooldown)
	}
	if b.failures != 0 {
		t.Fatalf("恢复后失败计数应清零，实际 %d", b.failures)
	}
}
