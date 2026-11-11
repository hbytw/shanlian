package dnsfwd

import (
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// answerTTL 取出应答里第一个 A 记录的 TTL（秒）。
func answerTTL(t *testing.T, b []byte) uint32 {
	t.Helper()
	if len(b) < 12 {
		t.Fatal("应答太短")
	}
	qd := int(binary.BigEndian.Uint16(b[4:6]))
	an := int(binary.BigEndian.Uint16(b[6:8]))
	if an == 0 {
		return 0
	}
	off := 12
	for i := 0; i < qd; i++ {
		_, next, err := parseName(b, off)
		if err != nil {
			t.Fatalf("解析问题段失败: %v", err)
		}
		off = next + 4
	}
	if off+12 > len(b) {
		t.Fatal("应答记录越界")
	}
	return binary.BigEndian.Uint32(b[off+6 : off+10])
}

// countingResolver 是一个会记录调用次数的解析器。
type countingResolver struct {
	calls atomic.Int64
	ips   []net.IP
	err   error
	delay time.Duration
}

func (c *countingResolver) resolve(domain string) ([]net.IP, error) {
	c.calls.Add(1)
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.ips, nil
}

// TestCache_HitSkipsSecondResolution 覆盖验收项：
// 命中缓存后**不再触发第二次解析**（出口解析器一次都不该被叫第二次）。
func TestCache_HitSkipsSecondResolution(t *testing.T) {
	local := &countingResolver{ips: []net.IP{net.ParseIP("1.2.3.4")}}
	egress := &countingResolver{ips: []net.IP{net.ParseIP("5.6.7.8")}}

	s, err := New(Options{
		Local:    local.resolve,
		ByEgress: func(string) Resolver { return egress.resolve },
		Policy:   &testPolicy{viaEgress: true, egress: "egA"},
	})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}

	q := buildQuery("www.example.com", typeA)

	// 第一次：未命中 → 走出口解析
	resp := s.handle(q)
	_, ips := parseAnswer(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("5.6.7.8")) {
		t.Fatalf("首次应走出口解析，实际 %v", ips)
	}
	if egress.calls.Load() != 1 {
		t.Fatalf("出口解析器应被调用 1 次，实际 %d", egress.calls.Load())
	}

	// 第二次：命中 → **不能再问出口**
	resp = s.handle(q)
	_, ips = parseAnswer(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("5.6.7.8")) {
		t.Fatalf("命中缓存应返回同一结果，实际 %v", ips)
	}
	if egress.calls.Load() != 1 {
		t.Fatalf("命中缓存后不应再解析，实际出口解析器被调用 %d 次", egress.calls.Load())
	}
	if local.calls.Load() != 0 {
		t.Fatalf("命中缓存不该走本地解析，实际调用 %d 次", local.calls.Load())
	}

	// 计数：命中 1 / 未命中 1
	hits, misses := s.CacheStats()
	if hits != 1 || misses != 1 {
		t.Fatalf("缓存计数不对：命中 %d / 未命中 %d", hits, misses)
	}
}

// TestCache_TTLNotLongerThanRemaining 覆盖验收项：
// 回给客户端的 TTL 必须 ≤ 缓存剩余寿命。
func TestCache_TTLNotLongerThanRemaining(t *testing.T) {
	local := &countingResolver{ips: []net.IP{net.ParseIP("1.2.3.4")}}
	s, err := New(Options{Local: local.resolve})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	q := buildQuery("ttl.example.com", typeA)

	// 第一次：刚解析出来 → TTL 应为缓存时长（30 秒），不超过 defaultTTL
	resp := s.handle(q)
	ttl := answerTTL(t, resp)
	if ttl > defaultTTL {
		t.Fatalf("TTL %d 超过 defaultTTL %d", ttl, defaultTTL)
	}
	if ttl > uint32(cacheTTL/time.Second) {
		t.Fatalf("TTL %d 超过缓存时长 %v", ttl, cacheTTL)
	}

	// 把缓存的剩余寿命调到只剩 5 秒 → TTL 必须跟着降下来
	s.cache.now = func() time.Time { return time.Now().Add(cacheTTL - 5*time.Second) }
	resp = s.handle(q)
	ttl = answerTTL(t, resp)
	if ttl > 5 {
		t.Fatalf("剩余寿命 5 秒时 TTL 应 ≤ 5，实际 %d", ttl)
	}
	if ttl == 0 {
		t.Fatal("TTL 不应为 0（客户端会立刻重查，缓存就白做了）")
	}
}

// TestCache_FailureNotCached 覆盖验收项：
// 失败结果不进缓存（一次抖动不该把域名钉死 30 秒）。
func TestCache_FailureNotCached(t *testing.T) {
	local := &countingResolver{err: errors.New("上游不可用")}
	s, err := New(Options{Local: local.resolve})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	q := buildQuery("fail.example.com", typeA)

	for i := 0; i < 3; i++ {
		resp := s.handle(q)
		rcode, _ := parseAnswer(t, resp)
		if rcode != rcodeServerFailure {
			t.Fatalf("第 %d 次应返回 SERVFAIL，实际 rcode=%d", i+1, rcode)
		}
	}
	if local.calls.Load() != 3 {
		t.Fatalf("失败不该被缓存，解析器应被调用 3 次，实际 %d", local.calls.Load())
	}
	if s.CacheLen() != 0 {
		t.Fatalf("失败不该进缓存，实际 %d 条", s.CacheLen())
	}
}

// TestCache_Expiry 覆盖验收项：
// 过了缓存时长就重新解析。
func TestCache_Expiry(t *testing.T) {
	local := &countingResolver{ips: []net.IP{net.ParseIP("1.2.3.4")}}
	s, err := New(Options{Local: local.resolve})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	q := buildQuery("exp.example.com", typeA)

	s.handle(q)
	s.handle(q)
	if local.calls.Load() != 1 {
		t.Fatalf("第二次应命中缓存，解析器调用 %d 次", local.calls.Load())
	}

	// 把时间往前推 31 秒 → 缓存过期
	s.cache.now = func() time.Time { return time.Now().Add(cacheTTL + time.Second) }
	s.handle(q)
	if local.calls.Load() != 2 {
		t.Fatalf("过期后应重新解析，解析器调用 %d 次", local.calls.Load())
	}
}

// TestCache_LRUEviction 确认容量有上限且按 LRU 淘汰。
func TestCache_LRUEviction(t *testing.T) {
	local := &countingResolver{ips: []net.IP{net.ParseIP("1.2.3.4")}}
	s, err := New(Options{Local: local.resolve, CacheMax: 4})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	for i := 0; i < 10; i++ {
		s.handle(buildQuery("d"+string(rune('a'+i))+".example.com", typeA))
	}
	if n := s.CacheLen(); n > 4 {
		t.Fatalf("缓存应不超过 4 条，实际 %d 条", n)
	}
	// 最老的（a）应被淘汰
	s.cache.mu.Lock()
	_, hasOldest := s.cache.entries["a.example.com"]
	s.cache.mu.Unlock()
	if hasOldest {
		t.Fatal("最久未用的条目应被 LRU 淘汰")
	}
}

// TestCache_Concurrent 覆盖验收项：
// 并发安全 —— 多个 goroutine 同时打同一个域名，结果一致、缓存不乱。
//
// 注：这台机器没有 gcc，-race 跑不了，所以用普通并发测试 +
// 计数器断言（缓存条目数正确、命中+未命中=总查询数）。
func TestCache_Concurrent(t *testing.T) {
	local := &countingResolver{ips: []net.IP{net.ParseIP("1.2.3.4")}}
	s, err := New(Options{Local: local.resolve})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	q := buildQuery("race.example.com", typeA)

	const n = 64
	var wg sync.WaitGroup
	results := make([][]net.IP, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := s.handle(q)
			_, ips := parseAnswer(t, resp)
			results[i] = ips
		}(i)
	}
	wg.Wait()

	for i, ips := range results {
		if len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.2.3.4")) {
			t.Fatalf("第 %d 个 goroutine 拿到的结果不对：%v", i, ips)
		}
	}
	if n := s.CacheLen(); n != 1 {
		t.Fatalf("并发后缓存应只有 1 条，实际 %d 条", n)
	}
	hits, misses := s.CacheStats()
	if hits+misses != n {
		t.Fatalf("命中+未命中应等于总查询数 %d，实际 %d+%d=%d", n, hits, misses, hits+misses)
	}
}

// testPolicy 是一个固定的测试策略。
type testPolicy struct {
	viaEgress bool
	egress    string
	observed  []string
}

func (p *testPolicy) ShouldResolveViaEgress(domain string) bool { return p.viaEgress }
func (p *testPolicy) PickEgress(domain string) string           { return p.egress }
func (p *testPolicy) Observe(domain, egressID string, ips []net.IP) {
	p.observed = append(p.observed, domain+"@"+egressID)
}

// TestCache_KeepsEgressAffinity 确认命中缓存时仍然上报原出口
// （否则「域名 ↔ 出口亲和」的学习会断）。
func TestCache_KeepsEgressAffinity(t *testing.T) {
	local := &countingResolver{ips: []net.IP{net.ParseIP("1.2.3.4")}}
	egress := &countingResolver{ips: []net.IP{net.ParseIP("5.6.7.8")}}
	pol := &testPolicy{viaEgress: true, egress: "egA"}
	s, err := New(Options{
		Local:    local.resolve,
		ByEgress: func(string) Resolver { return egress.resolve },
		Policy:   pol,
	})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	q := buildQuery("aff.example.com", typeA)

	s.handle(q)
	s.handle(q) // 命中
	if len(pol.observed) != 2 {
		t.Fatalf("两次解析都应上报 Observe，实际 %d 次", len(pol.observed))
	}
	for _, o := range pol.observed {
		if o != "aff.example.com@egA" {
			t.Fatalf("命中时也应上报原出口，实际 %q", o)
		}
	}
}
