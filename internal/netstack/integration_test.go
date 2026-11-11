package netstack

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"

	"slx/internal/proto"
	"slx/internal/relay"
)

// relayConnector 是「走真实 relay 链路」的 Connector：
// 把 netstack 提出的目标地址替换成本地 echo 服务，但**握手、帧格式、
// 中继全部走真实的 relay.Gateway → relay.Agent 路径**。
//
// 为什么不用一个简单的直连 Dialer：那样会绕过 relay 的流握手与 UDP 帧格式，
// 一旦两端帧头定义不一致（实测发生过：一端 4 字节长度头、另一端 6 字节帧头），
// 单元测试仍然全绿，但真机上 UDP 永远超时。
type relayConnector struct {
	gw        *relay.Gateway
	nodeID    string
	tcpTarget string
	udpTarget string
}

func (c *relayConnector) open(network string, transport uint8) (net.Conn, bool, error) {
	target := c.tcpTarget
	if transport == proto.ConnectUDP {
		target = c.udpTarget
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, true, err
	}
	port, _ := strconv.Atoi(portStr)
	conn, oerr := c.gw.Open(c.nodeID, proto.ConnectReq{
		Net:       0,
		Transport: transport,
		Addr:      host,
		Port:      uint16(port),
	})
	if oerr != nil {
		return nil, true, oerr
	}
	return conn, false, nil
}

func (c *relayConnector) ConnectTCP(net.IP, uint16) (net.Conn, bool, error) {
	return c.open("tcp", proto.ConnectTCP)
}

func (c *relayConnector) ConnectUDP(net.IP, uint16) (net.Conn, bool, error) {
	return c.open("udp", proto.ConnectUDP)
}

// startRelayPair 起一对真实的 gateway + agent，返回指向它的 connector。
func startRelayPair(t *testing.T, tcpTarget, udpTarget string) *relayConnector {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	key := bytes.Repeat([]byte{0x33}, 32)

	gw := relay.NewGateway(relay.GatewayOptions{Key: key, Addr: "127.0.0.1:0", Log: log})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); gw.Close() })
	go func() { _ = gw.Run(ctx) }()

	// 等监听就绪
	deadline := time.Now().Add(3 * time.Second)
	for gw.Addr() == "127.0.0.1:0" {
		if time.Now().After(deadline) {
			t.Fatal("网关未就绪")
		}
		time.Sleep(10 * time.Millisecond)
	}

	const nodeID = "test-agent"
	agent := relay.NewAgent(relay.AgentOptions{
		Key:         key,
		GatewayAddr: gw.Addr(),
		NodeID:      nodeID,
		Log:         log,
	})
	go func() { _ = agent.Run(ctx) }()

	deadline = time.Now().Add(5 * time.Second)
	for !gw.HasAgent(nodeID) {
		if time.Now().After(deadline) {
			t.Fatal("agent 未接入")
		}
		time.Sleep(20 * time.Millisecond)
	}

	return &relayConnector{gw: gw, nodeID: nodeID, tcpTarget: tcpTarget, udpTarget: udpTarget}
}

// TestUDPThroughFullRelayChain 验证 UDP 走完
// 「gvisor → Connector → Gateway.Open → yamux → Agent.FramedPipe → 真实 UDP socket」
// 的整条链路，且数据报不粘连。
//
// 这个测试是补的：之前 netstack 自己实现了一套 UDP 帧（4 字节长度头），
// 而 agent 侧用 stream 包的 6 字节帧头，两端不一致导致真机 UDP 全部超时。
// 只测 netstack 一层发现不了，必须贯穿 relay。
func TestUDPThroughFullRelayChain(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 UDP 回显失败: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, rerr := pc.ReadFrom(buf)
			if rerr != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()

	conn := startRelayPair(t, "", pc.LocalAddr().String())
	proxy := newTestStack(t, conn)
	clientStk, clientEP := newClientStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump(ctx, t, clientEP, proxy)

	raddr := tcpip.FullAddress{NIC: 1, Addr: testTargetAddr, Port: 53}
	uc, err := gonet.DialUDP(clientStk, nil, &raddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("UDP 拨号失败: %v", err)
	}
	defer uc.Close()

	for i := 0; i < 3; i++ {
		msg := []byte("payload-" + strconv.Itoa(i))
		if _, werr := uc.Write(msg); werr != nil {
			t.Fatalf("第 %d 次写失败: %v", i, werr)
		}
		_ = uc.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 2048)
		n, rerr := uc.Read(buf)
		if rerr != nil {
			t.Fatalf("第 %d 次读失败（UDP 帧格式不一致时会卡在这里）: %v", i, rerr)
		}
		if !bytes.Equal(buf[:n], msg) {
			t.Fatalf("第 %d 条数据报不一致: want %q got %q", i, msg, buf[:n])
		}
	}
}

// TestTCPThroughFullRelayChain 同上，TCP 走完整 relay 链路。
func TestTCPThroughFullRelayChain(t *testing.T) {
	echo := echoServer(t)
	conn := startRelayPair(t, echo, "")

	proxy := newTestStack(t, conn)
	clientStk, clientEP := newClientStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump(ctx, t, clientEP, proxy)

	addr := tcpip.FullAddress{NIC: 1, Addr: testTargetAddr, Port: 443}
	c, err := gonet.DialTCP(clientStk, addr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("TCP 拨号失败: %v", err)
	}
	defer c.Close()

	msg := []byte("full-chain-tcp")
	if _, werr := c.Write(msg); werr != nil {
		t.Fatalf("写失败: %v", werr)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(msg))
	if _, rerr := io.ReadFull(c, got); rerr != nil {
		t.Fatalf("读回显失败: %v", rerr)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("回显不一致: want %q got %q", msg, got)
	}
}
