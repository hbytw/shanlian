package cli

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// 带宽被动估算参数。
const (
	// bwMinDuration 是参与估算的最短连接时长。
	//
	// 太短的连接（握手、小请求）测出来的是抖动不是带宽，
	// 会把一条慢链路估成「几十 Gbps」。
	bwMinDuration = time.Second
	// bwMinBytes 是参与估算的最少传输量。同理，几百字节的连接没有参考价值。
	bwMinBytes = 64 * 1024
	// bwWindow 是峰值保留窗口。超过这个时长的旧样本不再使用。
	bwWindow = 3 * time.Minute
)

// bwTracker 被动统计每个 agent 的中继吞吐，用峰值作为带宽估计。
//
// 为什么被动而不是主动测速：
//   - 主动灌 256KB 去测，在家庭宽带上就是实打实吃掉用户 256KB 上行；
//   - packet 模式的 L2 是「下载真实网站测速」，proxy 模式下没有等价物
//     （不能为了测速去替用户连一个站点）；
//   - 而中继本来就在搬运真实数据，顺手统计几乎零成本。
//
// 代价是「没有长连接时就估不出带宽」，此时返回 0，选路器会走 fallbackBWMbps 兜底。
type bwTracker struct {
	mu    sync.Mutex
	stats map[string]*bwStat
}

type bwStat struct {
	bestMbps float64
	bestAt   time.Time
}

func newBWTracker() *bwTracker {
	return &bwTracker{stats: make(map[string]*bwStat)}
}

// observe 记录一次连接结算出来的吞吐。
func (t *bwTracker) observe(nodeID string, mbps float64) {
	if nodeID == "" || mbps <= 0 {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stats[nodeID]
	if st == nil {
		st = &bwStat{}
		t.stats[nodeID] = st
	}
	// 只保留窗口内的峰值：链路降速后旧峰值会过期，不会永久高估
	if now.Sub(st.bestAt) > bwWindow || mbps > st.bestMbps {
		st.bestMbps = mbps
		st.bestAt = now
	}
}

// Bandwidth 返回某 agent 的带宽估计（Mbps）；无有效样本时返回 0。
func (t *bwTracker) Bandwidth(nodeID string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stats[nodeID]
	if st == nil {
		return 0
	}
	if time.Since(st.bestAt) > bwWindow {
		delete(t.stats, nodeID)
		return 0
	}
	return st.bestMbps
}

// prune 清掉已断开 agent 的统计，避免随重连次数增长。
func (t *bwTracker) prune(alive map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.stats {
		if !alive[id] {
			delete(t.stats, id)
		}
	}
}

// countedConn 包一层 net.Conn，统计双向字节数并在关闭时结算吞吐。
type countedConn struct {
	net.Conn
	nodeID  string
	start   time.Time
	bytes   atomic.Int64
	tracker *bwTracker
	once    sync.Once
}

// wrapCounted 给一条中继流套上统计器。
func wrapCounted(conn net.Conn, nodeID string, tracker *bwTracker) net.Conn {
	if conn == nil || tracker == nil {
		return conn
	}
	return &countedConn{Conn: conn, nodeID: nodeID, start: time.Now(), tracker: tracker}
}

func (c *countedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.bytes.Add(int64(n))
	return n, err
}

func (c *countedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.bytes.Add(int64(n))
	return n, err
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.settle() })
	return err
}

// settle 结算吞吐并上报。只统计够长够大的连接。
func (c *countedConn) settle() {
	elapsed := time.Since(c.start)
	n := c.bytes.Load()
	if elapsed < bwMinDuration || n < bwMinBytes {
		return
	}
	mbps := float64(n) * 8 / elapsed.Seconds() / 1e6
	c.tracker.observe(c.nodeID, mbps)
}
