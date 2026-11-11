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
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// testClientAddr 是模拟 LAN 客户端的地址。
var testClientAddr = tcpip.AddrFrom4([4]byte{192, 168, 1, 100})

// testTargetAddr 是模拟境外目标的地址（任意公网 IP 即可，包会被代理栈拦下）。
var testTargetAddr = tcpip.AddrFrom4([4]byte{1, 1, 1, 1})

// newClientStack 建一个「模拟 LAN 客户端」的普通协议栈。
//
// 它不需要 promiscuous/spoofing：它就像一个普通主机，发出去就是发出去。
func newClientStack(t *testing.T) (*stack.Stack, *channel.Endpoint) {
	t.Helper()
	stk := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	ep := channel.New(1024, 1500, "")
	if err := stk.CreateNIC(1, ep); err != nil {
		t.Fatalf("客户端建网卡失败: %v", err)
	}
	if err := stk.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: testClientAddr.WithPrefix(),
	}, stack.AddressProperties{}); err != nil {
		t.Fatalf("客户端加地址失败: %v", err)
	}
	stk.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	t.Cleanup(stk.Destroy)
	return stk, ep
}

// pump 在客户端栈与代理栈的 channel 端点之间双向搬运 IP 包。
func pump(ctx context.Context, t *testing.T, clientEP *channel.Endpoint, proxy *Stack) {
	t.Helper()
	// 客户端 → 代理
	go func() {
		for {
			pkt := clientEP.ReadContext(ctx)
			if pkt == nil {
				return
			}
			b := packetBytes(pkt)
			pkt.DecRef()
			if len(b) > 0 {
				proxy.PushPacket(b)
			}
		}
	}()
	// 代理 → 客户端
	go func() {
		for {
			b := proxy.ReadPacket(ctx)
			if len(b) == 0 {
				return
			}
			pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
				Payload: bufferFrom(b),
			})
			clientEP.InjectInbound(ipv4.ProtocolNumber, pkb)
			pkb.DecRef()
		}
	}()
}

// tcpConnector 让所有连接都落到一个真实地址上（测试替身）。
type tcpConnector struct {
	target string
	// failWith 非 nil 时直接返回该错误，用于测试失败语义。
	failWith error
	reset    bool
}

func (c tcpConnector) ConnectTCP(net.IP, uint16) (net.Conn, bool, error) {
	if c.failWith != nil {
		return nil, c.reset, c.failWith
	}
	conn, err := net.DialTimeout("tcp", c.target, 2*time.Second)
	if err != nil {
		return nil, true, err
	}
	return conn, false, nil
}

func (c tcpConnector) ConnectUDP(net.IP, uint16) (net.Conn, bool, error) {
	if c.failWith != nil {
		return nil, c.reset, c.failWith
	}
	conn, err := net.DialTimeout("udp", c.target, 2*time.Second)
	if err != nil {
		return nil, true, err
	}
	return conn, false, nil
}

// echoServer 起一个回显 TCP 服务。
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 echo 服务失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// newTestStack 建代理栈并起泵。
func newTestStack(t *testing.T, conn Connector) *Stack {
	t.Helper()
	s, err := New(Options{
		MTU:       1500,
		Connector: conn,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("建协议栈失败: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestTCPThroughNetstack 验证完整链路：
// 客户端栈发出 SYN → 代理栈 forwarder 拦下 → Connector 连真实服务 → 双向中继。
func TestTCPThroughNetstack(t *testing.T) {
	echo := echoServer(t)

	_, portStr, _ := net.SplitHostPort(echo)
	_ = portStr

	proxy := newTestStack(t, tcpConnector{target: echo})
	clientStk, clientEP := newClientStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump(ctx, t, clientEP, proxy)

	// 从客户端栈拨「境外地址」
	addr := tcpip.FullAddress{
		NIC:  1,
		Addr: testTargetAddr,
		Port: 443,
	}
	conn, err := gonet.DialTCP(clientStk, addr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("客户端拨号失败: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello-through-gvisor")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("回显不一致: want %q got %q", msg, got)
	}

	st := proxy.Stats()
	if st.TCPAccepted != 1 {
		t.Fatalf("期望 1 条 TCP 被接受，实际 %d", st.TCPAccepted)
	}
}

// TestTCPRefusedReturnsReset 验证连不上时客户端立刻收到「连接被拒」，
// 而不是挂在 SYN 重传里。
func TestTCPRefusedReturnsReset(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errConnRefused{}}
	proxy := newTestStack(t, tcpConnector{failWith: dialErr, reset: true})
	clientStk, clientEP := newClientStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump(ctx, t, clientEP, proxy)

	addr := tcpip.FullAddress{NIC: 1, Addr: testTargetAddr, Port: 80}
	conn, err := gonet.DialTCP(clientStk, addr, ipv4.ProtocolNumber)
	if err == nil {
		conn.Close()
		t.Fatal("期望拨号失败（RST），实际成功")
	}
}

// TestUDPThroughNetstack 验证 UDP 数据报能穿透且不粘连。
func TestUDPThroughNetstack(t *testing.T) {
	// UDP 回显服务
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 UDP 服务失败: %v", err)
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

	proxy := newTestStack(t, tcpConnector{target: pc.LocalAddr().String()})
	clientStk, clientEP := newClientStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump(ctx, t, clientEP, proxy)

	raddr := tcpip.FullAddress{NIC: 1, Addr: testTargetAddr, Port: 53}
	conn, err := gonet.DialUDP(clientStk, nil, &raddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("UDP 拨号失败: %v", err)
	}
	defer conn.Close()

	for i := 0; i < 3; i++ {
		msg := []byte("dgram-" + strconv.Itoa(i))
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("UDP 第 %d 次写失败: %v", i, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 2048)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("UDP 第 %d 次读失败: %v", i, err)
		}
		if !bytes.Equal(buf[:n], msg) {
			t.Fatalf("UDP 第 %d 条不一致: want %q got %q", i, msg, buf[:n])
		}
	}
}

// errConnRefused 模拟「连接被拒」的底层错误。
type errConnRefused struct{}

func (errConnRefused) Error() string   { return "connect: connection refused" }
func (errConnRefused) Timeout() bool   { return false }
func (errConnRefused) Temporary() bool { return false }
