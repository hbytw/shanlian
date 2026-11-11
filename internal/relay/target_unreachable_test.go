package relay

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"slx/internal/proto"
)

// TestConnectRefusedIsTargetUnreachable 验证「agent 连不上目标」会被标成
// TargetUnreachable —— 它**不是**出口故障，不能计入熔断。
//
// 线上教训：用户访问被墙的站点 → 记成出口坏了 → 出口被熔断排除
// → 可用出口更少 → 更多请求失败。死亡螺旋就是这么来的。
func TestConnectRefusedIsTargetUnreachable(t *testing.T) {
	// 找一个几乎肯定没人监听的端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("找空闲端口失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // 立刻关掉，端口空出来但没人听

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	g := startGateway(t, testKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startAgent(t, ctx, g.Addr(), testKey, "node-target")
	waitAgent(t, g, "node-target")

	_, err = g.Open("node-target", proto.ConnectReq{
		Net: 0, Transport: proto.ConnectTCP, Addr: host, Port: uint16(port),
	})
	if err == nil {
		t.Fatal("连到无人监听的端口应失败")
	}
	ce, ok := err.(*ConnectError)
	if !ok {
		t.Fatalf("期望 *ConnectError，实际 %T: %v", err, err)
	}
	if !ce.TargetUnreachable {
		t.Fatalf("目标不可达应标记 TargetUnreachable（原因: %s）", ce.Reason)
	}
}

// TestUnknownAgentIsNotTargetUnreachable 验证「出口不在池子里」属于出口故障。
func TestUnknownAgentIsNotTargetUnreachable(t *testing.T) {
	g := startGateway(t, testKey)
	_, err := g.Open("nobody", proto.ConnectReq{
		Transport: proto.ConnectTCP, Addr: "127.0.0.1", Port: 1,
	})
	if err != ErrNoAgent {
		t.Fatalf("期望 ErrNoAgent，实际 %v", err)
	}
	// ErrNoAgent 不是 ConnectError —— 调用方按「出口故障」处理，计入熔断。
	var ce *ConnectError
	if errors.As(err, &ce) {
		t.Fatal("ErrNoAgent 不该被当成目标不可达")
	}
}
