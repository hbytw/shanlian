package netstack

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// fakeTCPRequest 是 tcpRequest 的测试替身
//
// 有了它就能直接驱动 handleTCP，不必造真实 TCP 包
type fakeTCPRequest struct {
	id       stack.TransportEndpointID
	reset    atomic.Bool
	accepted atomic.Bool
	epErr    tcpip.Error
}

func newFakeTCPRequest() *fakeTCPRequest {
	return &fakeTCPRequest{
		id: stack.TransportEndpointID{
			LocalAddress:  tcpip.AddrFromSlice([]byte{142, 251, 152, 119}),
			LocalPort:     443,
			RemoteAddress: tcpip.AddrFromSlice([]byte{192, 168, 1, 106}),
			RemotePort:    54321,
		},
	}
}

func (r *fakeTCPRequest) ID() stack.TransportEndpointID { return r.id }

func (r *fakeTCPRequest) Complete(sendReset bool) {
	r.reset.Store(sendReset)
	r.accepted.Store(!sendReset)
}

func (r *fakeTCPRequest) CreateEndpoint(*waiter.Queue) (tcpip.Endpoint, tcpip.Error) {
	if r.epErr != nil {
		return nil, r.epErr
	}
	return nil, &tcpip.ErrWouldBlock{}
}

// blockingConnector 的 ConnectTCP 会一直阻塞，直到测试放行
//
// 用来模拟「出口建连很慢」—— 这正是并发会被打满的情形
type blockingConnector struct {
	release chan struct{}
	entered atomic.Int64
}

func (c *blockingConnector) ConnectTCP(dst net.IP, port uint16) (net.Conn, bool, error) {
	c.entered.Add(1)
	<-c.release
	return &deadConn{}, true, nil
}

func (c *blockingConnector) ConnectUDP(dst net.IP, port uint16) (net.Conn, bool, error) {
	return &deadConn{}, true, nil
}

// deadConn 读写都立刻返回 EOF。
type deadConn struct{}

func (c *deadConn) Read(p []byte) (int, error)       { return 0, net.ErrClosed }
func (c *deadConn) Write(p []byte) (int, error)      { return 0, net.ErrClosed }
func (c *deadConn) Close() error                     { return nil }
func (c *deadConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *deadConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *deadConn) SetDeadline(time.Time) error      { return nil }
func (c *deadConn) SetReadDeadline(time.Time) error  { return nil }
func (c *deadConn) SetWriteDeadline(time.Time) error { return nil }

// TestActiveTCPLimit 验证并发上限真的会把超限的连接挡掉
//
// 关注点 ：
//  1. 上限之内的连接能正常建立
//  2. 超过上限的连接被拒（tcpFailLimit 增长 ）
//  3. 被拒的连接回 RST（客户端能立刻失败重试 ）
//  4. 分类互斥：被上限挡下的不算「出口连不上」
func TestActiveTCPLimit(t *testing.T) {
	conn := &blockingConnector{release: make(chan struct{})}

	s, err := New(Options{
		Connector:        conn,
		MaxActiveTCP:     4,
		UDPSweepInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("创建协议栈失败: %v", err)
	}
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleTCP(newFakeTCPRequest())
		}()
	}

	// 等前 4 条都进入阻塞状态
	deadline := time.Now().Add(5 * time.Second)
	for conn.entered.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	st := s.Stats()
	if st.TCPActive > 4 {
		t.Fatalf("在途连接数 %d 超过上限 4", st.TCPActive)
	}
	if st.TCPFailLimit == 0 {
		t.Fatalf("超限的连接应该被拒绝，但 tcpFailLimit=0（stats=%+v）", st)
	}
	if st.TCPFailConnect != 0 {
		t.Fatalf("被上限挡下的连接不该计入 tcpFailConnect，得到 %d", st.TCPFailConnect)
	}
	// 分类之和必须等于总数
	if got := st.TCPFailConnect + st.TCPFailEndpoint + st.TCPFailLimit; got != st.TCPFailed {
		t.Fatalf("失败分类之和 %d != 总失败数 %d", got, st.TCPFailed)
	}

	// 放行，让前 4 条连接结束
	close(conn.release)
	time.Sleep(500 * time.Millisecond)

	st = s.Stats()
	if st.TCPActive != 0 {
		t.Fatalf("连接结束后在途数应为 0，得到 %d", st.TCPActive)
	}
}

// TestActiveTCPLimitResets 验证超限的连接回的是 RST
//
// 静默丢弃比 RST 差得多 ：客户端会一直等到超时才重试
func TestActiveTCPLimitResets(t *testing.T) {
	conn := &blockingConnector{release: make(chan struct{})}
	s, err := New(Options{Connector: conn, MaxActiveTCP: 1})
	if err != nil {
		t.Fatalf("创建协议栈失败: %v", err)
	}
	defer s.Close()
	defer close(conn.release)

	// 第一条占住额度
	go s.handleTCP(newFakeTCPRequest())
	deadline := time.Now().Add(3 * time.Second)
	for conn.entered.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)

	// 第二条应当被拒
	req := newFakeTCPRequest()
	s.handleTCP(req)

	if !req.reset.Load() {
		t.Fatal("被并发上限挡下的连接应当回 RST，让客户端立即失败重试")
	}
	if req.accepted.Load() {
		t.Fatal("被拒的连接不能被当成已接受")
	}
}

// TestTCPFailCategories 验证 TCP 失败的三类归因互斥且完备
//
// 「线上只看到一个 tcp_fail 涨了，却不知道该查哪里」是以前最大的观测缺口
func TestTCPFailCategories(t *testing.T) {
	conn := &blockingConnector{release: make(chan struct{})}
	close(conn.release) // 立刻放行，不阻塞

	s, err := New(Options{Connector: conn, MaxActiveTCP: 8})
	if err != nil {
		t.Fatalf("创建协议栈失败: %v", err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		s.handleTCP(newFakeTCPRequest())
	}
	time.Sleep(300 * time.Millisecond)

	st := s.Stats()
	if got := st.TCPFailConnect + st.TCPFailEndpoint + st.TCPFailLimit; got != st.TCPFailed {
		t.Fatalf("失败分类之和 %d != 总失败数 %d（stats=%+v）", got, st.TCPFailed, st)
	}
	if st.TCPActive < 0 {
		t.Fatalf("在途数不能为负: %d", st.TCPActive)
	}
}

// TestEndpointFailureCategorized 验证「协议栈端点建不起来」归到 endpoint 类
func TestEndpointFailureCategorized(t *testing.T) {
	conn := &blockingConnector{release: make(chan struct{})}
	close(conn.release)

	s, err := New(Options{Connector: conn, MaxActiveTCP: 8})
	if err != nil {
		t.Fatalf("创建协议栈失败: %v", err)
	}
	defer s.Close()

	req := newFakeTCPRequest()
	req.epErr = &tcpip.ErrNoBufferSpace{}
	s.handleTCP(req)

	st := s.Stats()
	if st.TCPFailEndpoint == 0 {
		t.Fatalf("端点失败应计入 tcpFailEndpoint（stats=%+v）", st)
	}
	if st.TCPFailConnect != 0 {
		t.Fatalf("端点失败不该计入 tcpFailConnect，得到 %d", st.TCPFailConnect)
	}
}

// 确认 fakeTCPRequest 真的满足 tcpRequest 接口
var _ tcpRequest = (*fakeTCPRequest)(nil)

// 确认 gvisor 的请求类型也满足（保证 handleTCP 的调用点不用改 ）
var _ tcpRequest = (*tcp.ForwarderRequest)(nil)
