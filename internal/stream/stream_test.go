package stream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"slx/internal/proto"
)

// TestConnectHandshakeRoundTrip 验证请求/响应的线格式能原样还原。
//
// 为什么单独测：握手一旦序列化错位，整个 proxy 模式的所有连接都会失败，
// 且症状是「连不上」而不是明确的解析错误，很难定位。
func TestConnectHandshakeRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	req := proto.ConnectReq{
		StreamID:  42,
		Net:       0,
		Transport: proto.ConnectTCP,
		Addr:      "104.26.12.205",
		Port:      443,
	}
	if err := WriteConnectReq(&buf, req); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}
	got, err := ReadConnectReq(&buf)
	if err != nil {
		t.Fatalf("读请求失败: %v", err)
	}
	if got != req {
		t.Fatalf("请求不一致:\n  want %+v\n  got  %+v", req, got)
	}

	resp := proto.ConnectResp{StreamID: 42, Established: true}
	if err := WriteConnectResp(&buf, resp); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
	gotResp, err := ReadConnectResp(&buf)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	if gotResp != resp {
		t.Fatalf("响应不一致: want %+v got %+v", resp, gotResp)
	}
}

// TestHandshakeRejectsWrongKind 验证收到错的帧类型会报错而不是静默错位。
func TestHandshakeRejectsWrongKind(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteConnectResp(&buf, proto.ConnectResp{Established: true}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if _, err := ReadConnectReq(&buf); err == nil {
		t.Fatal("期望报错（把 connect_resp 当 connect_req 读），实际成功")
	}
}

// TestHandshakeRejectsOversize 验证超长握手被拒绝，避免对端宣告 4GB 把内存打爆。
func TestHandshakeRejectsOversize(t *testing.T) {
	var hdr [headerLen]byte
	hdr[0] = KindConnectReq
	binary.BigEndian.PutUint32(hdr[1:], uint32(MaxHandshake+1))
	_, _, err := readMsg(bytes.NewReader(hdr[:]))
	if !errors.Is(err, ErrHandshakeTooLarge) {
		t.Fatalf("期望 ErrHandshakeTooLarge，实际 %v", err)
	}
}

// TestRelayTCPBothDirections 验证双向字节流中继。
func TestRelayTCPBothDirections(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer a1.Close()
	defer b1.Close()

	go func() { _ = RelayTCP(a1, b1) }()

	// 双向各发一段，对端应收到。
	go func() {
		_, _ = a2.Write([]byte("ping-from-a"))
	}()
	go func() {
		_, _ = b2.Write([]byte("pong-from-b"))
	}()

	bufA := make([]byte, 16)
	bufB := make([]byte, 16)
	_ = b2.SetReadDeadline(time.Now().Add(3 * time.Second))
	nA, err := b2.Read(bufA)
	if err != nil {
		t.Fatalf("b 侧读失败: %v", err)
	}
	_ = a2.SetReadDeadline(time.Now().Add(3 * time.Second))
	nB, err := a2.Read(bufB)
	if err != nil {
		t.Fatalf("a 侧读失败: %v", err)
	}
	if !strings.HasPrefix(string(bufA[:nA]), "ping-from-a") {
		t.Fatalf("b 侧收到 %q，期望 ping-from-a", string(bufA[:nA]))
	}
	if !strings.HasPrefix(string(bufB[:nB]), "pong-from-b") {
		t.Fatalf("a 侧收到 %q，期望 pong-from-b", string(bufB[:nB]))
	}
}

// TestFramedPipePreservesDatagrams 验证 UDP 中继不粘连数据报。
//
// 这是 proxy 模式 UDP 正确性的关键：两条紧挨着写的数据报如果被合并成一条，
// DNS/QUIC 之类按报文解析的协议会直接错位。
func TestFramedPipePreservesDatagrams(t *testing.T) {
	tunnelTx, tunnelRx := net.Pipe() // tunnel 两端：网关侧读、agent 侧写
	sockA, sockB := net.Pipe()       // 模拟 agent 侧的 UDP socket
	defer sockA.Close()
	defer tunnelTx.Close()

	go func() { _ = FramedPipe(tunnelTx, sockA, nil) }()

	// agent 侧 socket 写两条数据报 → 应分别成帧
	go func() {
		_, _ = sockB.Write([]byte("datagram-one"))
		time.Sleep(10 * time.Millisecond)
		_, _ = sockB.Write([]byte("datagram-two"))
	}()

	hdr := make([]byte, udpFrameHeaderLen)
	readDatagram := func() string {
		if _, err := io.ReadFull(tunnelRx, hdr); err != nil {
			t.Fatalf("读帧头失败: %v", err)
		}
		if hdr[0] != udpFrameData {
			t.Fatalf("期望数据帧，收到类型 %d", hdr[0])
		}
		n := binary.BigEndian.Uint32(hdr[2:])
		payload := make([]byte, n)
		if _, err := io.ReadFull(tunnelRx, payload); err != nil {
			t.Fatalf("读载荷失败: %v", err)
		}
		return string(payload)
	}

	_ = tunnelRx.SetReadDeadline(time.Now().Add(3 * time.Second))
	if got := readDatagram(); got != "datagram-one" {
		t.Fatalf("第一条数据报 = %q，期望 datagram-one", got)
	}
	if got := readDatagram(); got != "datagram-two" {
		t.Fatalf("第二条数据报 = %q，期望 datagram-two（粘连说明帧边界丢了）", got)
	}
}

// closeTracker 记录自己有没有被关闭。
type closeTracker struct {
	net.Conn
	closed *atomic.Bool
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// TestFramedPipeClosesTunnel 验证帧管道结束时**隧道也被关掉**。
//
// 回归：审计报告「根因 B3」—— FramedPipe 原来只关 socket，
// 隧道（yamux 流）永远不关 ⇒ 每条 UDP 四元组永久泄漏一条流 + 一个 goroutine
// （网关侧一份、agent 侧一份）。UDP 是无连接的、churn 极高 ⇒ 泄漏持续累积。
func TestFramedPipeClosesTunnel(t *testing.T) {
	tunnelTx, tunnelRx := net.Pipe()
	sockA, sockB := net.Pipe()
	defer sockB.Close()
	defer tunnelRx.Close()

	var closed atomic.Bool
	tracked := &closeTracker{Conn: tunnelTx, closed: &closed}

	done := make(chan error, 1)
	go func() { done <- FramedPipe(tracked, sockA, nil) }()

	// 对端关掉 ⇒ 帧管道的 socket→tunnel 协程读到 EOF 后退出。
	_ = sockB.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FramedPipe 没有退出")
	}

	// 隧道必须被关掉（否则 yamux 流永久泄漏）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if closed.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("FramedPipe 结束时没有关闭隧道（yamux 流会永久泄漏）")
}

// TestRelayTCPClosesBothSides 验证 **TCP 中继的行为没有变**。
//
// 这是 P0-3 的「证明 TCP 路径不变」：
// FramedPipe 改了（会关隧道），但 TCP 走的是 RelayTCP ——
// 它一直就是「两侧都关」，本次改动没有碰它。
func TestRelayTCPClosesBothSides(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer a2.Close()
	defer b2.Close()

	done := make(chan error, 1)
	go func() { done <- RelayTCP(a1, b1) }()

	// 让中继跑起来，然后关掉对端 ⇒ 中继应当结束。
	go func() {
		_, _ = a2.Write([]byte("x"))
	}()
	_ = a2.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RelayTCP 没有退出")
	}

	// a1 必须已被关闭（读立刻返回错误而不是挂住）。
	_ = a1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := a1.Read(make([]byte, 1)); err == nil {
		t.Fatal("RelayTCP 结束后应当关闭两侧")
	}
}

// TestClassifyNetError 验证「端口不可达」能被识别。
//
// 这决定网关是否回 ICMP port unreachable：识别不出来时 UDP 应用会一直挂着。
func TestClassifyNetError(t *testing.T) {
	cases := []struct {
		in   error
		want uint8
	}{
		{nil, UDPErrNone},
		{errors.New("dial udp 1.2.3.4:53: connect: connection refused"), UDPErrPortUnreachable},
		{errors.New("read udp: No connection could be made because the target machine actively refused it."), UDPErrPortUnreachable},
		{errors.New("i/o timeout"), UDPErrNone},
		{errors.New("network is unreachable"), UDPErrNone},
	}
	for _, c := range cases {
		if got := ClassifyNetError(c.in); got != c.want {
			t.Errorf("ClassifyNetError(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}
