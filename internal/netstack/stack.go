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
	"slx/internal/stream"
)

// nicID 是 gvisor 里唯一网卡的编号（gvisor 要求从 1 开始）。
const nicID = 1

// channelQueue 是 channel 端点缓存的出站包数量。
//
// 太小会在突发时丢 SYN-ACK；太大会吃内存。256 足够覆盖家用路由器的瞬间并发。
const channelQueue = 256

// maxInFlight 是 gvisor TCP forwarder 允许同时在途的握手数量。
//
// 超过后新 SYN 会被丢弃（不是拒绝），客户端会重传 —— 属于过载保护，
// 避免被 SYN 洪水打爆内存。
const maxInFlight = 1024

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
}

// Stack 是网关侧的 gvisor 协议栈。
type Stack struct {
	stk       *stack.Stack
	ep        *channel.Endpoint
	connector Connector
	log       *slog.Logger

	closed atomic.Bool
	wg     sync.WaitGroup

	tcpAccepted atomic.Int64
	tcpFailed   atomic.Int64
	udpAccepted atomic.Int64
}

// Stats 是协议栈计数快照。
type Stats struct {
	TCPAccepted int64
	TCPFailed   int64
	UDPAccepted int64
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

	s := &Stack{connector: o.Connector, log: o.Log}

	stk := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
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
	tcpFwd := tcp.NewForwarder(stk, 0, maxInFlight, s.handleTCP)
	stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP 转发器：每个新的四元组触发一次。
	udpFwd := udp.NewForwarder(stk, s.handleUDP)
	stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	return s, nil
}

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
		TCPAccepted: s.tcpAccepted.Load(),
		TCPFailed:   s.tcpFailed.Load(),
		UDPAccepted: s.udpAccepted.Load(),
	}
}

// Close 销毁协议栈，并等待在途中继结束。
func (s *Stack) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.stk.Destroy()
	s.wg.Wait()
}

// handleTCP 处理一个进入的 TCP 连接。
//
// 顺序很关键：**必须先拿到出口连接再 Complete**。
// 如果先 Complete（接受连接）再去连出口，客户端会看到 SYN-ACK 之后
// 长时间无响应；出口连不上时更糟 —— 客户端以为连上了却永远收不到数据。
func (s *Stack) handleTCP(req *tcp.ForwarderRequest) {
	id := req.ID()
	port := id.LocalPort
	addr := net.IP(id.LocalAddress.AsSlice())

	conn, reset, err := s.connector.ConnectTCP(addr, port)
	if err != nil {
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
		relayBoth(local, conn)
	}()
}

// handleUDP 处理一个进入的 UDP 四元组。
func (s *Stack) handleUDP(req *udp.ForwarderRequest) {
	id := req.ID()
	port := id.LocalPort
	addr := net.IP(id.LocalAddress.AsSlice())

	conn, _, err := s.connector.ConnectUDP(addr, port)
	if err != nil {
		s.log.Debug("UDP 建连失败",
			logx.FieldEvent, "netstack.udp_failed",
			logx.FieldTarget, addr.String(), "port", port,
			logx.FieldErr, err.Error())
		return // UDP 没有 RST 语义，静默丢弃
	}

	var wq waiter.Queue
	udpEp, udpErr := req.CreateEndpoint(&wq)
	if udpErr != nil {
		_ = conn.Close()
		return
	}
	s.udpAccepted.Add(1)

	local := gonet.NewUDPConn(&wq, udpEp)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// 必须用 stream.FramedPipe：它是网关与 agent 共用的帧格式实现。
		// 曾经这里自己写了一套 4 字节长度头，而 agent 侧用的是 stream 包的
		// 6 字节帧头（类型+错误码+长度），两端格式不一致导致 UDP 永远超时。
		_ = stream.FramedPipe(conn, local, stream.ClassifyNetError)
	}()
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
