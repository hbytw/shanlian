package relay

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestAgentLifecycleHooks 验证 agent 接入与断开都会触发回调。
//
// 这是「熔断状态随 agent 掉线/重连复位」的事件源：
// 没有它，上层根本不知道出口掉线了，它带着冷却期回来时
// 仍然被排除在候选之外（线上表现为「刚回来又熔断」）。
func TestAgentLifecycleHooks(t *testing.T) {
	joined := make(chan string, 4)
	left := make(chan string, 4)

	g := NewGateway(GatewayOptions{
		Key:  testKey,
		Addr: "127.0.0.1:0",
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnAgentJoin: func(nodeID string, since time.Time) {
			joined <- nodeID
		},
		OnAgentLeave: func(nodeID string, uptime time.Duration) {
			left <- nodeID
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		g.Close()
	})
	go func() { _ = g.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for g.Addr() == "127.0.0.1:0" || g.ln == nil {
		if time.Now().After(deadline) {
			t.Fatal("网关未在 3 秒内就绪")
		}
		time.Sleep(10 * time.Millisecond)
	}

	agentCtx, agentCancel := context.WithCancel(context.Background())
	t.Cleanup(agentCancel)
	startAgent(t, agentCtx, g.Addr(), testKey, "node-hook")
	waitAgent(t, g, "node-hook")

	select {
	case id := <-joined:
		if id != "node-hook" {
			t.Fatalf("接入回调的 node_id 不对：%q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("接入回调未触发")
	}

	// agent 断开 → 应触发掉线回调
	agentCancel()
	select {
	case id := <-left:
		if id != "node-hook" {
			t.Fatalf("掉线回调的 node_id 不对：%q", id)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("掉线回调未触发")
	}
}
