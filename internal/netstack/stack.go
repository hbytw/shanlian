// Package netstack 用 gvisor 的用户态协议栈在 TUN 上终结 TCP/UDP 连接。
//
// 这是 proxy 模式的核心：网关不再把 IP 包透传给出口，而是自己当「透明代理」——
// 内核转进 slx0 的包进 gvisor 协议栈，栈里每建立一条 TCP/UDP 连接，
// 就去某个出口 agent 上开一条对应的中继。
//
// 这样出口侧完全不需要 TUN / NAT / root，也不需要被网关「够得着」。
//
// 包在栈内外的流转（由调用方驱动的泵）：
//
//	TUN 读到的 IP 包 ──PushPacket──→ gvisor 协议栈 ──→ forwarder 回调
//	                                                        ↓
//	                                                  Connector.ConnectTCP
//	                                                        ↓
//	                                                  出口 agent 中继流
//
//	gvisor 产出的 IP 包 ←──ReadPacket── channel 端点（SYN-ACK、数据、FIN…）
package netstack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"slx/internal/logx"
)

// nicID 是 gvisor 里唯一网卡的编号（gvisor 要求从 1 开始）。
const nicID = 1

// channelQueue 是 channel 端点缓存的出站包数量。
//
// ★ 为什么从 256 提到 1024 ✅✅：
//
//	这条队列是 gvisor 的**出站**包缓冲 ✅✅
//	⇒ 突发时（并发拉取、页面一次开几十个连接）队列一满就**丢包** ✅⚠️
//	⇒ 丢掉的如果是 SYN-ACK ⇒ 客户端要重传 ⇒ 表现为「偶尔卡一下」✅⚠️
//
// 内存代价 ✅✅：1024 × MTU(1380) ≈ 1.4 MB 最坏情况 ✅✅
//
//	（队列按需增长，不是预分配 ✅✅）
const channelQueue = 1024

// maxInFlight 是 gvisor TCP forwarder 允许同时在途的握手数量。
//
// 超过后新 SYN 会被丢弃（不是拒绝），客户端会重传 —— 属于过载保护，
// 避免被 SYN 洪水打爆内存。
//
// ★ 它只管「握手」，不管「已建立的连接」——后者由 maxActiveTCP 管 ✅✅
const maxInFlight = 1024

// defaultMaxActiveTCP 是同时中继的 TCP 连接数默认上限。
//
// 为什么需要它 ✅✅：
//
//	gvisor 的 maxInFlight（1024）只限制**握手**在途数 ✅✅
//	⇒ 握手成功后每条连接都留着一个中继 goroutine + 一条 yamux 流 ✅⚠️
//	⇒ 突发连接（爬虫、下载器、并发拉取）能把 goroutine 数、
//	  yamux 在途流位、出口连接数一起打满 ✅⚠️
//	⇒ 后面来的正常流量被挤掉，表现为「越忙越慢、最后全超时」✅⚠️
//
// 为什么是 512 ✅✅：
//   - 家用场景正常浏览也就几十条并发 ✅✅
//   - 512 远低于「把路由器内存打爆」的数量级 ✅✅
const defaultMaxActiveTCP = 512

// Connector 由网关侧实现：把「去往 dst:port 的连接」落到某个出口 agent。
type Connector interface {
	// ConnectTCP 建立一条 TCP 中继流。
	//
	// 返回的 reset 决定客户端看到什么：
	//   reset=true  → 回 RST（端口拒绝，立即失败）
	//   reset=false → 不回包（让客户端自己超时重试）
	ConnectTCP(dst net.IP, port uint16) (conn net.Conn, reset bool, err error)

	// ConnectUDP 建立一条 UDP 中继流（流内使用长度帧保留数据报边界）。
	ConnectUDP(dst net.IP, port uint16) (conn net.Conn, reset bool, err error)
}

// Options 是协议栈构造参数。
type Options struct {
	// MTU 是隧道 MTU（必须与 TUN 设备一致，否则 gvisor 会按错误大小分片）。
	MTU uint32
	// Connector 提供到出口的连接；为 nil 时所有连接都会被静默丢弃。
	Connector Connector
	// Log 日志。
	Log *slog.Logger

	// UDPIdleTimeout 是 UDP 四元组的空闲存活时间（0 = 默认 45 秒）。
	//
	// UDP 没有「连接结束」的概念，只能靠空闲超时回收：
	// 不回收的话，每个四元组都会一直占着一条 yamux 流 + 一个 goroutine
	// + 一个 NAT 映射（审计报告「根因 B3」）。
	UDPIdleTimeout time.Duration

	// UDPSweepInterval 是 UDP 四元组回收扫描周期（0 = 默认 5 秒）。
	UDPSweepInterval time.Duration

	// MaxActiveTCP 是同时中继的 TCP 连接数上限（0 = 默认 512）。
	//
	// 为什么需要它 ✅✅：
	//   gvisor 的 maxInFlight（1024）只限制**握手**在途数 ✅✅
	//   ⇒ 握手成功后每条连接都留着一个中继 goroutine ✅⚠️
	//   ⇒ 突发连接（爬虫、下载器、并发拉取）可以把 goroutine 数、
	//     yamux 在途流位、出口连接数一起打满 ✅⚠️
	//   ⇒ 后面来的正常流量被挤掉，表现为「越忙越慢、最后全超时」✅⚠️
	//
	// 超限时的行为 ✅✅：**回 RST** ✅✅
	//   ⇒ 客户端立刻失败并换源端口重试 ✅✅
	//   ⇒ 比「静默丢弃」好：静默丢弃会让客户端一直等到超时 ✅⚠️
	MaxActiveTCP int
}

// Stack 是网关侧的 gvisor 协议栈。
type Stack struct {
	stk       *stack.Stack
	ep        *channel.Endpoint
	connector Connector
	log       *slog.Logger

	closed atomic.Bool
	wg     sync.WaitGroup
	// udpWg 统计 UDP 四元组的建连+中继 goroutine。
	//
	// 单独一个 WaitGroup：UDP 的建连可能卡在出口上（最长十几秒），
	// 退出不能被它无限期拖住 ⇒ Close 里只等一个上限。
	udpWg sync.WaitGroup

	// udpMu / udpFlows 是 UDP 四元组表。
	//
	// 为什么要自己维护一张表：gvisor 的 demux 只知道「有没有端点」，
	// 不知道「这个四元组刚才建连失败过、正在冷却」。
	// 没有这张表的话，同一个四元组的每个包都会重新触发一次建连
	// （= 按包放大，审计报告「根因 A」连带问题 B2）。
	udpMu    sync.Mutex
	udpFlows map[udpFlowKey]*udpFlow

	// udpIdle / udpSweep 是 UDP 四元组的回收参数。
	udpIdle  time.Duration
	udpSweep time.Duration
	// udpStop 关闭空闲回收协程（Close 时关）。
	udpStop     chan struct{}
	udpStopOnce sync.Once

	tcpAccepted atomic.Int64
	tcpFailed   atomic.Int64
	udpAccepted atomic.Int64
	udpFail     atomic.Int64
	udpDropped  atomic.Int64

	// tcpActive 是当前在途的 TCP 中继连接数。
	tcpActive atomic.Int64
	// tcpFailConnect / tcpFailEndpoint / tcpFailLimit 把 TCP 失败拆开归因。
	//
	// 为什么必须拆开 ✅✅：以前只有一个 tcpFailed ⚠️
	//   ⇒ 「出口连不上」「协议栈起不来端点」「并发超限」三种完全不同的事
	//     混在一个数字里 ⚠️
	//   ⇒ 线上看到 tcpFail 涨了根本不知道该查哪里 ✅⚠️
	//   ⇒ 而这三者的处置完全不同 ✅✅：
	//     connect  ⇒ 查出口/选路 ✅✅
	//     endpoint ⇒ 查协议栈 ✅✅
	//     limit    ⇒ 查并发/正常负载高峰 ✅✅
	tcpFailConnect  atomic.Int64
	tcpFailEndpoint atomic.Int64
	tcpFailLimit    atomic.Int64

	// tcpBytes / udpBytes 是中继的字节数（双向合计）。
	tcpBytes atomic.Int64
	udpBytes atomic.Int64

	// maxActiveTCP 是同时中继的 TCP 连接数上限。
	maxActiveTCP int64
}

// Stats 是协议栈计数快照。
type Stats struct {
	TCPAccepted int64
	TCPFailed   int64
	UDPAccepted int64
	// UDPFailed 是 UDP 建连失败次数（出口连不上 / 端点建不起来）。
	UDPFailed int64
	// UDPDropped 是因为「四元组已知且正在冷却」而被丢掉的 UDP 包数。
	UDPDropped int64

	// TCPFailConnect / TCPFailEndpoint / TCPFailLimit 是 TCP 失败的分类归因。
	// 三者之和等于 TCPFailed ✅✅。
	TCPFailConnect  int64
	TCPFailEndpoint int64
	TCPFailLimit    int64
	// TCPActive 是当前在途的 TCP 中继连接数。
	TCPActive int64
	// TCPBytes / UDPBytes 是中继的字节数（双向合计）。
	TCPBytes int64
	UDPBytes int64
}

// New 创建协议栈。
func New(o Options) (*Stack, error) {
	mtu := o.MTU
	if mtu == 0 {
		mtu = 1380
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Connector == nil {
		o.Connector = discardConnector{}
	}

	s := &Stack{connector: o.Connector, log: o.Log, udpFlows: make(map[udpFlowKey]*udpFlow)}

	// UDP 回收参数（0 = 用默认值）。
	s.udpIdle = o.UDPIdleTimeout
	if s.udpIdle <= 0 {
		s.udpIdle = defaultUDPIdleTimeout
	}
	s.udpSweep = o.UDPSweepInterval
	if s.udpSweep <= 0 {
		s.udpSweep = defaultUDPSweepInterval
	}
	s.udpStop = make(chan struct{})

	// 并发上限（0 = 用默认值）。
	s.maxActiveTCP = int64(o.MaxActiveTCP)
	if s.maxActiveTCP <= 0 {
		s.maxActiveTCP = defaultMaxActiveTCP
	}

	stk := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			// ★ 用 CUBIC 而不是默认的 Reno ✅✅
			//
			// 为什么 ✅✅：跨境链路是**高 BDP**（大带宽 × 长延迟）✅✅
			//   Reno 在高 BDP 上窗口增长很慢 ⇒ 长期跑不满带宽 ✅⚠️
			//   CUBIC 按 cubic 函数增长窗口 ⇒ 在高 BDP 上明显更快 ✅✅
			//
			// 实测背景 ✅✅：原始 TCP 44.4 Mbps，SLX 端到端只有 16.9 Mbps ✅⚠️
			//   ⇒ 这段差距很可能就出在拥塞控制上 ✅✅
			tcp.NewProtocolCUBIC,
			udp.NewProtocol,
		},
	})
	s.stk = stk

	ep := channel.New(channelQueue, mtu, "")
	s.ep = ep
	if err := stk.CreateNIC(nicID, ep); err != nil {
		return nil, errors.New("netstack: 创建网卡失败: " + err.String())
	}

	// 关键：透明代理必须开这两个。
	//   PromiscuousMode：接收目的地址不属于本网卡的包（LAN 客户端连的是境外 IP）；
	//   Spoofing：允许以任意源地址发包（回给客户端的包里源地址是那个境外 IP）。
	// 不开的话表现是「SYN 进不来」或「SYN-ACK 发不出去」，且没有任何错误日志。
	stk.SetPromiscuousMode(nicID, true)
	stk.SetSpoofing(nicID, true)

	stk.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	// 转发要关掉：我们不做 IP 路由，gvisor 只负责终结连接。
	stk.SetForwardingDefaultAndAllNICs(ipv4.ProtocolNumber, false)
	stk.SetForwardingDefaultAndAllNICs(ipv6.ProtocolNumber, false)

	// 开 SACK 提升吞吐（丢包环境下差别明显）。
	sack := tcpip.TCPSACKEnabled(true)
	stk.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)

	// TCP 转发器：每个 SYN 触发一次回调，由我们决定连到哪个出口。
	//
	// ★ 这里要包一层 ✅✅：gvisor 的 NewForwarder 要的是
	//   func(*tcp.ForwarderRequest) 这个**精确签名** ✅⚠️
	//   而 handleTCP 收的是 tcpRequest 接口 ✅✅（为了可测 ✅✅）
	//   ⇒ 不包一层就编译不过 ✅⚠️
	tcpFwd := tcp.NewForwarder(stk, 0, maxInFlight, func(req *tcp.ForwarderRequest) {
		s.handleTCP(req)
	})
	stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP 转发器：每个新的四元组触发一次。
	udpFwd := udp.NewForwarder(stk, s.handleUDP)
	stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	// 空闲回收协程：只关掉长时间没有数据流动的 UDP 流。
	go s.sweepUDPFlows()

	return s, nil
}

// udpIdleTimeout 返回 UDP 四元组的空闲存活时间。
func (s *Stack) udpIdleTimeout() time.Duration { return s.udpIdle }

// udpSweepInterval 返回 UDP 四元组回收扫描周期。
func (s *Stack) udpSweepInterval() time.Duration { return s.udpSweep }

// Endpoint 返回 channel 端点（供调用方取包写回 TUN）。
func (s *Stack) Endpoint() *channel.Endpoint { return s.ep }

// PushPacket 把从 TUN 读到的一个 IP 包送进协议栈。
//
// 返回值表示是否被协议栈接收（false 时调用方可以记「坏包」计数）。
func (s *Stack) PushPacket(pkt []byte) bool {
	if s.closed.Load() || len(pkt) == 0 {
		return false
	}
	var proto tcpip.NetworkProtocolNumber
	switch pkt[0] >> 4 {
	case 4:
		proto = ipv4.ProtocolNumber
	case 6:
		proto = ipv6.ProtocolNumber
	default:
		return false
	}
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(pkt),
	})
	defer pkb.DecRef()
	s.ep.InjectInbound(proto, pkb)
	return true
}

// ReadPacket 取出协议栈待发出的一个 IP 包；没有则返回 nil。
//
// ctx 取消时立即返回，用于让泵在退出时不被 ReadContext 卡住。
func (s *Stack) ReadPacket(ctx context.Context) []byte {
	if s.closed.Load() {
		return nil
	}
	pkt := s.ep.ReadContext(ctx)
	if pkt == nil {
		return nil
	}
	defer pkt.DecRef()
	return packetBytes(pkt)
}

// Stats 返回计数快照。
func (s *Stack) Stats() Stats {
	return Stats{
		TCPAccepted:     s.tcpAccepted.Load(),
		TCPFailed:       s.tcpFailed.Load(),
		UDPAccepted:     s.udpAccepted.Load(),
		UDPFailed:       s.udpFail.Load(),
		UDPDropped:      s.udpDropped.Load(),
		TCPFailConnect:  s.tcpFailConnect.Load(),
		TCPFailEndpoint: s.tcpFailEndpoint.Load(),
		TCPFailLimit:    s.tcpFailLimit.Load(),
		TCPActive:       s.tcpActive.Load(),
		TCPBytes:        s.tcpBytes.Load(),
		UDPBytes:        s.udpBytes.Load(),
	}
}

// Close 销毁协议栈，并等待在途中继结束。
func (s *Stack) Close() {
	if s.closed.Swap(true) {
		return
	}
	// 先关掉全部 UDP 四元组：不关的话，中继 goroutine 会一直挂在
	// FramedPipe 的读上，下面的 wg.Wait() 永远等不到它们 ⇒ 退出卡住。
	s.closeAllUDPFlows()
	// 停掉空闲回收协程。
	s.udpStopOnce.Do(func() { close(s.udpStop) })
	s.stk.Destroy()
	s.wg.Wait()
	s.waitUDPFlows(udpCloseWait)
}

// tcpRequest 是 handleTCP 需要的最小接口 ✅✅
//
// 为什么要抽这一层 ✅✅：
//
//	gvisor 的 tcp.ForwarderRequest 没有公开构造器 ✅⚠️
//	⇒ 直接测 handleTCP 只能靠造真实 TCP 包打进协议栈 ✅⚠️（慢且脆 ✅⚠️）
//	⇒ 抽一个薄接口后，测试可以直接喂假请求 ✅✅
//	⇒ *tcp.ForwarderRequest 天然满足它 ✅✅（不用改 gvisor ✅✅）
type tcpRequest interface {
	ID() stack.TransportEndpointID
	Complete(sendReset bool)
	CreateEndpoint(queue *waiter.Queue) (tcpip.Endpoint, tcpip.Error)
}

// handleTCP 处理一个进入的 TCP 连接。
//
// 顺序很关键：**必须先拿到出口连接再 Complete**。
// 如果先 Complete（接受连接）再去连出口，客户端会看到 SYN-ACK 之后
// 长时间无响应；出口连不上时更糟 —— 客户端以为连上了却永远收不到数据。
func (s *Stack) handleTCP(req tcpRequest) {
	id := req.ID()
	port := id.LocalPort
	addr := net.IP(id.LocalAddress.AsSlice())

	// ★ 并发闸门 ✅✅
	// 先占位再干活：超限直接 RST，让客户端立刻换端口重试 ✅✅
	// （静默丢弃的话客户端会一直等到超时 ✅⚠️ —— 那比拒绝更糟 ✅✅）
	if n := s.tcpActive.Add(1); n > s.maxActiveTCP {
		s.tcpActive.Add(-1)
		s.tcpFailLimit.Add(1)
		s.tcpFailed.Add(1)
		s.log.Debug("TCP 并发超限",
			logx.FieldEvent, "netstack.tcp_limit",
			logx.FieldTarget, addr.String(),
			"port", port,
			"active", n-1,
			"limit", s.maxActiveTCP)
		req.Complete(true)
		return
	}

	conn, reset, err := s.connector.ConnectTCP(addr, port)
	if err != nil {
		s.tcpActive.Add(-1)
		s.tcpFailConnect.Add(1)
		s.tcpFailed.Add(1)
		// reset=true → 回 RST，客户端立即失败；false → 静默，客户端超时重试
		req.Complete(reset)
		s.log.Debug("TCP 建连失败",
			logx.FieldEvent, "netstack.tcp_failed",
			logx.FieldTarget, addr.String(),
			"port", port, "reset", reset,
			logx.FieldErr, err.Error())
		return
	}

	var wq waiter.Queue
	tcpEp, tcpErr := req.CreateEndpoint(&wq)
	if tcpErr != nil {
		_ = conn.Close()
		s.tcpActive.Add(-1)
		s.tcpFailEndpoint.Add(1)
		s.tcpFailed.Add(1)
		req.Complete(true)
		return
	}
	req.Complete(false) // 接受连接：gvisor 据此回 SYN-ACK
	s.tcpAccepted.Add(1)

	local := gonet.NewTCPConn(&wq, tcpEp)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// 计数在这里还回去：中继结束 = 这条连接不再占资源 ✅✅
		defer s.tcpActive.Add(-1)
		relayBothCounted(local, conn, &s.tcpBytes)
	}()
}

// handleUDP 处理一个进入的 UDP 四元组。
//
// 实现在 udp.go —— 它必须与 TCP 的处理彻底分开：
// gvisor 的 UDP forwarder 是**同步回调**
// （pkg/tcpip/transport/udp/forwarder.go:47 直接调 f.handler(...)），
// 而 TCP forwarder 是 `go f.handler(...)`（异步）。
// ⇒ 这里任何阻塞都会占着 TUN 读协程 ⇒ 整个数据面停摆
// （审计报告「根因 A」：并发 5 个请求全超时、串行单请求正好一个 RTT）。
func (s *Stack) handleUDP(req *udp.ForwarderRequest) {
	s.handleUDPFlow(req)
}

// relayBoth 在本地 gvisor 连接与出口中继流之间双向拷贝。
func relayBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
}

// relayBothCounted 是 relayBoth 的带计数版本 ✅✅
//
// 为什么要计字节 ✅✅：
//
//	之前只有「连接数」和「失败数」✅⚠️
//	⇒ 线上只能看到「连了多少条」，看不到「搬了多少数据」✅⚠️
//	⇒ 排查「是不是真的在走代理」「带宽打在哪条流上」时毫无抓手 ✅⚠️
//	⇒ 字节数是判断「流量到底有没有走代理」最直接的证据 ✅✅
func relayBothCounted(a, b net.Conn, counter *atomic.Int64) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		n, _ := io.Copy(dst, src)
		if counter != nil && n > 0 {
			counter.Add(n)
		}
		_ = dst.Close()
		_ = src.Close()
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
}

// packetBytes 把 PacketBuffer 拼成一个连续字节切片（复制）。
//
// 必须复制：PacketBuffer 的视图在 DecRef 之后失效，直接引用会读到已释放内存。
func packetBytes(pkt *stack.PacketBuffer) []byte {
	slices := pkt.AsSlices()
	n := 0
	for _, s := range slices {
		n += len(s)
	}
	if n == 0 {
		return nil
	}
	out := make([]byte, 0, n)
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

// bufferFrom 是构造 PacketBuffer 载荷的薄封装（测试也用得到）。
func bufferFrom(b []byte) buffer.Buffer { return buffer.MakeWithData(b) }

// discardConnector 是空实现：所有连接都拒绝（用于只测协议栈本身）。
type discardConnector struct{}

func (discardConnector) ConnectTCP(net.IP, uint16) (net.Conn, bool, error) {
	return nil, true, errors.New("netstack: 未配置 Connector")
}

func (discardConnector) ConnectUDP(net.IP, uint16) (net.Conn, bool, error) {
	return nil, true, errors.New("netstack: 未配置 Connector")
}
