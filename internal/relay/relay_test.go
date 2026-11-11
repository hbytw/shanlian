package relay

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"slx/internal/proto"
)

// testKey 是测试用 32 字节密钥（与生产的 network_key 等长）。
var testKey = bytes.Repeat([]byte{0x5a}, 32)

// echoOnce 起一个只回显一次的 TCP 服务，返回其地址。
func echoOnce(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 echo 服务失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // 回显
	}()
	return ln.Addr().String()
}

// startGateway 起一个监听随机端口的网关。
func startGateway(t *testing.T, key []byte) *Gateway {
	t.Helper()
	g := NewGateway(GatewayOptions{
		Key:  key,
		Addr: "127.0.0.1:0",
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		g.Close()
	})
	runErr := make(chan error, 1)
	go func() { runErr <- g.Run(ctx) }()

	// 等 listener 就绪
	deadline := time.Now().Add(3 * time.Second)
	for g.Addr() == "127.0.0.1:0" || !g.Listening() {
		if time.Now().After(deadline) {
			t.Fatal("网关未在 3 秒内就绪")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return g
}

// startAgent 起一个 agent 并等到网关把它登记进池子。
func startAgent(t *testing.T, ctx context.Context, gatewayAddr string, key []byte, nodeID string) {
	t.Helper()
	a := NewAgent(AgentOptions{
		Key:         key,
		GatewayAddr: gatewayAddr,
		NodeID:      nodeID,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	go func() { _ = a.Run(ctx) }()
}

// waitAgent 轮询等待 agent 在网关上线。
func waitAgent(t *testing.T, g *Gateway, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !g.HasAgent(nodeID) {
		if time.Now().After(deadline) {
			t.Fatalf("agent %s 未在 5 秒内接入（在线: %v）", nodeID, g.AgentIDs())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEndToEndTCP 验证「网关开流 → agent 连目标 → 双向中继」完整链路。
//
// 这是新架构的核心路径：一条 TCP 连接从网关侧流过去，字节能到真实目标再回来。
func TestEndToEndTCP(t *testing.T) {
	target := echoOnce(t)
	targetHost, targetPortStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(targetPortStr)

	g := startGateway(t, testKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startAgent(t, ctx, g.Addr(), testKey, "node-aaa")
	waitAgent(t, g, "node-aaa")

	conn, err := g.Open("node-aaa", proto.ConnectReq{
		Net:       0,
		Transport: proto.ConnectTCP,
		Addr:      targetHost,
		Port:      uint16(port),
	})
	if err != nil {
		t.Fatalf("开流失败: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello-through-relay")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("回显不一致: want %q got %q", msg, got)
	}
}

// TestAuthRejectsWrongKey 验证密钥不一致时连不上。
//
// 这条必须存在：密钥是唯一的身份凭证（TLS 证书是自签且不校验链），
// 一旦这里漏了，任何能连上端口的人都能免费用你的出口。
func TestAuthRejectsWrongKey(t *testing.T) {
	g := startGateway(t, testKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wrongKey := bytes.Repeat([]byte{0x11}, 32)
	startAgent(t, ctx, g.Addr(), wrongKey, "node-bad")

	time.Sleep(500 * time.Millisecond)
	if g.HasAgent("node-bad") {
		t.Fatal("错误密钥的 agent 不应被接受")
	}
}

// TestAuthRejectsClockSkew 验证时间戳偏差过大被拒。
func TestAuthRejectsClockSkew(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// 客户端用 10 分钟前的时间戳
	done := make(chan error, 1)
	go func() {
		// 手工构造一条时钟偏斜的握手：直接调 mac 算好再写
		nonce := bytes.Repeat([]byte{7}, nonceLen)
		ts := time.Now().Add(-10 * time.Minute).Unix()
		tag := mac(testKey, nonce, ts, "node-skew")
		buf := make([]byte, 0, 1+nonceLen+8+macLen+2+len("node-skew"))
		buf = append(buf, ProtoVersion)
		buf = append(buf, nonce...)
		var tb [8]byte
		putUint64(tb[:], uint64(ts))
		buf = append(buf, tb[:]...)
		buf = append(buf, tag...)
		buf = append(buf, byte(len("node-skew")>>8), byte(len("node-skew")))
		buf = append(buf, "node-skew"...)
		_, werr := b.Write(buf)
		// 等拒绝响应
		rb := make([]byte, 64)
		_, _ = b.Read(rb)
		done <- werr
	}()

	_, err := ServerHandshake(a, testKey)
	if err == nil {
		t.Fatal("时间戳偏斜的握手应被拒绝")
	}
	<-done
}

// TestOpenUnknownAgent 验证对不存在的 agent 开流返回 ErrNoAgent。
func TestOpenUnknownAgent(t *testing.T) {
	g := startGateway(t, testKey)
	_, err := g.Open("nobody", proto.ConnectReq{Transport: proto.ConnectTCP, Addr: "127.0.0.1", Port: 1})
	if err != ErrNoAgent {
		t.Fatalf("期望 ErrNoAgent，实际 %v", err)
	}
}

// TestConnectRefusedReportsReset 验证连不上目标时返回 Reset=true（应回 RST）。
func TestConnectRefusedReportsReset(t *testing.T) {
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
	startAgent(t, ctx, g.Addr(), testKey, "node-refuse")
	waitAgent(t, g, "node-refuse")

	_, err = g.Open("node-refuse", proto.ConnectReq{
		Net: 0, Transport: proto.ConnectTCP, Addr: host, Port: uint16(port),
	})
	if err == nil {
		t.Fatal("连到无人监听的端口应失败")
	}
	ce, ok := err.(*ConnectError)
	if !ok {
		t.Fatalf("期望 *ConnectError，实际 %T: %v", err, err)
	}
	if !ce.Reset {
		t.Fatalf("端口拒绝应置 Reset=true（让客户端快速失败），实际 Reset=false，原因: %s", ce.Reason)
	}
}

// putUint64 把大端 uint64 写入 dst（避免依赖 encoding/binary 的多余 import 顺序）。
func putUint64(dst []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		dst[i] = byte(v)
		v >>= 8
	}
}

// TestPingMeasuresRTT 验证探测通道能在 agent 上往返并测得合理 RTT。
//
// 这条链路是 proxy 模式选路的唯一数据来源：没有它，多个出口时
// 选路器拿不到任何质量指标，只会按候选顺序挑第一个。
func TestPingMeasuresRTT(t *testing.T) {
	g := startGateway(t, testKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startAgent(t, ctx, g.Addr(), testKey, "node-ping")
	waitAgent(t, g, "node-ping")

	// 首次 Ping 会顺手建立探测流，之后复用。
	//
	// 注意断言口径：只要求「往返成功 + RTT 不为负」。
	// 不要求 RTT > 0 —— Windows 上 time.Now 的粒度可达 15ms，
	// 本机回环往返会落在同一个时钟滴答里被量成 0。
	// 真机（Linux）上 RTT 是真实的亚毫秒/毫秒值，生产口径不受影响。
	for i := 0; i < 3; i++ {
		rtt, err := g.Ping("node-ping", 2*time.Second)
		if err != nil {
			t.Fatalf("第 %d 次 Ping 失败（探测通道没跑通）: %v", i+1, err)
		}
		if rtt < 0 {
			t.Fatalf("第 %d 次 RTT 为负: %v", i+1, rtt)
		}
		if rtt > 2*time.Second {
			t.Fatalf("第 %d 次 RTT 异常大: %v", i+1, rtt)
		}
	}
}

// TestPingUnknownAgent 验证对不存在的 agent 探测返回 ErrNoAgent。
func TestPingUnknownAgent(t *testing.T) {
	g := startGateway(t, testKey)
	if _, err := g.Ping("nobody", time.Second); err != ErrNoAgent {
		t.Fatalf("期望 ErrNoAgent，实际 %v", err)
	}
}

// TestResolveThroughAgent 验证「请 agent 解析域名」这条通道。
//
// 用 localhost 而不是 example.com：前者的解析不依赖外网，
// 测试可以在任何环境稳定通过；后者会受机器 DNS/出口影响而偶发失败。
// 真机的 CDN 就近验证放在联调脚本里做。
func TestResolveThroughAgent(t *testing.T) {
	g := startGateway(t, testKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startAgent(t, ctx, g.Addr(), testKey, "node-resolve")
	waitAgent(t, g, "node-resolve")

	ips, err := g.Resolve("node-resolve", "localhost", 5*time.Second)
	if err != nil {
		t.Fatalf("解析 localhost 失败: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("解析结果为空")
	}
	found := false
	for _, ip := range ips {
		if ip.IsLoopback() {
			found = true
		}
	}
	if !found {
		t.Fatalf("localhost 应解析出回环地址，实际得到 %v", ips)
	}
}

// TestResolveUnknownAgent 验证对不存在的 agent 请求解析返回 ErrNoAgent。
func TestResolveUnknownAgent(t *testing.T) {
	g := startGateway(t, testKey)
	if _, err := g.Resolve("nobody", "localhost", time.Second); err != ErrNoAgent {
		t.Fatalf("期望 ErrNoAgent，实际 %v", err)
	}
}
