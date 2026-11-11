// Package mesh 实现节点发现与拓扑维护。
//
// 发现机制（DESIGN.md §4）：
//
//  1. 主路径：在 ZeroTier 虚拟接口上 UDP 广播 HELLO。ZT 网络配置里
//     broadcastEnabled=true 时秒级成网，零配置。
//  2. 兜底：广播不可用时，对 ZT 子网做低速单播扫描。
//
// 设计取舍：
//   - 广播只用来「喊话」，回应一律走单播，避免广播风暴；
//   - 只要收到任何消息都刷新 last_seen，不要求严格的心跳周期；
//   - 节点状态用「距上次收到消息的时长」判定，不做协商，避免状态机分歧。
package mesh

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

// State 是节点连通状态。
type State int

const (
	// StateAlive 最近有消息往来。
	StateAlive State = iota
	// StateStale 一段时间没消息，但还没判定下线。
	StateStale
	// StateDown 已判定下线。
	StateDown
)

func (s State) String() string {
	switch s {
	case StateAlive:
		return "alive"
	case StateStale:
		return "stale"
	case StateDown:
		return "down"
	default:
		return "?"
	}
}

// Chinese 返回中文标签，用于 CLI 展示。
func (s State) Chinese() string {
	switch s {
	case StateAlive:
		return "在线"
	case StateStale:
		return "迟滞"
	case StateDown:
		return "离线"
	default:
		return "?"
	}
}

// 状态判定阈值。集中定义，便于调参与测试。
const (
	// AliveWithin 内的消息算「在线」。
	AliveWithin = 15 * time.Second
	// StaleWithin 内算「迟滞」，超过即「离线」。
	StaleWithin = 45 * time.Second
)

// Peer 是一个已发现的对端节点。
type Peer struct {
	// NodeID 对端身份，由对方密钥+机器指纹派生，跨重启稳定。
	NodeID string
	// ZTAddr 我们从哪个地址收到它的消息（通常是对方的 ZT 虚拟 IP）。
	ZTAddr *net.UDPAddr
	// AdvertiseAddr 对方声称的直连地址，形如 "129.225.146.84:51820"。
	//
	// 这是 Oracle 那批 1:1 NAT 机器的关键：它们通告给 ZT 的 endpoint 是内网地址，
	// ZT 走不通，只能靠这个公网地址直连。
	AdvertiseAddr string

	SWVersion    string
	Capabilities []string
	Transports   []string
	Region       string
	AgreedProto  int

	// 出口负载信息（由 heartbeat 更新），选路时用于容量惩罚。
	Load        float64
	ActiveFlows int
	UpBps       int64
	DownBps     int64

	FirstSeen time.Time
	LastSeen  time.Time

	// L1 观测指标（由探测模块填充；M1 阶段先留空）。
	RTT      time.Duration
	LossRate float64
}

// State 计算当前状态。
func (p *Peer) State(now time.Time) State {
	ago := now.Sub(p.LastSeen)
	switch {
	case ago <= AliveWithin:
		return StateAlive
	case ago <= StaleWithin:
		return StateStale
	default:
		return StateDown
	}
}

// Age 返回距上次消息的时长。
func (p *Peer) Age(now time.Time) time.Duration { return now.Sub(p.LastSeen) }

// Clone 返回一份拷贝，避免调用方持有内部指针。
func (p *Peer) Clone() *Peer {
	cp := *p
	cp.Capabilities = append([]string(nil), p.Capabilities...)
	cp.Transports = append([]string(nil), p.Transports...)
	if p.ZTAddr != nil {
		addr := *p.ZTAddr
		cp.ZTAddr = &addr
	}
	return &cp
}

// HasCap 判断是否具备某能力。
func (p *Peer) HasCap(cap string) bool {
	for _, c := range p.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// PeerTable 是并发安全的节点表。
type PeerTable struct {
	mu    sync.RWMutex
	peers map[string]*Peer
}

// NewPeerTable 创建空表。
func NewPeerTable() *PeerTable {
	return &PeerTable{peers: make(map[string]*Peer)}
}

// Upsert 插入或更新节点。返回是否为首次发现（便于日志只在首次打印）。
//
// 用 updater 回调修改，避免调用方拿着锁外的指针乱改。
func (t *PeerTable) Upsert(nodeID string, updater func(p *Peer, isNew bool)) (isNew bool) {
	return t.upsertAt(nodeID, time.Now(), updater)
}

// upsertAt 是 Upsert 的内部实现，允许指定「视为最后一次消息的时间」。
//
// 缓存恢复时需要这个能力：恢复出来的节点总线时间应当是「过去」，
// 从而正确落入 stale 态，而不是被当成刚刚在线。
func (t *PeerTable) upsertAt(nodeID string, seenAt time.Time, updater func(p *Peer, isNew bool)) (isNew bool) {
	if nodeID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	p, ok := t.peers[nodeID]
	isNew = !ok
	if isNew {
		p = &Peer{NodeID: nodeID, FirstSeen: seenAt, LastSeen: seenAt}
		t.peers[nodeID] = p
	}
	updater(p, isNew)
	p.LastSeen = seenAt
	return isNew
}

// Get 取一个节点的拷贝。
func (t *PeerTable) Get(nodeID string) (*Peer, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	p, ok := t.peers[nodeID]
	if !ok {
		return nil, false
	}
	return p.Clone(), true
}

// Snapshot 返回全部节点的拷贝，按 node_id 排序保证输出稳定。
func (t *PeerTable) Snapshot() []*Peer {
	t.mu.RLock()
	out := make([]*Peer, 0, len(t.peers))
	for _, p := range t.peers {
		out = append(out, p.Clone())
	}
	t.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// AddrOf 返回某节点的隧道地址，不拷贝。
//
// 专为数据面热路径提供：每个包都要查一次出口地址，
// 走 Get() 会拷贝 Peer 造成每包一次分配。
// 返回的指针只读——节点地址更新时是整体替换指针，不会原地改写。
func (t *PeerTable) AddrOf(nodeID string) *net.UDPAddr {
	t.mu.RLock()
	defer t.mu.RUnlock()
	p, ok := t.peers[nodeID]
	if !ok {
		return nil
	}
	return p.ZTAddr
}

// FirstEgressAddr 返回第一个可用出口的隧道地址，不拷贝、不分配。
//
// 供数据面在「未显式指定出口」时每包现查使用。
// 按 node_id 排序取最小，保证同样拓扑下选择稳定（M4 会换成按评分选）。
func (t *PeerTable) FirstEgressAddr(excludeID string, now time.Time) *net.UDPAddr {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// 两轮：先找在线候选；都没有就退而用「迟滞」候选。
	//
	// 为什么要退：ZT 广播并不对称，某些拓扑下节点可能几十秒收不到对端消息，
	// 状态落进迟滞态，但链路其实是通的。此时若直接判"没有出口"，
	// 流量会被全部丢弃，而实际还能走 —— 宁可发出去失败，也不要凭空掐断。
	var bestID string
	var bestAddr *net.UDPAddr
	var staleID string
	var staleAddr *net.UDPAddr

	for id, p := range t.peers {
		if id == excludeID || p.ZTAddr == nil || !p.HasCap("egress") {
			continue
		}
		if now.Sub(p.LastSeen) <= AliveWithin {
			if bestID == "" || id < bestID {
				bestID, bestAddr = id, p.ZTAddr
			}
		} else if staleID == "" || id < staleID {
			staleID, staleAddr = id, p.ZTAddr
		}
	}
	if bestAddr != nil {
		return bestAddr
	}
	return staleAddr
}

// Len 返回节点数。
func (t *PeerTable) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.peers)
}

// Prune 移除超过 maxAge 完全没有消息的节点，返回移除数量。
func (t *PeerTable) Prune(now time.Time, maxAge time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for id, p := range t.peers {
		if now.Sub(p.LastSeen) > maxAge {
			delete(t.peers, id)
			n++
		}
	}
	return n
}

// EgressCandidates 返回可用作出口的节点。
//
// 判定：具备 egress 能力，且状态为 alive。
func (t *PeerTable) EgressCandidates(now time.Time) []*Peer {
	var out []*Peer
	for _, p := range t.Snapshot() {
		if !p.HasCap("egress") {
			continue
		}
		if p.State(now) != StateAlive {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Count 统计各状态的节点数。
func (t *PeerTable) Count(now time.Time) map[State]int {
	m := map[State]int{}
	for _, p := range t.Snapshot() {
		m[p.State(now)]++
	}
	return m
}

// String 便于日志输出。
func (p *Peer) String() string {
	addr := "-"
	if p.ZTAddr != nil {
		addr = p.ZTAddr.String()
	}
	return fmt.Sprintf("%s@%s", p.NodeID, addr)
}
