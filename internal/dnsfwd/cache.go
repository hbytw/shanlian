package dnsfwd

import (
	"container/list"
	"net"
	"sync"
	"time"
)

// cacheItem 是一条缓存记录。
type cacheItem struct {
	key string
	ips []net.IP
	// viaEgress 记着这条结果是经哪个出口解析出来的。
	//
	// 必须记：命中缓存时要把同样的出口 id 交给 policy.Observe，
	// 否则「域名 ↔ 出口亲和」的学习就断了 —— 缓存命中一次，
	// 亲和关系就丢一次。
	viaEgress string
	// until 是这条记录的失效时刻。
	until time.Time
}

// resultCache 是解析结果的短 TTL 缓存（LRU 淘汰）。
//
// # 只存成功的 A 记录
//
// 失败与 NXDOMAIN **一律不缓存**：一次网络抖动不应该把一个域名
// 钉死 30 秒（那会让「刚才好好的」变成「整整 30 秒都打不开」）。
//
// # 并发安全
//
// 转发器是多 goroutine 的（每个客户端连接一个），所以缓存自带锁。
type resultCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // 链表首元素 = 最久未使用
	ttl     time.Duration
	max     int

	// now 可注入，便于测试控制时间。
	now func() time.Time
}

// newResultCache 创建缓存。
func newResultCache(ttl time.Duration, max int) *resultCache {
	if ttl <= 0 {
		ttl = cacheTTL
	}
	if max <= 0 {
		max = cacheMaxEntries
	}
	return &resultCache{
		entries: make(map[string]*list.Element, max),
		order:   list.New(),
		ttl:     ttl,
		max:     max,
		now:     time.Now,
	}
}

// get 查缓存。
//
// 返回：结果、是否命中、**剩余寿命**（用于决定回给客户端的 TTL）。
//
// 剩余寿命必须一并返回：缓存 30 秒却告诉客户端 60 秒的话，
// 客户端会在缓存失效后的一段时间里继续用那个（可能已经过期的）地址。
func (c *resultCache) get(key string) (ips []net.IP, viaEgress string, ok bool, remaining time.Duration) {
	if c == nil || key == "" {
		return nil, "", false, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, hit := c.entries[key]
	if !hit {
		return nil, "", false, 0
	}
	item := el.Value.(*cacheItem)
	now := c.now()
	if !now.Before(item.until) {
		// 已过期：顺手删掉。
		c.removeElement(el)
		return nil, "", false, 0
	}
	// LRU：命中就挪到队尾（最新使用）。
	c.order.MoveToBack(el)
	return item.ips, item.viaEgress, true, item.until.Sub(now)
}

// put 写入一条结果（只应由调用方在**解析成功**后调用）。
func (c *resultCache) put(key string, ips []net.IP, viaEgress string) {
	if c == nil || key == "" || len(ips) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	item := &cacheItem{
		key:       key,
		ips:       append([]net.IP(nil), ips...),
		viaEgress: viaEgress,
		until:     now.Add(c.ttl),
	}

	if el, hit := c.entries[key]; hit {
		el.Value = item
		c.order.MoveToBack(el)
		return
	}

	el := c.order.PushBack(item)
	c.entries[key] = el

	// 超容量：淘汰最久未用的。
	for c.order.Len() > c.max {
		oldest := c.order.Front()
		if oldest == nil {
			break
		}
		c.removeElement(oldest)
	}
}

// removeElement 从 map 与 LRU 链表里删掉一条（调用方必须持锁）。
func (c *resultCache) removeElement(el *list.Element) {
	item := el.Value.(*cacheItem)
	delete(c.entries, item.key)
	c.order.Remove(el)
}

// len 返回当前条目数（测试与诊断用）。
func (c *resultCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// purge 清空缓存（测试与诊断用）。
func (c *resultCache) purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*list.Element, c.max)
	c.order.Init()
}
