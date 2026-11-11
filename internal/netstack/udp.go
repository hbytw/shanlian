package netstack

// UDP 四元组的生命周期管理。
//
// # 为什么单独一个文件
//
// UDP 与 TCP 在协议栈里的处理完全不同：
//
//   - TCP 有握手、有 RST 语义、每个连接只建一次端点；
//   - UDP 无连接、每个四元组只建一次「连」，但**每个包**都要过一遍 demux。
//
// 更要命的是：gvisor 的 UDP forwarder 是**同步回调**
// （pkg/tcpip/transport/udp/forwarder.go:47 直接调 f.handler(...)），
// 而 TCP forwarder 是 `go f.handler(...)`（异步）。
//
// ⇒ handleUDP 里任何阻塞都会占着 TUN 读协程 ⇒ 整个数据面停摆。
// （审计报告「根因 A」：并发 5 个请求全超时、串行单请求正好一个 RTT。）

import (
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"slx/internal/logx"
	"slx/internal/stream"
)

// —— UDP 四元组的参数 ——

const (
	// udpIdleTimeout 是 UDP 四元组的空闲存活时间。
	//
	// UDP 没有「连接结束」这个概念：客户端可能再也不发包了，
	// 但我们这边还占着一条 yamux 流 + 一个 goroutine + 一个 NAT 映射。
	// ⇒ 空闲超过这个时间就回收。
	//
	// 45 秒的取舍：短到能挡住 NAT 表被撑爆，长到不会误杀
	// 「安静但正常」的 UDP 会话（QUIC 之类的应用自己会发保活）。
	udpIdleTimeout = 45 * time.Second

	// udpFailCooldown 是 UDP 建连失败后的冷却时间。
	//
	// 失败时必须冷却：否则同一个四元组的**每一个包**都会重新触发一次
	// 建连尝试，而每次尝试都可能阻塞好几秒（= 按包放大，审计报告「根因 B2」）。
	udpFailCooldown = 10 * time.Second

	// udpSweepInterval 是 UDP 四元组回收扫描的周期。
	udpSweepInterval = 5 * time.Second

	// maxUDPFlows 是同时存活的 UDP 四元组数量上限。
	//
	// 家用路由器的 UDP 四元组 churn 很高（手机心跳、QUIC、STUN…），
	// 没有上限的话表会无限增长。
	maxUDPFlows = 4096

	// udpCloseWait 是退出时等待 UDP 四元组 goroutine 的上限。
	//
	// 为什么要上限：UDP 建连可能卡在出口上（等 yamux ACK，最长十几秒）。
	// 进程退出不能被它无限期拖住 —— 剩下的 goroutine 看到栈已关闭
	// 会自己收工（runUDPFlow 里检查 s.closed）。
	udpCloseWait = 3 * time.Second
)

// udpFlowState 是 UDP 四元组的状态。
type udpFlowState int32

const (
	// udpFlowConnecting：端点已建好，出口连接还在建。
	udpFlowConnecting udpFlowState = iota
	// udpFlowActive：已连通，正在中继数据报。
	udpFlowActive
	// udpFlowFailed：建连失败，冷却期内不再重试。
	udpFlowFailed
)

// udpFlowKey 标识一个 UDP 四元组。
//
// 注意方向：gvisor 的 forwarder 给的 ID 里，
// Local = **目的**地址（境外 IP），Remote = **来源**地址（LAN 客户端）。
// 因为协议栈在中间终结连接，客户端看到的是「连上了那个境外 IP」。
type udpFlowKey struct {
	local      netip.Addr
	localPort  uint16
	remote     netip.Addr
	remotePort uint16
}

// udpFlowKeyOf 把 gvisor 的四元组 ID 转成可比较的键。
func udpFlowKeyOf(id stack.TransportEndpointID) (udpFlowKey, bool) {
	local, ok := netip.AddrFromSlice(id.LocalAddress.AsSlice())
	if !ok {
		return udpFlowKey{}, false
	}
	remote, ok := netip.AddrFromSlice(id.RemoteAddress.AsSlice())
	if !ok {
		return udpFlowKey{}, false
	}
	return udpFlowKey{
		local:      local.Unmap(),
		localPort:  id.LocalPort,
		remote:     remote.Unmap(),
		remotePort: id.RemotePort,
	}, true
}

// udpFlow 是一个 UDP 四元组的运行时状态。
type udpFlow struct {
	key udpFlowKey

	// state 是当前状态（原子读写）。
	state atomic.Int32
	// expireAt 是失败冷却的到期时间（Unix 纳秒）。
	expireAt atomic.Int64
	// lastActive 是最后一次有数据流动的时间（Unix 纳秒）。
	lastActive atomic.Int64

	mu sync.Mutex
	// wq 是端点的等待队列。端点持有它的指针，所以它必须活到端点关闭。
	wq *waiter.Queue
	// local 是本地 UDP 端点（对客户端那一侧）。
	local *gonet.UDPConn
	// conn 是出口侧的中继连接（建连成功后才有）。
	conn net.Conn
	// closed 标记这个四元组已经收尾。
	closed bool
}

// touch 记录一次数据流动（用于空闲回收）。
func (f *udpFlow) touch() {
	f.lastActive.Store(time.Now().UnixNano())
}

// handleUDP 处理一个进入的 UDP 四元组。
//
// # 三条硬要求（都是现场事故换来的）
//
//  1. **绝不能在这里阻塞**：gvisor 的 UDP forwarder 是同步回调，
//     阻塞 = 占着 TUN 读协程 = 整个数据面停摆（审计报告「根因 A」）。
//     ⇒ 出口建连放到 goroutine。
//  2. **必须立刻建端点**：端点一注册进 gvisor 的 demux，
//     同一个四元组的后续包就不会再回到 forwarder，
//     而是直接进端点的接收队列 —— 这就消掉了「按包放大」
//     （失败时不建端点 ⇒ demux 找不到匹配端点 ⇒ 每个包重新阻塞一次）。
//  3. **失败要冷却**：冷却期内同一个四元组的包直接丢弃，
//     不重新触发建连（否则又是一轮按包放大）。
func (s *Stack) handleUDPFlow(req *udp.ForwarderRequest) {
	id := req.ID()
	key, ok := udpFlowKeyOf(id)
	if !ok {
		return
	}
	port := id.LocalPort
	addr := net.IP(id.LocalAddress.AsSlice())

	now := time.Now()

	// —— 登记四元组 ——
	s.udpMu.Lock()
	f, exists := s.udpFlows[key]
	if exists {
		if f.state.Load() == int32(udpFlowFailed) && now.UnixNano() > f.expireAt.Load() {
			// 冷却期过了 ⇒ 允许重新建连（摘掉旧记录）。
			delete(s.udpFlows, key)
			exists = false
		} else {
			// 已知四元组：
			//   · 正在建连/已连通 ⇒ 这个包本该已经被 demux 投给端点，
			//     走到这里说明端点刚被关掉，丢弃即可；
			//   · 失败冷却中 ⇒ 丢弃，**绝不重新建连**。
			s.udpMu.Unlock()
			s.udpDropped.Add(1)
			return
		}
	}
	if !exists {
		if len(s.udpFlows) >= maxUDPFlows*3/4 {
			s.pruneUDPFlowsLocked(now)
		}
		if len(s.udpFlows) >= maxUDPFlows {
			s.udpMu.Unlock()
			s.udpFail.Add(1)
			s.log.Warn("UDP 四元组数量超限，丢弃新的 UDP 流",
				logx.FieldEvent, "netstack.udp_overflow",
				"limit", maxUDPFlows)
			return
		}
		f = &udpFlow{key: key}
		f.state.Store(int32(udpFlowConnecting))
		f.touch()
		s.udpFlows[key] = f
	}
	s.udpMu.Unlock()

	// —— 建端点（不阻塞：只是 gvisor 内存里的登记）——
	wq := &waiter.Queue{}
	udpEp, udpErr := req.CreateEndpoint(wq)
	if udpErr != nil {
		s.udpFail.Add(1)
		s.failUDPFlow(f)
		s.log.Debug("UDP 建端点失败",
			logx.FieldEvent, "netstack.udp_endpoint_failed",
			logx.FieldTarget, addr.String(), "port", port,
			logx.FieldErr, udpErr.String())
		return
	}
	f.mu.Lock()
	f.wq = wq
	f.local = gonet.NewUDPConn(wq, udpEp)
	f.mu.Unlock()

	// —— 出口建连放到 goroutine ——
	//
	// 这是整个修复的核心：TUN 读协程到此为止，绝不等待出口。
	s.udpWg.Add(1)
	go func() {
		defer s.udpWg.Done()
		s.runUDPFlow(f, addr, port)
	}()
}

// runUDPFlow 建连并中继一个 UDP 四元组。
//
// 它跑在独立 goroutine 里（TUN 读协程绝不阻塞在这里）。
func (s *Stack) runUDPFlow(f *udpFlow, addr net.IP, port uint16) {
	conn, _, err := s.connector.ConnectUDP(addr, port)
	if err != nil {
		s.udpFail.Add(1)
		s.log.Debug("UDP 建连失败",
			logx.FieldEvent, "netstack.udp_failed",
			logx.FieldTarget, addr.String(), "port", port,
			logx.FieldErr, err.Error())
		s.failUDPFlow(f)
		return
	}

	// 栈已经关了：别再碰端点，直接收工。
	if s.closed.Load() {
		_ = conn.Close()
		return
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		_ = conn.Close()
		return
	}
	f.conn = conn
	local := f.local
	f.mu.Unlock()

	f.state.Store(int32(udpFlowActive))
	s.udpAccepted.Add(1)

	// 必须用 stream.FramedPipe：它是网关与 agent 共用的帧格式实现。
	// 曾经这里自己写了一套 4 字节长度头，而 agent 侧用的是 stream 包的
	// 6 字节帧头（类型+错误码+长度），两端格式不一致导致 UDP 永远超时。
	_ = stream.FramedPipe(conn, local, stream.ClassifyNetError)

	s.finishUDPFlow(f)
}

// failUDPFlow 标记建连失败并进入冷却。
func (s *Stack) failUDPFlow(f *udpFlow) {
	f.state.Store(int32(udpFlowFailed))
	f.expireAt.Store(time.Now().Add(udpFailCooldown).UnixNano())
	// 必须关掉端点：demux 里的登记不消失的话，
	// 后续包会一直投给这个死端点，永远不会再触发重连。
	s.closeUDPFlow(f)
}

// finishUDPFlow 中继结束后收尾。
func (s *Stack) finishUDPFlow(f *udpFlow) {
	s.udpMu.Lock()
	if cur, ok := s.udpFlows[f.key]; ok && cur == f {
		delete(s.udpFlows, f.key)
	}
	s.udpMu.Unlock()
	s.closeUDPFlow(f)
}

// closeUDPFlow 关闭一个 UDP 四元组的端点与中继连接（幂等）。
func (s *Stack) closeUDPFlow(f *udpFlow) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	conn := f.conn
	local := f.local
	f.conn = nil
	f.local = nil
	f.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if local != nil {
		_ = local.Close()
	}
}

// pruneUDPFlowsLocked 清理没用的四元组记录（**调用者必须已持有 udpMu**）。
//
// 只清失败冷却已过的：正在中继的由 finishUDPFlow 自己摘。
func (s *Stack) pruneUDPFlowsLocked(now time.Time) {
	nowNS := now.UnixNano()
	for k, f := range s.udpFlows {
		if f.state.Load() == int32(udpFlowFailed) && nowNS > f.expireAt.Load() {
			delete(s.udpFlows, k)
		}
	}
}

// closeAllUDPFlows 关闭全部 UDP 四元组（栈退出时调用）。
//
// 不关的话：中继 goroutine 会一直挂在 FramedPipe 的读上，
// 退出就会卡住。
func (s *Stack) closeAllUDPFlows() {
	s.udpMu.Lock()
	flows := make([]*udpFlow, 0, len(s.udpFlows))
	for _, f := range s.udpFlows {
		flows = append(flows, f)
	}
	s.udpFlows = make(map[udpFlowKey]*udpFlow)
	s.udpMu.Unlock()

	for _, f := range flows {
		s.closeUDPFlow(f)
	}
}

// waitUDPFlows 等 UDP 四元组的 goroutine 退出，但**最多等 timeout**。
//
// 为什么要有上限：UDP 的建连可能卡在出口上（等 yamux ACK，最长十几秒），
// 退出不能被它无限期拖住。剩下的 goroutine 看到 s.closed 之后会自己收工。
func (s *Stack) waitUDPFlows(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.udpWg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}
