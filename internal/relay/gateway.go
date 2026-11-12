package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"slx/internal/logx"
	"slx/internal/proto"
	"slx/internal/stream"
)

// Gateway 是网关侧的 agent 连接池。
//
// 它既接受反向连接（出口节点主动拨进来），也可以主动拨 agent 的公网 IP
// （直连通道）。两种连接都记在同一个 node_id 下，构成「多通道」。
//
// # 通道与角色的关系
//
//   - ZeroTier 反向通道（zt）：agent 是 yamux 客户端，网关是服务端；
//   - 公网直连通道（tls）：网关是 yamux 客户端，agent 是服务端。
//
// 角色互换不影响上层：握手（HMAC）只认 net.Conn，yamux 两端对称，
// 业务流永远是「谁需要出网谁开流」。
//
// 网关**只出不进**：它不新增任何监听端口（直连是它主动拨出去）。
type Gateway struct {
	key  []byte
	addr string
	log  *slog.Logger

	onJoin  func(nodeID string, since time.Time)
	onLeave func(nodeID string, uptime time.Duration)

	mu     sync.RWMutex
	agents map[string]*agentSession

	ln net.Listener

	// —— 通道相关 ——

	// transport 是通道模式：zt / tls / auto。
	//
	// 用 atomic.Value 存字符串：运行期可能被切换（灰度、回退），
	// 而选路热路径每包都要读它。
	//
	// 注意：Go 1.26 的 sync/atomic 没有 atomic.String（只有 Bool/Int32/Int64…），
	// 所以这里用 atomic.Value 装 string。
	transport atomic.Value

	// nodeID 是网关自己的 node_id（直连通道里网关要向 agent 介绍自己）。
	nodeID string

	// directConns 是每个 agent 的直连通道连接数。
	directConns int

	// dialTimeout 是直连建连超时。
	dialTimeout time.Duration

	// probeInterval 是通道探测周期。
	probeInterval time.Duration

	// probeTimeout 是单次探测的超时（默认 10 秒）。
	probeTimeout time.Duration

	// pinnedTLS 是手工指定的证书指纹（可空 = 用 agent 上报的 TOFU 指纹）。
	pinnedTLS string

	// certFile 是网关侧 TLS 监听证书的持久化路径（可空 = 运行期临时证书）。
	certFile string
}

// agentSession 是一个已认证的 agent 连接。
type agentSession struct {
	nodeID  string
	conn    net.Conn
	sess    *yamux.Session
	remote  string
	since   time.Time
	closeMu sync.Once

	// chMu 保护通道列表与主通道。
	chMu sync.Mutex
	// chans 是这个 agent 的全部通道：
	// 第 0 条是 ZeroTier 反向通道（一定有），后面是公网直连通道。
	chans []*channel
	// primary 是当前选中的通道（nil = 没有可用通道）。
	primary *channel

	// dialInfo 是 agent 上报的直连信息（公网地址 + 证书指纹）。
	dialInfo *dialInfo

	// dialling 表示正在给这个 agent 补直连通道（防止并发重复建）。
	dialling bool

	// dialMu 是「建直连通道」专用的锁。
	//
	// 与 chMu 分开的理由：拨号是网络操作（可能几秒），
	// 绝不能占着 chMu（那是选路热路径的锁）。
	dialMu sync.Mutex
}

// dialInfo 是 agent 上报的直连信息。
type dialInfo struct {
	host string
	port uint16
	pin  string
	// pinAt 是指纹的上报时刻（指纹变更宽限期用）。
	pinAt time.Time
	// prevPin 是上一个指纹（轮换时留 24 小时宽限）。
	prevPin string
	// prevPinAt 是上一个指纹的上报时刻。
	prevPinAt time.Time
}

// GatewayOptions 是网关构造参数。
type GatewayOptions struct {
	Key  []byte
	Addr string
	Log  *slog.Logger

	// OnAgentJoin 在 agent 接入后调用（nodeID、接入时刻）。
	//
	// 用途：让上层把这个出口当成**全新**的 —— 清掉它上一段生命周期的
	// 熔断状态、测量指标与路由缓存。
	//
	// 为什么必须有：熔断状态按 node_id 记，而 agent 重连时 node_id 不变。
	// 它会带着上一段的冷却期（最长 10 分钟）回来，继续被排除在候选集之外，
	// 表现为「刚回来又熔断」，而且退避越滚越长，出口永远回不来。
	OnAgentJoin func(nodeID string, since time.Time)

	// OnAgentLeave 在 agent 断开后调用（nodeID、在线时长）。
	//
	// 用途同上：把它的熔断状态与指标清掉，别让下线出口的状态
	// 一直挂在内存里，也别让它带着冷却期回来。
	OnAgentLeave func(nodeID string, uptime time.Duration)

	// Transport 是通道模式：zt / tls / auto。
	//
	// 空或 "zt" = 只走 ZeroTier 反向通道（= 改造前的行为，P0 阶段的默认值）。
	Transport string

	// NodeID 是网关自己的 node_id。
	//
	// 直连通道里网关是拨号方，要向 agent 介绍自己（ClientHandshake），
	// 所以必须有这个身份。
	NodeID string

	// DirectConns 是每个 agent 的直连通道连接数（默认 4）。
	//
	// 多条独立 TCP 是缓解 TCP-in-TCP 头阻塞的关键：
	// 一条连接丢包只会拖慢它自己承载的那些流。
	DirectConns int

	// DialTimeout 是直连建连超时（默认 8 秒）。
	DialTimeout time.Duration

	// ProbeInterval 是通道探测周期（默认 15 秒）。
	ProbeInterval time.Duration

	// ProbeTimeout 是单次通道探测的超时（默认 10 秒）。
	//
	// 实测直连 RTT 75~267ms，5 秒偏紧（还要含 yamux 建流开销），
	// 10 秒更稳；宁可晚点发现坏通道，也别把好通道误杀。
	ProbeTimeout time.Duration

	// PinnedTLS 是手工指定的 agent 证书指纹（可空 = 用 agent 上报的 TOFU 指纹）。
	//
	// 钉扎是强制的：没有指纹就不开直连通道。
	PinnedTLS string

	// CertFile 是网关侧 TLS 监听证书的持久化路径（可空 = 运行期临时证书）。
	//
	// 证书持久化是 P0 的一部分：现状每次启动重新生成且只有 24 小时有效期
	//（实测路由器上那张早已过期），指纹每次都变。
	CertFile string
}

// NewGateway 创建网关。addr 形如 "0.0.0.0:11901"。
func NewGateway(o GatewayOptions) *Gateway {
	mode := o.Transport
	if mode != TransportTLS && mode != TransportAuto {
		// 默认 zt：行为与改造前完全一致，任何时候能一键回退。
		mode = TransportZT
	}
	conns := o.DirectConns
	if conns <= 0 {
		conns = DefaultChannelConns
	}
	dialTO := o.DialTimeout
	if dialTO <= 0 {
		dialTO = 8 * time.Second
	}
	probeIV := o.ProbeInterval
	if probeIV <= 0 {
		probeIV = ChannelProbeInterval
	}
	probeTO := o.ProbeTimeout
	if probeTO <= 0 {
		probeTO = DefaultProbeTimeout
	}
	g := &Gateway{
		key:           o.Key,
		addr:          o.Addr,
		log:           o.Log,
		onJoin:        o.OnAgentJoin,
		onLeave:       o.OnAgentLeave,
		agents:        make(map[string]*agentSession),
		nodeID:        o.NodeID,
		directConns:   conns,
		dialTimeout:   dialTO,
		probeInterval: probeIV,
		probeTimeout:  probeTO,
		pinnedTLS:     o.PinnedTLS,
		certFile:      o.CertFile,
	}
	g.transport.Store(mode)
	return g
}

// transportMode 返回当前通道模式（zt / tls / auto）。
func (g *Gateway) transportMode() string {
	if v, ok := g.transport.Load().(string); ok {
		return v
	}
	return TransportZT
}

// Addr 返回实际监听地址（addr 为 :0 时能拿到真实端口）。
//
// g.ln 由 Run() 写、由其它 goroutine（状态页、测试）读 ⇒ **必须加锁**。
// 之前这里是无锁读，-race 直接报 DATA RACE
// （读写都发生在 crypto/tls.Listen() 返回后的那一瞬间）。
func (g *Gateway) Addr() string {
	g.mu.RLock()
	ln := g.ln
	g.mu.RUnlock()
	if ln == nil {
		return g.addr
	}
	return ln.Addr().String()
}

// Listening 报告监听是否已经就绪。
//
// 给「等网关起来」的调用方用：Addr() 返回 ":0" 就说明还没监听上，
// 但直接读 g.ln 会与 Run() 产生数据竞争。
func (g *Gateway) Listening() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ln != nil
}

// Run 监听并接受 agent 连接，直到 ctx 取消。
func (g *Gateway) Run(ctx context.Context) error {
	// 证书持久化（P0）：现状每次启动重新生成、只有 24 小时有效期，
	// 指纹每次都变 —— 既影响排查，也让「指纹钉扎」无从谈起。
	//
	// 落盘失败（例如目录不可写）时**退回运行期临时证书**，
	// 不能因为「想持久化」反而起不来 —— P0 的底线是行为与今天完全一致。
	tlsCfg, err := TLSConfigForServerWithCert(g.certFile)
	if err != nil {
		g.log.Warn("持久化证书不可用，改用临时证书",
			logx.Event("relay.cert_fallback"),
			logx.FieldErr, err.Error(),
			logx.FieldHint, "检查证书目录是否可写（默认 /etc/slx/relay-cert.pem）")
		tlsCfg, err = TLSConfigForServer()
		if err != nil {
			return err
		}
	}
	ln, err := tls.Listen("tcp", g.addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("relay: 监听 %s 失败: %w", g.addr, err)
	}
	g.mu.Lock()
	g.ln = ln
	g.mu.Unlock()

	// ctx 取消时关掉 listener，让 Accept 返回
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	g.log.Info("agent 反向连接监听已启动",
		logx.Event("relay.listen"),
		"addr", ln.Addr().String(),
		"transport", g.transportMode(),
		logx.Hint("出口节点用 `slx agent --gateway <本机地址:端口>` 接入"))

	// 通道探测：维护每条通道的健康度与通道级熔断状态。
	//
	// 周期 15 秒、连续 3 次失败即熔断 ⇒ 45 秒内回退到兜底通道。
	go g.probeChannels(ctx)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// 单次 Accept 失败不该终止整个监听（例如瞬时 fd 耗尽）
			g.log.Warn("接受 agent 连接失败", logx.Event("relay.accept_failed"),
				logx.FieldErr, err.Error())
			continue
		}
		go g.serveAgent(ctx, conn)
	}
}

// serveAgent 处理一条新进来的 agent 连接：握手 → yamux 服务端 → 入池。
func (g *Gateway) serveAgent(ctx context.Context, conn net.Conn) {
	nodeID, err := ServerHandshake(conn, g.key)
	if err != nil {
		g.log.Warn("agent 握手失败",
			logx.Event("relay.auth_failed"),
			"remote", conn.RemoteAddr().String(),
			logx.FieldErr, err.Error(),
			logx.Hint("确认两端 network_key 一致、系统时钟正常"))
		_ = conn.Close()
		return
	}

	// yamux 服务端配置：
	// EnableKeepAlive 让断网能被及时发现（否则池子里留着死连接，
	// 选路会把流量导向一个连不上的出口，表现为「时通时不通」）。
	// 注意：yamux 要求显式传 LogOutput 或 Logger，两者全空会直接报错
	// "one of Logger or LogOutput must be set"（v0.1.x 的行为）。
	// 这里把内部日志丢弃，重要事件我们自己在上面用 logx 记录。
	sess, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		g.log.Warn("建立 yamux 会话失败", logx.Event("relay.yamux_failed"),
			logx.FieldErr, err.Error())
		_ = conn.Close()
		return
	}

	s := &agentSession{
		nodeID: nodeID,
		conn:   conn,
		sess:   sess,
		remote: conn.RemoteAddr().String(),
		since:  time.Now(),
	}
	// 反向连接 = ZeroTier 兜底通道（第 0 条）。
	ztCh := newChannel(ChannelZT, conn, sess, s.remote)
	s.addChannel(ztCh)

	g.mu.Lock()
	if old, ok := g.agents[nodeID]; ok && old != s {
		// 同一节点重连：关掉旧会话，否则会出现两条流都活着、
		// 一半流量走旧连接（旧连接可能已经半死）。
		go old.close()
	}
	g.agents[nodeID] = s
	g.mu.Unlock()

	// agent → 网关方向的流：目前只有一种 —— 地址上报（KindAddrReport）。
	//
	// 老 agent 不发这个帧 ⇒ 这里永远收不到东西 ⇒ 该 agent 只有兜底通道，
	// 天然向后兼容。
	go g.acceptAgentStreams(ctx, s, sess)

	g.log.Info("agent 已接入",
		logx.Event("relay.agent_joined"),
		logx.FieldNodeID, shortID(nodeID),
		"remote", s.remote)
	if g.onJoin != nil {
		g.onJoin(nodeID, s.since)
	}

	// 会话结束（对端断开 / 网络断）时从池子里摘掉
	<-sess.CloseChan()
	g.mu.RLock()
	cur, ok := g.agents[nodeID]
	g.mu.RUnlock()
	// isCurrent 表示这个会话仍是池子里当前那一条。
	//
	// 同一节点重连时，新会话会顶掉旧会话；旧会话随后结束。
	// 那时**不能**报「agent 掉线」，也不能触发掉线回调 ——
	// 否则刚接入的出口会被立刻当成下线，状态被清空一次。
	isCurrent := ok && cur == s
	keepAlive := false
	if isCurrent {
		// 兜底连接断了，但**直连通道可能还活着** ——
		// 直连通道存在的意义就是「ZT 路径出问题时它还能扛」。
		// 所以先摘掉兜底通道：还有可用直连通道就保留这个 agent，
		// 一条都不剩才算真的下线。
		//
		// 注意：这里**不放进 g.mu 临界区**再拿通道锁 ——
		// 锁顺序固定为 g.mu → chMu，别在同一个临界区里再套一把锁。
		cur.removeChannel(ztCh)
		keepAlive = cur.hasUsableChannel(time.Now())
		if !keepAlive {
			g.mu.Lock()
			if c, ok2 := g.agents[nodeID]; ok2 && c == s {
				delete(g.agents, nodeID)
			}
			g.mu.Unlock()
		}
	}
	if isCurrent && keepAlive {
		g.log.Info("兜底连接已断开，直连通道仍在",
			logx.Event("relay.agent_backlink_lost"),
			logx.FieldNodeID, shortID(nodeID),
			"uptime", time.Since(s.since).Round(time.Second).String())
		return
	}
	g.log.Info("agent 已断开",
		logx.Event("relay.agent_left"),
		logx.FieldNodeID, shortID(nodeID),
		"uptime", time.Since(s.since).Round(time.Second).String())
	if isCurrent && g.onLeave != nil {
		g.onLeave(nodeID, time.Since(s.since))
	}
}

// close 关闭会话（只生效一次）。
//
// 反向连接与全部直连通道一起关 —— 直连通道挂在同一个 agentSession 下，
// 只关反向连接会留下「池子里以为它下线了、实际连接还在」的僵尸通道。
func (s *agentSession) close() {
	s.closeMu.Do(func() {
		_ = s.sess.Close()
		_ = s.conn.Close()
		for _, c := range s.channels() {
			if c.kind == ChannelTLS {
				c.close()
			}
		}
	})
}

// AgentIDs 返回当前在线的 agent node_id 列表。
func (g *Gateway) AgentIDs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.agents))
	for id := range g.agents {
		out = append(out, id)
	}
	return out
}

// HasAgent 判断某 node_id 的 agent 是否在线。
// AgentRemotes 返回 nodeID → 对端地址（通常是对端的 ZeroTier 地址），
// 用于在状态页上区分「这台出口是哪一台」。
//
// 不改握手协议：加字段要动二进制帧格式并升协议版本，
// 代价远大于收益；对端地址已经足够定位。
func (g *Gateway) AgentRemotes() map[string]string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]string, len(g.agents))
	for id, s := range g.agents {
		out[id] = s.remote
	}
	return out
}

func (g *Gateway) HasAgent(nodeID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.agents[nodeID]
	return ok
}

// HasUsableChannel 判断某 node_id 的 agent 当前是否**还有可用通道**。
//
// # 为什么需要它 （2026-11-12 审计新增 ）
//
// 「agent 在线」≠「agent 能用」
//
// 一台 agent 的所有通道都可能被熔断（通道级熔断，冷却 60 秒起 ）
// 或正在重建（ZT 断线后要重新握手 ）。
//
// **这时候它仍在候选集里 ** ——
//   - EgressCandidates 只按「有 egress 能力 + 状态 alive」过滤
//   - L1 探测对「无可用通道」的 agent 整条跳过（不记丢包、不推进熔断）
//   - ⇒ 它的丢包率永远不更新、评分永远保持高位
//   - ⇒ 选路器会一直选中它 ⇒ 每次连接都失败
//   - ⇒ 它就是个**黑洞代理**
//
// **.26 这台只有一条 ZeroTier 通道（没有直连）**
// ⇒ ZT 一断它立刻变成黑洞代理
//
// ⇒ 所以候选集要**额外**按「有可用通道」过滤
func (g *Gateway) HasUsableChannel(nodeID string) bool {
	g.mu.RLock()
	s, ok := g.agents[nodeID]
	g.mu.RUnlock()
	if !ok {
		return false
	}
	return s.hasUsableChannel(time.Now())
}

// UsableAgentIDs 返回**有可用通道**的 agent node_id 列表。
//
// # AgentIDs 的加强版：把「在线但一条通道都不剩」的 agent 排除掉
//
// 为什么需要它：见 HasUsableChannel 的说明
//
//	（黑洞代理的问题：它们在线、评分高、但每条连接都失败 ）
func (g *Gateway) UsableAgentIDs() []string {
	ids := g.AgentIDs()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if g.HasUsableChannel(id) {
			out = append(out, id)
		}
	}
	return out
}

// Close 关闭监听与全部会话。
func (g *Gateway) Close() {
	g.mu.RLock()
	ln := g.ln
	g.mu.RUnlock()
	if ln != nil {
		_ = ln.Close()
	}
	g.mu.Lock()
	sessions := make([]*agentSession, 0, len(g.agents))
	for _, s := range g.agents {
		sessions = append(sessions, s)
	}
	g.agents = make(map[string]*agentSession)
	g.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

// Session 返回某个 agent 的会话（加锁读）。
//
// 给测试与内部调用用：直接读 g.agents 会与写入产生数据竞争。
//
// ★ agent 不存在时返回 nil —— 调用方必须判空
//
//	（2026-11-11 CI 上真实崩过一次：测试里直接 .tlsChannels() ⇒ nil 解引用 ）
//	想要"没有就当没有通道"的语义，请用测试辅助里的 agentSessionOf 。
func (g *Gateway) Session(nodeID string) *agentSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.agents[nodeID]
}

// ConnectError 描述一次建连失败，并携带「网关该怎么回客户端」的语义。
type ConnectError struct {
	// Reset 为 true 表示应该给客户端回 RST（快速失败），
	// false 表示不回包（让客户端自己超时重试）。
	Reset  bool
	Reason string
	// TargetUnreachable 为 true 表示「agent 本身是好的，只是它连不上这个目标」。
	//
	// 这两类失败必须分开：
	//   - relay 层失败（连不上 agent / agent 会话断）→ 出口自己坏了；
	//   - 目标不可达（被墙 / 网站挂了 / DNS 解析失败）→ 出口好得很，
	//     只是这个站点打不开。
	//
	// 混在一起的后果很致命：用户访问几个打不开的站点就能把
	// （往往也是唯一那台）出口判成坏了并熔断掉，
	// 于是「访问失败 → 出口被排除 → 更多访问失败」形成死亡螺旋。
	TargetUnreachable bool
}

func (e *ConnectError) Error() string { return e.Reason }

// ErrNoAgent 表示目标出口当前没有在线 agent。
var ErrNoAgent = errors.New("relay: 目标出口没有在线 agent")

// ErrNoUsableChannel 表示这台 agent **在线**，但它的一条可用通道都不剩
// （都被通道级熔断打开了）。
//
// # 为什么要与 ErrNoAgent 严格分开
//
// 上层的熔断计数必须知道这是哪一种：
//
//   - ErrNoAgent：agent 根本没连上来 ⇒ 这才是「出口链路失败」；
//   - ErrNoUsableChannel：agent 好好地在线着，只是通道级熔断把它掐死了。
//
// 混为一谈的后果是**级联**：一条坏通道先熔断 → L1 探测「找不到可用通道」
// → 被当成链路丢包记成 98~100% → 触发 agent 级熔断 → 整台出口被踢出候选集
// → DNS 之类的旁路一起断 → 用户重试 → 更堵 → 循环。
// 现场事故：6 台出口集体熔断、DNS 失败 523 次、国内域名也解析不出来。
var ErrNoUsableChannel = errors.New("relay: 该出口的所有通道都不可用")

// Open 在指定 agent 上开一条流并请求它连接目标。
//
// 返回的 net.Conn 是握手完成后的纯数据流；失败时返回 *ConnectError，
// 调用方据此决定回 SYN-ACK 之后是 RST 还是静默。
func (g *Gateway) Open(nodeID string, req proto.ConnectReq) (net.Conn, error) {
	g.mu.RLock()
	s, ok := g.agents[nodeID]
	g.mu.RUnlock()
	if !ok {
		return nil, ErrNoAgent
	}

	// 选通道：直连通道按 hash(dst)%N 打散，兜底通道单独承载。
	// zt 模式下永远选中兜底通道（= 改造前的行为）。
	dst := net.JoinHostPort(req.Addr, strconv.Itoa(int(req.Port)))
	now := time.Now()

	// ★ 拿**候选列表**而不是单条 ✅✅
	//
	// 2026-11-12 晚高峰修复：原来只 pick 一条，开流失败直接返回错误
	// ⇒ 撞上「半死通道」（TCP 在、yamux 会话坏、探测还没发现）
	//   每次建连都失败、且**不回退到其他通道** ⇒ 92% 建连失败率 ✅⚠️
	// ⇒ 现在：逐条尝试，失败换下一条 ✅✅
	cands := s.candidatesFor(dst, now, g.transportMode())
	if len(cands) == 0 {
		return nil, ErrNoUsableChannel
	}

	var lastErr error
	for i, sel := range cands {
		sel.inflight.Add(1)
		conn, err := openStreamWithTimeout(sel.sess, streamOpenTimeout)
		sel.inflight.Add(-1)
		if err != nil {
			lastErr = err
			// ★ 只换下一条，**不记探测失败** ✅✅
			//
			// 2026-11-12 stage7 踩坑 ✅⚠️：开流失败时调 recordProbe(false)
			//   ⇒ 数据路径的失败直接喂给通道熔断器 ✅⚠️
			//   ⇒ 网络拥塞时开流超时，5 次就熔断一条通道 ✅⚠️
			//   ⇒ 所有通道接连熔断 ⇒ 彻底无路可走 ⇒ 100% 建连失败 ✅⚠️
			//   （死亡螺旋：拥塞→超时→熔断→更少通道→更大压力→全熔断）
			//
			// ⇒ 通道死活由**专用探测**（每 15 秒）判定，数据路径不参与 ✅✅
			// ⇒ 数据路径只负责「这条不行换下一条」✅✅
			g.log.Debug("开流失败，换下一条通道",
				logx.FieldEvent, "relay.open_retry",
				logx.FieldNodeID, shortID(nodeID),
				"channel", sel.Name(), "idx", i, "err", err.Error())
			continue
		}

		// 写请求 + 读响应整体限时，避免 agent 卡住把网关协程占死。
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

		if err := stream.WriteConnectReq(conn, req); err != nil {
			_ = conn.Close()
			lastErr = err
			continue
		}
		resp, err := stream.ReadConnectResp(conn)
		if err != nil {
			_ = conn.Close()
			lastErr = err
			continue
		}
		_ = conn.SetDeadline(time.Time{})

		if !resp.Established {
			_ = conn.Close()
			// agent 明确回的是「我连不上这个目标」→ 出口本身没问题，
			// 不能算作出口故障（否则用户访问几个打不开的站点就能把出口熔断掉）。
			// ★ 注意：这条**不换通道重试** ✅✅ —— 换一条也是同一个 agent，结果一样
			return nil, &ConnectError{Reset: resp.Reset, Reason: resp.Reason, TargetUnreachable: true}
		}
		// 成功：直接返回，不碰探测计数 ✅✅
		//（通道评分由专用探测维护，数据路径的成功会把 rtt 拉成 0 ✅⚠️）
		return conn, nil
	}

	// 所有候选都失败
	if lastErr == nil {
		return nil, ErrNoUsableChannel
	}
	return nil, &ConnectError{Reset: true, Reason: "打开复用流失败: " + lastErr.Error()}
}

// shortID 截短 node_id 便于日志阅读。
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Ping 测一次到指定 agent 的往返时延（L1 链路质量）。
//
// 复用一条长探测流：每次采样只发 5 字节帧，开销可以忽略。
// 流一旦出错就丢弃，下次调用重建 —— 比在坏流上重试更简单可靠。
func (g *Gateway) Ping(nodeID string, timeout time.Duration) (time.Duration, error) {
	g.mu.RLock()
	s, ok := g.agents[nodeID]
	g.mu.RUnlock()
	if !ok {
		return 0, ErrNoAgent
	}

	// ★ 探测走**专用的探测通道**，不是业务主通道 （审计「第 3 批 #14」）
	//
	// 为什么可以这么做 ：
	//   L1 指标要衡量的是「网关 ↔ 这个 agent」的链路质量
	//   而同一个 agent 的多条 TLS 通道走的是**同一条物理路径**
	//   （只是不同的 TCP 连接）⇒ 换一条测出来的 RTT 基本一样
	//
	// 换来的好处 ：
	//   探测与业务**不再共享同一条 TCP 的拥塞窗口**
	//   ⇒ 一条几 MB 的业务传输不会把探测的小请求堵在后面
	//   ⇒ 探测超时不再被误判成「出口链路失败」⇒ **不再误杀好出口**
	//   （那正是熔断级联的源头之一 ）
	//
	// 只有一条通道时自动退化到它 （见 pickProbeChannel ）
	sel := s.pickProbeChannel(time.Now(), g.transportMode())
	if sel == nil {
		return 0, ErrNoUsableChannel
	}
	rtt, err := g.probeL1(sel, timeout)
	if err != nil {
		return 0, err
	}
	return rtt, nil
}

// PingHost 请求指定 agent 探测某主机是否可达（ICMP echo）。
//
// 每次探测开一条短流：ICMP 频率低（客户端默认 1 次/秒），
// 不值得为此维护一条常驻通道和请求 ID 复用来增加复杂度。
func (g *Gateway) PingHost(nodeID string, ip net.IP, timeout time.Duration) (bool, error) {
	g.mu.RLock()
	s, ok := g.agents[nodeID]
	g.mu.RUnlock()
	if !ok {
		return false, ErrNoAgent
	}
	if ip == nil {
		return false, errors.New("relay: 目标地址为空")
	}

	cands := s.candidatesFor(ip.String(), time.Now(), g.transportMode())
	if len(cands) == 0 {
		return false, ErrNoUsableChannel
	}
	// ★ 把探测专用通道排到最前面
	//
	// candidatesFor 是「业务主通道优先」 —— 对业务是对的
	// 但对探测是错的 ：探测要和业务错开，才不会被业务流量饿死
	// （审计「第 3 批 #14」）
	if pc := s.pickProbeChannel(time.Now(), g.transportMode()); pc != nil {
		reordered := make([]*channel, 0, len(cands))
		reordered = append(reordered, pc)
		for _, c := range cands {
			if c != pc {
				reordered = append(reordered, c)
			}
		}
		cands = reordered
	}
	var lastErr error
	for _, c := range cands {
		conn, err := openStreamWithTimeout(c.sess, streamOpenTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		alive, err := hostPingOnChannel(conn, ip, timeout)
		_ = conn.Close()
		if err == nil {
			return alive, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrNoAgent
	}
	return false, lastErr
}

// hostPingOnChannel 在一条通道上完成一次主机可达性探测。
func hostPingOnChannel(conn net.Conn, ip net.IP, timeout time.Duration) (bool, error) {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := stream.WriteHostPing(conn, proto.HostPingReq{Addr: ip.String()}); err != nil {
		return false, err
	}
	kind, payload, err := stream.ReadFrame(conn)
	if err != nil {
		return false, err
	}
	if kind != stream.KindHostPong {
		return false, fmt.Errorf("relay: 期望 host_pong，收到 %d", kind)
	}
	resp, err := stream.DecodeHostPong(payload)
	if err != nil {
		return false, err
	}
	return resp.Alive, nil
}

// dnsOpenSem 是**DNS 解析开流**的并发上限。
//
// 与业务流的开流分开计数：DNS 是短平快的请求，但并发可以很高
// （dnsfwd 允许 512 个查询同时在飞），不设上限的话它会在开流阶段
// 把 yamux 的槽位吃光，业务连接随后全部排队超时。
//
// 超限时快速失败，dnsfwd 随即回落到本地解析。
var dnsOpenSem = make(chan struct{}, 32)

// Resolve 请求指定 agent 用它那边的 DNS 解析域名。
//
// 返回的 IP 是「贴着出口位置」的结果，用于 CDN 就近与域名↔出口亲和。
//
// # 解析失败**不**记通道熔断计数（这是踩过坑的地方）
//
// 之前这里每次解析失败都会 `c.recordProbe(false)`，
// 于是**业务流量在喂通道熔断器**。后果是：
//
//	一次 DNS 解析超时 → 通道熔断计数 +1
//	→ 攒够 8 次 → 通道熔断
//	→ 这台 agent 的通道全熔断 → L1 探测找不到可用通道
//	→ 被记成 98~100% 丢包 → agent 级熔断 → 出口被踢出候选集
//	→ DNS 经出口解析全失败 → 更多 recordProbe(false) → 循环
//
// 现场症状：熔断 → 冷却 → 半开 → 探测成功 → 但下一次 DNS 解析失败又立刻熔断，
// 永远回不来。**通道的健康只能由通道探测来判定**（probeChannels），
// 业务路径不许碰熔断器。
//
// 另外，解析成功时也不该记 rtt=0 —— 那会把通道的延迟统计污染成 0，
// 让评分看起来虚假地好。
func (g *Gateway) Resolve(nodeID, domain string, timeout time.Duration) ([]net.IP, error) {
	g.mu.RLock()
	s, ok := g.agents[nodeID]
	g.mu.RUnlock()
	if !ok {
		return nil, ErrNoAgent
	}

	// 候选通道：首选主通道，失败就换下一条。
	//
	// 「一条通道坏了就换一条」是通道级隔离的核心：
	// 直连通道出问题，绝不能连带把兜底通道也拖下水。
	cands := s.candidatesFor(domain, time.Now(), g.transportMode())
	if len(cands) == 0 {
		// agent 在线，但没有一条可用通道（都被通道级熔断打开了）。
		return nil, ErrNoUsableChannel
	}

	// **DNS 解析的开流有独立上限**。
	//
	// dnsfwd 允许 512 个查询并发，而 yamux 每个会话的 synCh 只有 256 个槽位
	// ⇒ DNS 完全可以在「开流」这一阶段把业务流的位置吃光，
	// 业务连接随后全部排队超时（审计报告「根因 B」的连带问题）。
	//
	// 超限时**快速失败**：dnsfwd 会回落到本地解析
	//（internal/dnsfwd/dnsfwd.go:346），宁可给一个本地解析结果，
	// 也不能让 DNS 把整条数据面堵住。
	select {
	case dnsOpenSem <- struct{}{}:
		defer func() { <-dnsOpenSem }()
	default:
		return nil, fmt.Errorf("relay: DNS 开流并发已达上限（%d），回落到本地解析", cap(dnsOpenSem))
	}

	// 总预算：调用方给多少，整个解析（含开流）就用多少。
	//
	// 之前开流用的是固定的 streamOpenTimeout（5 秒），
	// 而 DNS 路径的客户端预算只有 2 秒 ⇒ 实际最坏 7 秒，
	// 客户端 DNS 等 7 秒 ⇒ 后续请求全排队 ⇒「一个请求慢，全家都慢」。
	deadline := time.Now().Add(timeout)
	var lastErr error
	for _, c := range cands {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			lastErr = fmt.Errorf("relay: 解析预算已用尽（%s）", timeout)
			break
		}
		openBudget := remaining
		if openBudget > streamOpenTimeout {
			openBudget = streamOpenTimeout
		}
		conn, err := openStreamWithTimeout(c.sess, openBudget)
		if err != nil {
			lastErr = err
			// **这里不记通道熔断计数**（见下面的说明）。
			continue
		}
		// 开流花掉的时间要从总预算里扣：解析本身只能用剩下的。
		rest := time.Until(deadline)
		if rest <= 0 {
			_ = conn.Close()
			lastErr = fmt.Errorf("relay: 解析预算已用尽（%s）", timeout)
			break
		}
		ips, err := resolveOnChannel(conn, domain, rest)
		_ = conn.Close()
		if err == nil {
			return ips, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrNoUsableChannel
	}
	return nil, lastErr
}

// resolveOnChannel 在一条通道上完成一次解析请求。
func resolveOnChannel(conn net.Conn, domain string, timeout time.Duration) ([]net.IP, error) {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := stream.WriteResolveReq(conn, proto.ResolveReq{Domain: domain}); err != nil {
		return nil, err
	}
	kind, payload, err := stream.ReadFrame(conn)
	if err != nil {
		return nil, err
	}
	if kind != stream.KindResolveResp {
		return nil, fmt.Errorf("relay: 期望 resolve_resp，收到 %d", kind)
	}
	resp, err := stream.DecodeResolveResp(payload)
	if err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return nil, errors.New(resp.Err)
	}
	out := make([]net.IP, 0, len(resp.IPs))
	for _, s := range resp.IPs {
		if ip := net.ParseIP(s); ip != nil {
			out = append(out, ip)
		}
	}
	return out, nil
}

// yamuxConfig 返回 relay 用的 yamux 会话配置。
//
// 启用 KeepAlive：断网能被及时发现（否则池子里留着死连接，
// 选路会把流量导向一个连不上的出口，表现为「时通时不通」）。
//
// 注意：yamux 要求显式传 LogOutput 或 Logger，两者全空会直接报错
// "one of Logger or LogOutput must be set"（v0.1.x 的行为）。
// 这里把内部日志丢弃，重要事件我们自己在上面用 logx 记录。
func yamuxConfig() *yamux.Config {
	return &yamux.Config{
		AcceptBacklog:          256,
		EnableKeepAlive:        true,
		KeepAliveInterval:      10 * time.Second,
		ConnectionWriteTimeout: 10 * time.Second,
		MaxStreamWindowSize:    512 * 1024,
		LogOutput:              io.Discard,
	}
}

// SetTransport 运行期切换通道模式（灰度与一键回退用）。
//
// 立即生效：下一次选路就用新模式。
func (g *Gateway) SetTransport(mode string) {
	switch mode {
	case TransportTLS, TransportAuto, TransportZT:
		g.transport.Store(mode)
	default:
		g.transport.Store(TransportZT)
	}
}

// Transport 返回当前通道模式。
func (g *Gateway) Transport() string {
	return g.transportMode()
}
