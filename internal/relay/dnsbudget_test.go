package relay

import (
	"strings"
	"testing"
	"time"
)

// TestResolve_RespectsCallerBudget 确认 DNS 解析的**总预算**等于调用方给的时限。
//
// 审计报告「根因 B」：DNS 路径给客户端的预算是 2 秒，
// 但 relay.Resolve 内部先 openStreamWithTimeout(5 秒)，
// 再给解析 2 秒 ⇒ 实际最坏 7 秒。
// 客户端 DNS 等 7 秒 ⇒ 后续所有请求排队 ⇒「一个请求慢，全家都慢」。
//
// 修法：开流用 min(剩余预算, streamOpenTimeout)，解析只用剩下的时间。
func TestResolve_RespectsCallerBudget(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-budget")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-budget")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-budget").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 出口收下解析请求但不答复 ⇒ 网关会一直等到预算耗尽。
	ag.hangResolve.Store(true)

	const budget = 600 * time.Millisecond
	start := time.Now()
	_, err := gw.Resolve("agent-budget", "example.com", budget)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("出口不答复时解析应当失败")
	}
	// 允许 1 倍预算的宽限（调度抖动），但绝不能出现 5 秒开流那种量级。
	if elapsed > budget*3 {
		t.Fatalf("解析耗时 %s，远超预算 %s（总预算没有被遵守）", elapsed, budget)
	}
}

// TestResolve_RejectsWhenOpenCapFull 确认 DNS 开流并发满时**快速失败**。
//
// 快速失败才能让 dnsfwd 回落到本地解析；排队只会把问题往后推。
func TestResolve_RejectsWhenOpenCapFull(t *testing.T) {
	for i := 0; i < cap(dnsOpenSem); i++ {
		dnsOpenSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(dnsOpenSem); i++ {
			<-dnsOpenSem
		}
	}()

	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-cap")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-cap")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-cap").tlsChannels()) >= 1
	}, "直连通道应建立")

	start := time.Now()
	_, err := gw.Resolve("agent-cap", "example.com", 5*time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("DNS 开流并发满时应当失败（让调用方回落到本地解析）")
	}
	if !strings.Contains(err.Error(), "DNS 开流并发已达上限") {
		t.Fatalf("错误信息应说明是开流上限，实际 = %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("被上限拒绝时耗时 %s，必须快速失败（不能排队）", elapsed)
	}
}

// TestResolve_NoUsableChannel_NotErrNoAgent 确认「没有可用通道」与
// 「agent 不在线」是两种不同的错误。
//
// 混为一谈会让上层把「通道级熔断」误判成「出口掉线」，
// 于是把整台出口踢出候选集（级联的起点）。
func TestResolve_NoUsableChannel_NotErrNoAgent(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ag := newFakeAgent(t, key, "agent-nocand2")
	defer ag.close()

	gw, gwAddr, stop := startGatewayWithMode(t, key, TransportAuto, "gw-nocand2")
	defer stop()

	ag.connectReverse(t, gwAddr)
	waitFor(t, 5*time.Second, func() bool {
		return len(agentSessionOf(gw, "agent-nocand2").tlsChannels()) >= 1
	}, "直连通道应建立")

	// 把这台 agent 的所有通道都熔断掉。
	now := time.Now()
	for _, c := range agentSessionOf(gw, "agent-nocand2").channels() {
		for i := 0; i < 100; i++ {
			c.recordProbe(false, 0, now)
		}
	}

	_, err := gw.Resolve("agent-nocand2", "example.com", time.Second)
	if err == nil {
		t.Fatal("所有通道熔断时解析应当失败")
	}
	if err != ErrNoUsableChannel {
		t.Fatalf("应当返回 ErrNoUsableChannel，实际 = %v", err)
	}
}

// TestResolve_SlowOpenStillRespectsBudget 是 TestResolve_RespectsCallerBudget
// 的加强版：用一条「对端永远不确认 SYN」的会话，让**开流本身**卡住。
//
// 这才是旧代码真正的病灶：开流用固定的 streamOpenTimeout（5 秒），
// 与调用方的 2 秒预算无关 ⇒ 实际最坏 5 秒 + 2 秒 = 7 秒。
// 修法是开流用 min(剩余预算, streamOpenTimeout) ⇒ 总预算 = 调用方的预算。
func TestResolve_SlowOpenStillRespectsBudget(t *testing.T) {
	sess := deadSession(t)

	gw := &Gateway{agents: map[string]*agentSession{}}
	s := &agentSession{
		nodeID: "slow-open",
		sess:   sess,
		remote: "127.0.0.1:0",
		since:  time.Now(),
	}
	s.chans = []*channel{newChannel(ChannelZT, nil, sess, "127.0.0.1:0")}
	gw.agents["slow-open"] = s

	const budget = 600 * time.Millisecond
	start := time.Now()
	_, err := gw.Resolve("slow-open", "example.com", budget)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("对端不确认时解析应当失败")
	}
	if elapsed > budget*3 {
		t.Fatalf("解析耗时 %s，远超预算 %s（开流没有计入总预算）", elapsed, budget)
	}
}
