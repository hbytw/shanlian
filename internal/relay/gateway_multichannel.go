package relay

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"slx/internal/logx"
	"slx/internal/proto"
	"slx/internal/stream"
) // 通道模式（配置项 relay_transport）。
// P0 阶段默认 zt —— 行为与改造前完全一致，任何时候能一键回退。
const (
	// TransportZT 只走 ZeroTier 反向通道（= 今天的现状）。
	TransportZT = "zt"
	// TransportTLS 只走公网直连通道（网关主动拨 agent 的公网 IP）。
	TransportTLS = "tls"
	// TransportAuto 按通道评分自动选，带 8 分滞回。
	TransportAuto = "auto"
)

// 通道相关常量。
const (
	// ChannelProbeInterval 是通道探测周期。
	//
	// 15 秒：比 agent 级的 L1 探测（2 秒）慢得多 ——
	// 通道质量的变化以「分钟」为单位（TCP 重传、路由变化），
	// 每 2 秒探一次只是白烧 CPU。
	ChannelProbeInterval = 15 * time.Second

	// ChannelProbeFailures 是连续多少次探测失败才熔断通道。
	//
	// 3 次 × 15 秒 = 45 秒内回退到兜底通道。
	// 兜底通道是现成的（agent 反向连接一直在），所以回退要快。
	ChannelProbeFailures = 3

	// ChannelSwitchMargin 是切换通道所需的分数优势。
	//
	// 没有滞回，通道分数在噪声里来回跳会导致反复切换，
	// 而每次切换都要重建 TCP 连接（慢启动），比一直用差通道更糟。
	ChannelSwitchMargin = 8.0

	// DefaultChannelConns 是每 agent 的直连通道连接数。
	//
	// 4 条独立 TCP：一条丢包只会拖慢它自己承载的那些流，
	// 其它连接上的目标不受影响（TCP-in-TCP 头阻塞的缓解手段）。
	DefaultChannelConns = 4

	// maxChannelCooldown 是通道级熔断的冷却上限。
	maxChannelCooldown = 10 * time.Minute
)

// ChannelKind 区分通道类型。
type ChannelKind string

const (
	// ChannelZT 是 ZeroTier 反向通道（agent → 网关）。
	ChannelZT ChannelKind = "zt"
	// ChannelTLS 是公网直连通道（网关 → agent 公网 IP）。
	ChannelTLS ChannelKind = "tls"
)

// ewma 是指数加权移动平均。
//
// 这里自己实现一份而不复用 internal/picker 的 EWMA：
// relay 不该依赖 picker（那是选路层的包），
// 而且通道级指标与 agent 级指标的语义不同，混在一起容易搞混。
type ewma struct {
	mu    sync.Mutex
	alpha float64
	value float64
	valid bool
}

func newEWMA(alpha float64) *ewma {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	return &ewma{alpha: alpha}
}

// Update 加入一个新样本。
func (e *ewma) Update(v float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.valid {
		e.value = v
		e.valid = true
		return
	}
	e.value = e.alpha*v + (1-e.alpha)*e.value
}

// set 直接设置当前值（不经过平滑），用于测试与初始化。
func (e *ewma) set(v float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.value = v
	e.valid = true
}

// get 返回当前值；没有样本时返回 fallback。
func (e *ewma) get(fallback float64) float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.valid {
		return fallback
	}
	return e.value
}

// reset 清空样本。
func (e *ewma) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.value = 0
	e.valid = false
}

// channelProbeFailures 是通道级熔断的阈值（连续失败多少次才熔断）。
//
// 用包级变量而不是层层传参：熔断器是在建通道时创建的，
// 而阈值来自启动配置（配置在 CLI 层，通道在 relay 层）。
var channelProbeFailures = ChannelProbeFailuresDefault

// SetChannelProbeFailures 设置通道级熔断阈值（非法值忽略）。
func SetChannelProbeFailures(n int) {
	if n > 0 {
		channelProbeFailures = n
	}
}

// ChannelProbeFailuresDefault 是通道级熔断的默认阈值（连续失败次数）。
//
// 3 次太激进：一次网络抖动 + 一次慢响应就够把好通道打死，
// 而兜底通道虽然还在，但直连的收益全丢。5 次（15 秒一次 ⇒ 75 秒）更稳。
const ChannelProbeFailuresDefault = 5

// channelBreaker 是**通道级**熔断器。
//
// 与 agent 级熔断（internal/picker 里的 breakers / l2Breakers）完全独立：
// 计数独立、冷却独立、复位独立。
//
// 铁律：通道熔断**只影响「选哪条通道」**，
// 绝不能把 agent 排除出候选集 —— 那是 agent 级熔断的职责。
// 两者混在一起会重演 v2 线上那次死亡螺旋
// （用户建连失败误伤 agent 级熔断 → 出口全被排除 → 访问更失败）。
type channelBreaker struct {
	mu       sync.Mutex
	failures int
	until    time.Time
	cooldown time.Duration
	open     bool
	// threshold 是连续失败多少次才熔断（默认 5，见 ChannelProbeFailuresDefault）。
	threshold int
}

// channelBreakerCooldownBase 是通道级熔断的首次冷却时间。
//
// 60 秒起步，每次熔断翻倍，maxChannelCooldown 封顶
const channelBreakerCooldownBase = 60 * time.Second

func newChannelBreaker() *channelBreaker {
	return &channelBreaker{cooldown: channelBreakerCooldownBase, threshold: channelProbeFailures}
}

// Allow 报告这条通道现在能不能用。
//
// 冷却结束后进入半开：放一次探测过去。
// 半开期间再失败一次就重新熔断（冷却翻倍）。
//
// 返回 true 且状态刚从「熔断」变成「半开」时，调用方**必须**用一条
// 新的探测流去试（见 channel.resetProbe）——
// 拿一条已经坏掉的流试探，注定失败，会立刻重新熔断，形成死循环。
func (b *channelBreaker) Allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if now.After(b.until) {
		b.open = false
		b.failures = b.threshold - 1
		return true
	}
	return false
}

// Record 记录一次探测结果。
//
// # ★ open 状态下的一次成功，不该关闭熔断
//
// 这是 2026-11-12 审计的同源问题 （agent 级熔断器已修，这里一并修 ）
//
// 通道探测每 15 秒一次 ⇒ 只要通道有一点通，success 就会不停到来
// ⇒ 原来的写法每次 success 都清零 failures、关掉 open
// ⇒ ⇒ **指数退避（60→120→240…）永远升不上去**
// ⇒ ⇒ 一个时好时坏的通道，每次都只享受 60 秒冷却
//
// ⇒ 现在：★★
//
//	open 状态下的一次成功：只清失败计数，**不关闭、不重置退避**
//	                  ⇒ 冷却期真正生效
//	半开状态下的一次成功：试探通过 ⇒ 真恢复 ⇒ 关闭并重置退避
func (b *channelBreaker) Record(ok bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		if b.open {
			// ★ 还在冷却期 ⇒ 一次成功不算恢复
			//   保持 open、保持退避计数与冷却时长
			return
		}
		if b.failures > 0 && b.failures >= b.threshold-1 {
			// 半开试探通过 ⇒ 真的恢复了 ⇒ 清空并重置退避
			b.failures = 0
			b.cooldown = channelBreakerCooldownBase
			b.until = time.Time{}
			return
		}
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.open = true
		b.until = now.Add(b.cooldown)
		b.cooldown *= 2
		if b.cooldown > maxChannelCooldown {
			b.cooldown = maxChannelCooldown
		}
	}
}

// state 返回熔断状态（供日志与状态展示用）。
func (b *channelBreaker) state() (open bool, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open, b.until
}

// channel 是一条到某个 agent 的具体连接。
//
// 一条 channel = 一条 TCP 连接 + 一个 yamux 会话。
type channel struct {
	kind   ChannelKind
	conn   net.Conn
	sess   *yamux.Session
	remote string
	since  time.Time

	// probeMu 保护**通道健康探测**流（沿用 agentSession 的既有做法：
	// 探测流按需建立、失败即丢弃重来）。
	probeMu sync.Mutex
	probe   *yamux.Stream

	// l1Mu / l1probe 是 **agent 级 L1 探测**用的独立流。
	//
	// 与通道健康探测分开：L1 每 2 秒一次（喂选路器），
	// 通道探测每 15 秒一次（判健康）。共用一条流会互相饿死。
	l1Mu    sync.Mutex
	l1probe *yamux.Stream

	// 通道评分用的指标。
	mu      sync.Mutex
	rtt     *ewma
	succ    *ewma
	bw      *ewma
	bytes   int64
	bytesAt time.Time

	// inflight 是这条连接上在途的业务流数（负载均衡用）。
	inflight atomic.Int64

	// breaker 是通道级熔断器（与 agent 级完全独立）。
	breaker *channelBreaker

	// closed 标记这条连接已经废弃。
	closed atomic.Bool
}

// newChannel 创建一条通道。
func newChannel(kind ChannelKind, conn net.Conn, sess *yamux.Session, remote string) *channel {
	return &channel{
		kind:    kind,
		conn:    conn,
		sess:    sess,
		remote:  remote,
		since:   time.Now(),
		rtt:     newEWMA(0.3),
		succ:    newEWMA(0.3),
		bw:      newEWMA(0.3),
		bytesAt: time.Now(),
		breaker: newChannelBreaker(),
	}
}

// Name 返回通道名。
func (c *channel) Name() string { return string(c.kind) }

// Inflight 返回在途流数。
func (c *channel) Inflight() int64 { return c.inflight.Load() }

// usable 报告这条通道当前能不能用（没熔断、没关闭）。
//
// 注意：这里**只**看通道自己的熔断器，不看 agent 级状态。
func (c *channel) usable(now time.Time) bool {
	if c == nil || c.closed.Load() {
		return false
	}
	return c.breaker.Allow(now)
}

// recordProbe 记录一次探测结果（成功与否、往返时延）。
func (c *channel) recordProbe(ok bool, rtt time.Duration, now time.Time) {
	c.mu.Lock()
	if ok {
		c.succ.Update(1)
		c.rtt.Update(float64(rtt.Microseconds()) / 1000)
	} else {
		c.succ.Update(0)
	}
	c.mu.Unlock()

	c.breaker.Record(ok, now)
}

// addBytes 累计吞吐字节数。
func (c *channel) addBytes(n int64) {
	c.mu.Lock()
	c.bytes += n
	c.mu.Unlock()
}

// score 计算这条通道的综合分（0~100，越高越好）。
//
// 权重与 picker.DefaultWeights() 同构（成功/延迟/吞吐 ↔ 带宽/延迟/丢包），
// 让「通道好」与「出口好」在观感上一致。
//
// 没有样本时用保守兜底值（成功率 0.5、RTT 200ms、吞吐 5Mbps），
// 避免「刚建好的直连通道」因为零数据就被判成满分。
func (c *channel) score(now time.Time) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	succ := c.succ.get(0.5)
	rtt := c.rtt.get(200)
	bw := c.bw.get(5)

	sRTT := 100.0 / (100.0 + rtt)
	sBW := bw / (bw + 50)
	return 100 * (0.45*succ + 0.30*sRTT + 0.25*sBW)
}

// settleBW 结算一次吞吐（Mbps）。
func (c *channel) settleBW(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elapsed := now.Sub(c.bytesAt).Seconds()
	if elapsed < 10 {
		return
	}
	mbps := float64(c.bytes*8) / 1000 / 1000 / elapsed
	c.bw.Update(mbps)
	c.bytes = 0
	c.bytesAt = now
}

// close 关闭这条通道。
func (c *channel) close() {
	if c == nil {
		return
	}
	c.closed.Store(true)
	if c.sess != nil {
		_ = c.sess.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// hashTarget 把目标地址散列到一条直连连接上。
//
// 同一个目标的流量稳定落在同一条连接上（连接亲和），
// 避免同一站点在多条连接之间乱跳。
func hashTarget(dst string, n int) int {
	if n <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(dst))
	return int(h.Sum32() % uint32(n))
}

// —— agentSession 的通道管理 ——

// addChannel 加入一条通道。
func (s *agentSession) addChannel(c *channel) {
	if c == nil {
		return
	}
	s.chMu.Lock()
	s.chans = append(s.chans, c)
	s.chMu.Unlock()
}

// removeChannel 移除一条通道（连接断了）。
func (s *agentSession) removeChannel(c *channel) {
	if c == nil {
		return
	}
	s.chMu.Lock()
	out := s.chans[:0]
	for _, x := range s.chans {
		if x != c {
			out = append(out, x)
		}
	}
	s.chans = out
	if s.primary == c {
		s.primary = nil
	}
	s.chMu.Unlock()
}

// channels 返回该 agent 的全部通道（副本）。
func (s *agentSession) channels() []*channel {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	out := make([]*channel, len(s.chans))
	copy(out, s.chans)
	return out
}

// tlsChannels 返回全部直连通道（副本）。
func (s *agentSession) tlsChannels() []*channel {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	return s.tlsChansLocked()
}

// tlsChansLocked 返回全部直连通道（副本）。
//
// **调用者必须已持有 chMu。**
//
// 为什么要单独一个「已持锁」版本：现场事故的根因就是
// 「持着 chMu 又去调一个自己加锁的方法」—— Go 的 mutex 不可重入，
// 直接自锁死，该 agent 的所有选路（Open/Resolve/Ping）全部永久阻塞。
func (s *agentSession) tlsChansLocked() []*channel {
	var out []*channel
	for _, c := range s.chans {
		if c.kind == ChannelTLS {
			out = append(out, c)
		}
	}
	return out
}

// hasUsableChannel 报告这个 agent 是否还有可用通道。
func (s *agentSession) hasUsableChannel(now time.Time) bool {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	for _, c := range s.chans {
		if c.usable(now) {
			return true
		}
	}
	return false
}

// selectChannel 按模式与评分选出主通道。
//
// 模式：
//   - zt：只认 ZeroTier 兜底通道（P0 的默认，行为与改造前完全一致）；
//   - tls：只认直连通道；
//   - auto：在全部可用通道里选分数最高的。
//
// 滞回：当前主通道还可用、且没有别的通道高出 ChannelSwitchMargin（8 分）时，
// 不切换 —— 通道分数会抖，每次切换都要重建 TCP 连接（慢启动），
// 反复切换比一直用次优通道更糟。
func (s *agentSession) selectChannel(now time.Time, mode string) *channel {
	return s.selectChannelLocked(now, mode, true)
}

// selectChannelNoSideEffect 与 selectChannel 相同，但**不写 s.primary**
//
// 用途：探测这类「只想知道当前主通道是谁」的只读场景
//
// ★ 为什么要单独一个 ：
//
//	selectChannel 会把选出的通道写进 s.primary
//	⇒ 探测路径调它 ⇒ 在 tls/zt 模式下会把**全局业务主通道**
//	  改写成该模式下评分最高的通道
//	⇒ 业务本该走 ZT，探测一跑就改走 TLS
//
// ★ 注意：它返回的是「按同样规则算出来、但不落盘」的那条
//
//	滞回逻辑与 selectChannel 完全一致
func (s *agentSession) selectChannelNoSideEffect(now time.Time, mode string) *channel {
	return s.selectChannelLocked(now, mode, false)
}

// selectChannelLocked 是 selectChannel 的实现。
//
// commit=false 时**不写 s.primary** —— 只读场景用
func (s *agentSession) selectChannelLocked(now time.Time, mode string, commit bool) *channel {
	s.chMu.Lock()
	defer s.chMu.Unlock()

	var cands []*channel
	for _, c := range s.chans {
		if !c.usable(now) {
			continue
		}
		switch mode {
		case TransportTLS:
			if c.kind != ChannelTLS {
				continue
			}
		case TransportZT:
			if c.kind != ChannelZT {
				continue
			}
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		if commit {
			s.primary = nil
		}
		return nil
	}

	// ★ auto 模式：TLS 直连优先，TLS 全部不可用才回退 ZT 隧道 ✅✅
	//
	// 为什么要这样（2026-11-12 晚高峰排查结论）：
	//
	//	- ZT 是 ZeroTier 的 UDP 虚拟以太网，跨境 UDP 不稳定
	//	- 实测 ZT 隧道延迟与裸路径相当（49ms vs 70ms），但**间歇性抖动更明显**
	//	- 而 TLS 直连走 TCP 443，与 v2ray 同类路径，稳定性更好
	//
	// 之前的问题：TLS 评分比 ZT 高一点（48.7 vs 48.2），
	// 但滞回门槛 ChannelSwitchMargin=8 分，差 0.5 分切不过去
	// ⇒ 流量一直挤在 ZT 上 ✅⚠️
	//
	// 现在：★ 只要有可用的 TLS 通道，就只在 TLS 里选 ✅✅
	//        TLS 全部熔断/关闭 ⇒ cands 里没有 TLS ⇒ 自动回退 ZT ✅✅
	//
	// .26 这种只有 ZT 没有 TLS 的 agent：cands 里全是 ZT ⇒ 用 ZT ✅✅
	if mode == TransportAuto {
		var tlsCands []*channel
		for _, c := range cands {
			if c.kind == ChannelTLS {
				tlsCands = append(tlsCands, c)
			}
		}
		if len(tlsCands) > 0 {
			cands = tlsCands
		}
		// 没有 TLS 通道 ⇒ cands 保持原样（全是 ZT）✅✅
	}

	best := cands[0]
	bestScore := best.score(now)
	for _, c := range cands[1:] {
		if sc := c.score(now); sc > bestScore {
			best, bestScore = c, sc
		}
	}

	// ★ TLS 优先时跳过滞回 ✅✅
	//
	// 滞回的目的是避免频繁切换，但 TLS 优先是**策略选择**不是评分比较
	// ⇒ 只要 TLS 可用就切过去，不看评分差距 ✅✅
	// ⇒ TLS 不可用时 cands 里没有 TLS，走不到这里 ✅✅
	if mode == TransportAuto && best.kind == ChannelTLS {
		if commit {
			s.primary = best
		}
		return best
	}

	if s.primary != nil && s.primary.usable(now) {
		if best == s.primary {
			return s.primary
		}
		cur := s.primary.score(now)
		if bestScore < cur+ChannelSwitchMargin {
			return s.primary // 优势不够，保持现状
		}
	}
	if commit {
		s.primary = best
	}
	return best
}

// pickChannel 选出承载某个目标的通道。
//
// 直连通道有 N 条时按 hash(dst)%N 打散：同一个目标的流量稳定落在同一条连接上，
// 避免同一站点在多条连接之间乱跳；一条连接丢包只影响它自己承载的那些流。
func (s *agentSession) pickChannel(dst string, now time.Time, mode string) *channel {
	sel := s.selectChannel(now, mode)
	if sel == nil {
		return nil
	}
	if sel.kind != ChannelTLS {
		return sel
	}
	all := s.tlsChannels()
	if len(all) == 0 {
		return sel
	}
	idx := hashTarget(dst, len(all))
	c := all[idx]
	if c.usable(now) {
		return c
	}
	// 哈希选中的那条坏了 → 退回主通道（不换连接，避免重排）。
	return sel
}

// —— 网关侧直连 ——

// fingerprintGrace 是指纹变更的宽限期。
//
// agent 的自签证书到期前会被重新签发，指纹随之改变。
// 宽限期内接受「上一个指纹」，避免轮换瞬间把直连通道全打断。
const fingerprintGrace = 24 * time.Hour

// acceptAgentStreams 接收 agent → 网关方向的流。
//
// 目前只有一种：地址上报（KindAddrReport）。
// 老 agent 不发这个帧 ⇒ 这里收不到东西 ⇒ 该 agent 只有兜底通道，
// 天然向后兼容。
func (g *Gateway) acceptAgentStreams(ctx context.Context, s *agentSession, sess *yamux.Session) {
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go g.handleAgentStream(ctx, s, st)
	}
}

// handleAgentStream 处理一条 agent 主动开来的流。
func (g *Gateway) handleAgentStream(ctx context.Context, s *agentSession, st *yamux.Stream) {
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(10 * time.Second))
	kind, payload, err := stream.ReadFrame(st)
	if err != nil {
		return
	}
	if kind != stream.KindAddrReport {
		// 不认识的帧按「容忍未知消息」处理，忽略即可。
		return
	}
	rep, err := stream.DecodeAddrReport(payload)
	if err != nil {
		return
	}
	g.handleAddrReport(ctx, s.nodeID, rep)
}

// handleAddrReport 处理 agent 上报的公网地址与证书指纹。
//
// 信任来源：TOFU —— 这个帧走在**已通过 HMAC 握手认证**的反向连接上，
// 所以地址与指纹的可信度等同于 network_key。
//
// 钉扎强制：没有指纹就不开直连通道（宁可用兜底通道）。
func (g *Gateway) handleAddrReport(ctx context.Context, nodeID string, rep proto.AddrReport) {
	pin, err := NormalizeFingerprint(rep.CertSHA256)
	if err != nil {
		g.log.Warn("地址上报的证书指纹非法",
			logx.Event("relay.addr_report_invalid"),
			logx.FieldNodeID, shortID(nodeID),
			logx.FieldErr, err.Error())
		return
	}
	if pin == "" {
		g.log.Warn("地址上报没有指纹，不建立直连通道",
			logx.Event("relay.addr_report_no_pin"),
			logx.FieldNodeID, shortID(nodeID),
			logx.FieldHint, "agent 必须启用 relay_tls_listen 并持久化证书")
		return
	}
	if rep.Host == "" || rep.Port == 0 {
		return
	}

	g.mu.RLock()
	s := g.agents[nodeID]
	g.mu.RUnlock()
	if s == nil {
		return
	}

	s.chMu.Lock()
	prev := s.dialInfo
	info := &dialInfo{host: rep.Host, port: rep.Port, pin: pin, pinAt: time.Now()}
	if prev != nil {
		if prev.pin == pin {
			// 指纹没变：保留原来的宽限信息。
			info.prevPin = prev.prevPin
			info.prevPinAt = prev.prevPinAt
		} else {
			// 指纹变了：旧的留 24 小时宽限。
			info.prevPin = prev.pin
			info.prevPinAt = prev.pinAt
		}
	}
	s.dialInfo = info
	s.chMu.Unlock()

	g.log.Info("收到 agent 的直连地址",
		logx.Event("relay.addr_reported"),
		logx.FieldNodeID, shortID(nodeID),
		"addr", net.JoinHostPort(rep.Host, strconv.Itoa(int(rep.Port))),
		"pin", shortFingerprint(pin),
		"transport", g.transport)

	g.ensureDirectChannels(ctx, nodeID)
}

// pinFor 返回建立直连通道时应当钉扎的指纹。
//
// 优先级：手工配置 > agent 上报的当前指纹 > 24 小时宽限内的旧指纹。
func (g *Gateway) pinFor(info *dialInfo) string {
	if g.pinnedTLS != "" {
		return g.pinnedTLS
	}
	if info == nil {
		return ""
	}
	if info.pin != "" {
		return info.pin
	}
	if info.prevPin != "" && time.Since(info.prevPinAt) < fingerprintGrace {
		return info.prevPin
	}
	return ""
}

// ensureDirectChannels 为某个 agent 补齐直连通道（默认 4 条）。
func (g *Gateway) ensureDirectChannels(ctx context.Context, nodeID string) {
	if g.transportMode() == TransportZT {
		return // zt 模式不建直连通道
	}
	g.mu.RLock()
	s := g.agents[nodeID]
	g.mu.RUnlock()
	if s == nil {
		return
	}

	// 拨号锁与通道列表锁分开：
	// 拨号是网络操作（可能几秒），绝不能占着 chMu ——
	// 占了就把这个 agent 的所有选路（Open/Resolve/Ping）卡死。
	s.dialMu.Lock()
	defer s.dialMu.Unlock()

	s.chMu.Lock()
	if s.dialling {
		s.chMu.Unlock()
		return
	}
	s.dialling = true
	info := s.dialInfo
	// 注意：这里必须用「已持锁」的版本。
	// 原来写成 len(s.tlsChannels()) —— 而 tlsChannels() 自己要拿 chMu，
	// Go 的 mutex 不可重入 ⇒ **自锁死** ⇒ 该 agent 的选路永久阻塞
	// ⇒ DNS 转发器（串行）被拖死 ⇒ 全屋断网（现场事故根因）。
	have := len(s.tlsChansLocked())
	s.chMu.Unlock()
	defer func() {
		s.chMu.Lock()
		s.dialling = false
		s.chMu.Unlock()
	}()

	if info == nil {
		return
	}
	if have >= g.directConns {
		return
	}

	for i := have; i < g.directConns; i++ {
		ch, err := g.dialAgent(ctx, nodeID, info)
		if err != nil {
			g.log.Warn("建立直连通道失败",
				logx.Event("relay.direct_failed"),
				logx.FieldNodeID, shortID(nodeID),
				logx.FieldErr, err.Error())
			return
		}
		s.addChannel(ch)
		g.log.Info("直连通道已建立",
			logx.Event("relay.direct_opened"),
			logx.FieldNodeID, shortID(nodeID),
			"channel", ch.Name(),
			"remote", ch.remote,
			"idx", i+1)
	}
}

// dialAgent 主动拨 agent 的公网 IP，建立一条直连通道。
//
// 与反向通道相反：这里是**网关**拨号，所以网关是 yamux 客户端，
// 由网关向 agent 介绍自己（ClientHandshake）。
func (g *Gateway) dialAgent(ctx context.Context, nodeID string, info *dialInfo) (*channel, error) {
	pin := g.pinFor(info)
	if pin == "" {
		return nil, ErrNoPin
	}
	if g.nodeID == "" {
		return nil, errors.New("relay: 网关缺少自己的 node_id，无法建立直连通道")
	}
	tr := TLSTransport{Server: false, Pin: pin}
	addr := net.JoinHostPort(info.host, strconv.Itoa(int(info.port)))

	dialCtx, cancel := context.WithTimeout(ctx, g.dialTimeout)
	defer cancel()
	conn, err := tr.Dial(dialCtx, addr)
	if err != nil {
		return nil, err
	}
	if err := ClientHandshake(conn, g.key, g.nodeID); err != nil {
		_ = conn.Close()
		return nil, err
	}
	sess, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newChannel(ChannelTLS, conn, sess, conn.RemoteAddr().String()), nil
}

// —— 通道探测 ——

// errChannelSessionClosed 表示这条通道的底层会话已经关闭。
//
// 这不是「探测失败」，而是「这条通道已经死了」——
// 应当直接把它摘掉重建，而不是走熔断流程反复冷却。
var errChannelSessionClosed = errors.New("relay: 通道会话已关闭")

// probeOnChannel 在某条通道上做一次 ping 往返。
//
// 每条通道各有一条常驻探测流：流一旦出错就丢弃，下次重建。
//
// # 三条硬要求（都是现场事故换来的）
//
//  1. 开流必须带超时：yamux 的 OpenStream 在对端不再 ACK 时会**永久阻塞**
//     （默认 StreamOpenTimeout=0 = 不超时）。
//  2. 会话已死要立刻认出来：否则每轮探测都要白等一个超时。
//  3. 半开（冷却结束）后必须**重建探测流**：
//     拿一条已经坏掉的流去试探，注定失败 → 立刻再熔断 → 永远回不来
//     （现场症状：熔断 → 冷却 2 分钟 → 半开 → 又失败 → 再熔断，循环）。
func (g *Gateway) probeOnChannel(c *channel, timeout time.Duration) (time.Duration, error) {
	// 会话已经死了 → 立刻失败，不浪费一个探测周期。
	if c.sess == nil || c.sess.IsClosed() {
		return 0, errChannelSessionClosed
	}

	c.probeMu.Lock()
	defer c.probeMu.Unlock()

	if c.probe == nil {
		st, err := openStreamWithTimeout(c.sess, streamOpenTimeout)
		if err != nil {
			return 0, err
		}
		if err := stream.WriteControlFrame(st, stream.KindProbeHello); err != nil {
			_ = st.Close()
			return 0, err
		}
		c.probe = st
	}

	start := time.Now()
	_ = c.probe.SetDeadline(start.Add(timeout))
	if err := stream.WriteControlFrame(c.probe, stream.KindPing); err != nil {
		// 写失败：这条流已经废了（对端关掉或会话死了）。
		// 丢掉它，下次重建 —— 留着只会一直失败。
		c.discardProbe()
		return 0, err
	}
	kind, _, err := stream.ReadFrame(c.probe)
	if err != nil {
		c.discardProbe()
		return 0, err
	}
	if kind != stream.KindPong {
		c.discardProbe()
		return 0, fmt.Errorf("relay: 探测通道收到意外帧类型 %d", kind)
	}
	rtt := time.Since(start)
	_ = c.probe.SetDeadline(time.Time{})
	return rtt, nil
}

// discardProbe 丢弃当前的探测流（下次探测会重建一条新的）。
//
// 调用者必须持有 c.probeMu。
func (c *channel) discardProbe() {
	if c.probe != nil {
		_ = c.probe.Close()
		c.probe = nil
	}
}

// resetProbe 强制丢弃探测流，用于「半开试探」之前：
// 半开必须用一条**全新的**流去试，拿坏流试探注定失败。
func (c *channel) resetProbe() {
	c.probeMu.Lock()
	c.discardProbe()
	c.probeMu.Unlock()
}

// probeChannels 周期性地探测所有通道，维护通道评分与通道级熔断。
//
// 探测失败连续 3 次（15 秒 × 3 = 45 秒）就熔断这条通道 → 自动回退兜底通道。
func (g *Gateway) probeChannels(ctx context.Context) {
	t := time.NewTicker(g.probeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		g.mu.RLock()
		sessions := make([]*agentSession, 0, len(g.agents))
		for _, s := range g.agents {
			sessions = append(sessions, s)
		}
		g.mu.RUnlock()

		// 各通道**并行**探测。
		//
		// 之前是逐条串行：一条通道卡住就要等满一个探测超时，
		// 20 条直连 + 5 条兜底最坏一轮要几分钟 —— 探测周期形同虚设，
		// 熔断判定也跟着滞后，状态页上的延迟全是旧账。
		var wg sync.WaitGroup
		for _, s := range sessions {
			for _, c := range s.channels() {
				// 会话已死的通道直接摘掉重建 —— 它已经没救了，
				// 再探测也只是每次都超时。
				if c.sess != nil && c.sess.IsClosed() {
					g.log.Warn("通道会话已断开，重建该通道",
						logx.Event("relay.channel_dead"),
						logx.FieldNodeID, shortID(s.nodeID),
						"channel", c.Name(),
						"remote", c.remote)
					s.removeChannel(c)
					c.close()
					go g.ensureDirectChannels(ctx, s.nodeID)
					continue
				}
				if !c.usable(now) {
					continue
				}
				wg.Add(1)
				go func(s *agentSession, c *channel) {
					defer wg.Done()
					g.probeOneChannel(s, c, now)
				}(s, c)
			}
		}
		wg.Wait()
	}
}

// probeOneChannel 探测单条通道并维护它的评分与熔断状态。
//
// 只有这里可以推进通道级熔断计数 —— 业务路径（DNS 解析、建连）
// 不许碰熔断器，否则一次业务超时就能把好通道打死。
func (g *Gateway) probeOneChannel(s *agentSession, c *channel, now time.Time) {
	rtt, err := g.probeOnChannel(c, g.probeTimeout)
	if err != nil {
		// 半开试探失败前，先把坏流丢掉 ——
		// 否则下次还是拿同一条坏流试，永远回不来。
		c.resetProbe()
	}
	c.recordProbe(err == nil, rtt, now)
	c.settleBW(now)
	if err != nil {
		open, until := c.breaker.state()
		if open {
			g.log.Warn("通道已熔断",
				logx.Event("relay.channel_open"),
				logx.FieldNodeID, shortID(s.nodeID),
				"channel", c.Name(),
				logx.FieldErr, err.Error(),
				"until", until.Format(time.RFC3339),
				logx.Hint("通道熔断只影响这条连接，兜底通道与 agent 级熔断都不受影响"))
		}
	}
}

// ChannelInfo 是通道的状态摘要（供状态页展示）。
type ChannelInfo struct {
	NodeID  string  `json:"node_id"`
	Kind    string  `json:"kind"`
	Remote  string  `json:"remote"`
	Score   float64 `json:"score"`
	Primary bool    `json:"primary"`
	Open    bool    `json:"open"`
	Since   string  `json:"since"`
	// Streams 是这条通道当前打开的 yamux 流数。
	//
	// 为什么要暴露它：yamux 的 synCh 只有 256 个槽位，而开流拿槽位时
	// 没有超时（对端不确认就永久阻塞）。流数异常膨胀 ⇒
	// 说明有开流卡住没回收 ⇒ 这正是「数据面永久卡死」的前兆。
	Streams int `json:"streams"`
	// RTT 是该通道探测往返时延（EWMA，毫秒；无样本时兜底 200）。
	RTT float64 `json:"rtt_ms"`
	// Succ 是该通道探测成功率（0~1，EWMA；无样本时兜底 0.5）。
	Succ float64 `json:"succ"`
	// BW 是该通道吞吐（Mbps，EWMA 结算；无样本时兜底 5）。
	BW float64 `json:"bw_mbps"`
}

// ChannelInfos 返回所有 agent 的通道状态。
func (g *Gateway) ChannelInfos() []ChannelInfo {
	now := time.Now()
	g.mu.RLock()
	sessions := make([]*agentSession, 0, len(g.agents))
	for _, s := range g.agents {
		sessions = append(sessions, s)
	}
	g.mu.RUnlock()

	var out []ChannelInfo
	for _, s := range sessions {
		primary := s.Primary()
		for _, c := range s.channels() {
			open, _ := c.breaker.state()
			streams := 0
			if c.sess != nil {
				streams = c.sess.NumStreams()
			}
			out = append(out, ChannelInfo{
				NodeID:  s.nodeID,
				Kind:    c.Name(),
				Remote:  c.remote,
				Score:   c.score(now),
				Primary: primary == c,
				Open:    open,
				Since:   c.since.Format(time.RFC3339),
				Streams: streams,
				RTT:     c.rtt.get(200),
				Succ:    c.succ.get(0.5),
				BW:      c.bw.get(5),
			})
		}
	}
	return out
}

// Primary 返回某个 agent 当前选中的通道名（空 = 没有可用通道）。
func (s *agentSession) Primary() *channel {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	return s.primary
}

// streamOpenTimeout 是开复用流的超时。
//
// 为什么必须加超时：yamux 的 OpenStream 在对端不再 ACK 时会**永久阻塞**
// （默认 StreamOpenTimeout = 0 = 不超时）。
// 而 DNS 转发器是串行处理的 —— 一个卡住的开流会把整个 DNS 服务拖死，
// 客户端 20 秒超时 = 全屋断网（现场事故）。
const streamOpenTimeout = 5 * time.Second

// openStreamSem 是**在途开流**的全局信号量（信号量满了就快速失败）。
//
// # 为什么必须有这个上限
//
// yamux 的 OpenStream 在拿 SYN 槽位时**没有超时**（yamux session.go:169-174：
// `s.synCh <- struct{}{}` 只在会话关闭时才解除），对端不 ACK 就永久阻塞。
// 而 synCh 一共只有 256 个槽位（每会话）。
//
// ⇒ 一次开流超时若把 goroutine 丢在那儿，槽位就被**永久**占住；
//
//	攒够 256 次，这个会话的所有新开流永久阻塞 ⇒ 该通道彻底死掉。
//	（审计报告「根因 C」：可能演变成整台网关数据面永久死亡。）
//
// ⇒ 所以在途开流数必须有硬上限：超了直接快速失败，**不再排队**
//
//	（排队等于把超时往后推，而超时的根因是会话本身有问题）。
//	上限取 128：远小于 256 ⇒ 永远吃不满 synCh ⇒ 不会锁死。
var openStreamSem = make(chan struct{}, 128)

// openStreamWithTimeout 开一条复用流，带超时。
//
// 三条保证：
//
//  1. 超时后调用方立刻返回，不被拖住；
//  2. 卡在 yamux 里的那个 goroutine **有界**：在途开流总数受
//     openStreamSem 限制，卡住的开流数量因此有界，通道重建或
//     会话关闭时自然释放；
//  3. 迟到的 stream 会被回收（下面的 janitor 关掉它），
//     不在对端留下永远不关的流条目。
func openStreamWithTimeout(sess *yamux.Session, timeout time.Duration) (*yamux.Stream, error) {
	if sess == nil {
		return nil, errors.New("relay: 会话为空")
	}
	// 在途开流数超限 ⇒ 快速失败（不排队）。
	// 在途开流数超限 ⇒ 快速失败（不排队）。
	select {
	case openStreamSem <- struct{}{}:
	default:
		return nil, fmt.Errorf("relay: 在途开流已达上限（%d），拒绝新开流", cap(openStreamSem))
	}
	type res struct {
		conn *yamux.Stream
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		defer func() { <-openStreamSem }()
		c, err := sess.OpenStream()
		ch <- res{c, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-timer.C:
		// 超时：调用方不等了，但那个 goroutine 还在 yamux 里等 ACK。
		// 起一个回收协程：等它拿到 stream 就立刻关掉，
		// 否则会在对端留下一条永远不关的流（占对端资源、也占窗口）。
		go func() {
			r := <-ch
			if r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, fmt.Errorf("relay: 打开复用流超时（%s）", timeout)
	}
}

// candidatesFor 返回本次请求的候选通道（按优先级排）。
//
// 首选是当前主通道，后面是其它可用通道。
//
// 「首选通道坏了就换一条」是**通道级隔离**的关键：
// 一条直连通道出问题，绝不能连带把兜底通道也拖下水
// （更不能拖垮整个 DNS 转发器）。
func (s *agentSession) candidatesFor(dst string, now time.Time, mode string) []*channel {
	sel := s.selectChannel(now, mode)
	if sel == nil {
		return nil
	}
	out := []*channel{sel}
	for _, c := range s.channels() {
		if c == sel {
			continue
		}
		if !c.usable(now) {
			continue
		}
		switch mode {
		case TransportTLS:
			if c.kind != ChannelTLS {
				continue
			}
		case TransportZT:
			if c.kind != ChannelZT {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// pickProbeChannel 为探测选一条通道
//
// ★ 为什么探测要避开业务主通道 （审计「第 3 批 #14」）：
//
//	探测与业务流量共用同一个 yamux 会话 ⇒ **共享同一条 TCP 的拥塞窗口**
//	⇒ 一条几 MB 的业务传输能把窗口占满
//	⇒ 探测的小请求排在它后面 ⇒ 探测超时
//	⇒ 而此时通道其实好得很 （只是被数据挤了 ）
//	⇒ 探测超时会被当成「出口链路失败」⇒ **误杀好出口 ⇒ 熔断级联**
//
// ⇒ 所以探测优先选：**不是**业务主通道、且在途流最少的那条
//
// ★ 退化情况 ：只有一条通道时仍然用它
//
//	（总不能让「没别的通道」变成「不探测」——那更糟 ）
func (s *agentSession) pickProbeChannel(now time.Time, mode string) *channel {
	// ★ 排除的是**全局业务主通道** s.primary
	//
	// ★ 为什么不是 selectChannel(now, mode) ：
	//   ① selectChannel **会写 s.primary**（业务主通道）
	//      ⇒ 探测这个「看起来只读」的路径，却改了业务路由
	//      ⇒ 在 tls/zt 模式下，它会把全局业务主通道改写成该模式下评分最高的
	//   ② 它按 mode 过滤后选出来的那条，**未必是承载业务的那条**
	//      ⇒ 业务走 ZT、探测按 tls 选 ⇒ 排除的是 TLS 那条
	//      ⇒ 探测照样会打到 ZT 上 ⇒ **"与业务错开"的目标落空**
	//
	// ⇒ 正确做法：★ 排除全局业务主通道 s.primary
	//   它才是真正承载业务流量的那条
	s.chMu.Lock()
	businessPrimary := s.primary
	s.chMu.Unlock()

	if businessPrimary == nil {
		// 还没有主通道（首次选路）⇒ 退化成按模式选一条
		if p := s.selectChannelNoSideEffect(now, mode); p != nil {
			return p
		}
		return nil
	}

	var best *channel
	for _, c := range s.channels() {
		if !c.usable(now) {
			continue
		}
		switch mode {
		case TransportTLS:
			if c.kind != ChannelTLS {
				continue
			}
		case TransportZT:
			if c.kind != ChannelZT {
				continue
			}
		}
		// 第一步：先排除业务主通道
		if c == businessPrimary {
			continue
		}
		// 第二步：在剩下的里挑在途流最少的
		if best == nil || c.inflight.Load() < best.inflight.Load() {
			best = c
		}
	}
	if best != nil {
		return best
	}
	// ★ 只有业务主通道可用 ⇒ 退化用它
	//   （总不能让「没别的通道」变成「不探测」——那更糟 ）
	return businessPrimary
}

// DefaultProbeTimeout 是单次通道探测的默认超时（秒）。
//
// 5 秒偏紧：实测直连 RTT 75~267ms，一次探测还要含 yamux 建流开销，
// 而且跨境抖动偶尔就能到秒级。10 秒更稳 ——
// 宁可晚一点发现坏通道，也别把好通道误杀。
const DefaultProbeTimeout = 10 * time.Second

// probeL1 是 agent 级 L1 探测（选路器用它测「网关 ↔ 出口」的链路质量）。
//
// # 为什么与通道健康探测分开用流
//
// 两者目的不同、周期也不同：
//   - L1 探测：每 2 秒一次，喂给选路器（要快、要有连续性）；
//   - 通道探测：每 15 秒一次，判断这条通道健不健康（要稳、宁可慢）。
//
// 共用一条流的话，通道探测的 10 秒超时会把 L1 探测一起卡住
// （L1 探测在等 probeMu），表现为「status 里的延迟不更新」。
// 所以这里给 L1 单独一条流。
func (g *Gateway) probeL1(c *channel, timeout time.Duration) (time.Duration, error) {
	if c.sess == nil || c.sess.IsClosed() {
		return 0, errChannelSessionClosed
	}

	c.l1Mu.Lock()
	defer c.l1Mu.Unlock()

	if c.l1probe == nil {
		st, err := openStreamWithTimeout(c.sess, streamOpenTimeout)
		if err != nil {
			return 0, err
		}
		if err := stream.WriteControlFrame(st, stream.KindProbeHello); err != nil {
			_ = st.Close()
			return 0, err
		}
		c.l1probe = st
	}

	start := time.Now()
	_ = c.l1probe.SetDeadline(start.Add(timeout))
	if err := stream.WriteControlFrame(c.l1probe, stream.KindPing); err != nil {
		_ = c.l1probe.Close()
		c.l1probe = nil
		return 0, err
	}
	kind, _, err := stream.ReadFrame(c.l1probe)
	if err != nil {
		_ = c.l1probe.Close()
		c.l1probe = nil
		return 0, err
	}
	if kind != stream.KindPong {
		_ = c.l1probe.Close()
		c.l1probe = nil
		return 0, fmt.Errorf("relay: L1 探测通道收到意外帧类型 %d", kind)
	}
	rtt := time.Since(start)
	_ = c.l1probe.SetDeadline(time.Time{})
	return rtt, nil
}
