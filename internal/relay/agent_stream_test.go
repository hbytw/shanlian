package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"slx/internal/proto"
	"slx/internal/stream"
)

// TestAgentHandleStreamClosesStream 验证 agent 处理完一条流后**一定**把流关掉。
//
// 回归：审计报告「根因 B3」—— agent 侧 handleStream 只有**错误分支**才关流，
// 正常中继结束后流一直开着 ⇒ 每条连接都占着一条 yamux 流 + 一个 goroutine。
//
// 修法（两处都要）：
//   - agent.handleStream 开头加 defer s.Close()（yamux 的 Close 幂等）；
//   - stream.FramedPipe 结束时把隧道也关掉。
//
// 这里用 **UDP** 中继做验证：TCP 走 RelayTCP，它一直就会关两侧，
// 用 TCP 测不出这个泄漏。
func TestAgentHandleStreamClosesStream(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	key := bytes.Repeat([]byte{0x44}, 32)

	c1, c2 := net.Pipe()
	clientSess, err := yamux.Client(c1, yamuxConfig())
	if err != nil {
		t.Fatalf("建 yamux 客户端失败: %v", err)
	}
	defer clientSess.Close()
	serverSess, err := yamux.Server(c2, yamuxConfig())
	if err != nil {
		t.Fatalf("建 yamux 服务端失败: %v", err)
	}
	defer serverSess.Close()

	// 客户端开一条流。
	clientStream, err := clientSess.OpenStream()
	if err != nil {
		t.Fatalf("开流失败: %v", err)
	}

	// 服务端接受这条流，交给 agent 处理。
	serverStream, err := serverSess.AcceptStream()
	if err != nil {
		t.Fatalf("接受流失败: %v", err)
	}

	// 模拟「agent 侧的 UDP socket」：对端一关，中继就结束。
	targetA, targetB := net.Pipe()
	defer targetB.Close()

	agent := NewAgent(AgentOptions{
		Key:    key,
		NodeID: "agent-close",
		Log:    log,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return targetA, nil
		},
	})

	handled := make(chan struct{})
	go func() {
		agent.handleStream(serverStream)
		close(handled)
	}()

	// 发一个 UDP 建连请求。
	req := proto.ConnectReq{Net: 0, Transport: proto.ConnectUDP, Addr: "203.0.113.1", Port: 53}
	if err := stream.WriteConnectReq(clientStream, req); err != nil {
		t.Fatalf("写建连请求失败: %v", err)
	}

	// agent 会回「连上了」。
	_ = clientStream.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := stream.ReadConnectResp(clientStream)
	if err != nil {
		t.Fatalf("读建连应答失败: %v", err)
	}
	if !resp.Established {
		t.Fatalf("期望建连成功，实际 = %+v", resp)
	}

	// 关掉 agent 侧的 socket ⇒ 中继结束 ⇒ handleStream 返回。
	_ = targetB.Close()

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handleStream 没有返回")
	}

	// 流必须已经被关闭：再读应当立刻拿到 EOF，而不是挂住。
	_ = clientStream.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := clientStream.Read(buf); err == nil {
		t.Fatal("agent 处理完流之后必须把它关掉（否则 yamux 流永久泄漏）")
	} else if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("期望读到 EOF（流已关闭），实际 = %v", err)
	}
}
