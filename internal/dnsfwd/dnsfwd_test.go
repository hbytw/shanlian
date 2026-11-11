package dnsfwd

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

// buildQuery 构造一个 A 查询报文。
func buildQuery(name string, qtype uint16) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], 0x1234) // id
	binary.BigEndian.PutUint16(b[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(b[4:6], 1)      // QDCOUNT
	for _, label := range splitName(name) {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	var t [4]byte
	binary.BigEndian.PutUint16(t[0:2], qtype)
	binary.BigEndian.PutUint16(t[2:4], 1) // IN
	b = append(b, t[:]...)
	return b
}

func splitName(n string) []string {
	var out []string
	cur := ""
	for _, c := range n {
		if c == '.' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// parseAnswer 从应答报文里取出所有 A 记录。
func parseAnswer(t *testing.T, b []byte) (rcode int, ips []net.IP) {
	t.Helper()
	if len(b) < 12 {
		t.Fatal("应答太短")
	}
	flags := binary.BigEndian.Uint16(b[2:4])
	if flags&0x8000 == 0 {
		t.Fatal("QR 位未置位，不是应答")
	}
	rcode = int(flags & 0x000F)
	qd := int(binary.BigEndian.Uint16(b[4:6]))
	an := int(binary.BigEndian.Uint16(b[6:8]))

	off := 12
	for i := 0; i < qd; i++ {
		_, next, err := parseName(b, off)
		if err != nil {
			t.Fatalf("解析问题段失败: %v", err)
		}
		off = next + 4
	}
	for i := 0; i < an; i++ {
		if off+12 > len(b) {
			t.Fatalf("应答记录越界 (i=%d off=%d len=%d)", i, off, len(b))
		}
		rtype := binary.BigEndian.Uint16(b[off+2 : off+4])
		rdlen := int(binary.BigEndian.Uint16(b[off+10 : off+12]))
		if off+12+rdlen > len(b) {
			t.Fatalf("RDATA 越界")
		}
		if rtype == typeA && rdlen == 4 {
			ips = append(ips, net.IP(append([]byte(nil), b[off+12:off+12+4]...)))
		}
		off += 12 + rdlen
	}
	return rcode, ips
}

func TestParseQuery(t *testing.T) {
	b := buildQuery("www.example.com", typeA)
	q, err := parseQuery(b)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if q.name != "www.example.com" {
		t.Fatalf("域名不符: %q", q.name)
	}
	if q.qtype != typeA {
		t.Fatalf("类型不符: %d", q.qtype)
	}
	if !q.rd {
		t.Fatal("RD 标志应被识别")
	}
	if q.id != 0x1234 {
		t.Fatalf("ID 不符: %#x", q.id)
	}
}

func TestParseQuery_RejectsGarbage(t *testing.T) {
	if _, err := parseQuery([]byte{1, 2, 3}); err == nil {
		t.Fatal("过短报文应报错")
	}
	// 没有查询段
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[4:6], 0)
	if _, err := parseQuery(b); err == nil {
		t.Fatal("QDCOUNT=0 应报错")
	}
}

func TestBuildResponse(t *testing.T) {
	q, _ := parseQuery(buildQuery("a.b.com", typeA))
	resp := buildResponse(q, []net.IP{net.ParseIP("1.2.3.4"), net.ParseIP("5.6.7.8")}, rcodeSuccess, defaultTTL)

	rcode, ips := parseAnswer(t, resp)
	if rcode != rcodeSuccess {
		t.Fatalf("rcode 应为 0，实际 %d", rcode)
	}
	if len(ips) != 2 {
		t.Fatalf("应有 2 条 A 记录，实际 %d", len(ips))
	}
	if !ips[0].Equal(net.ParseIP("1.2.3.4")) || !ips[1].Equal(net.ParseIP("5.6.7.8")) {
		t.Fatalf("IP 不符: %v", ips)
	}
	if binary.BigEndian.Uint16(resp[0:2]) != 0x1234 {
		t.Fatal("应答 ID 应与查询一致")
	}
}

func TestBuildResponse_FiltersIPv6(t *testing.T) {
	q, _ := parseQuery(buildQuery("x.com", typeA))
	resp := buildResponse(q, []net.IP{
		net.ParseIP("2001:db8::1"),
		net.ParseIP("1.2.3.4"),
	}, rcodeSuccess, defaultTTL)
	_, ips := parseAnswer(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.2.3.4")) {
		t.Fatalf("应只保留 IPv4，实际 %v", ips)
	}
}

func TestBuildResponse_EmptyIsValid(t *testing.T) {
	q, _ := parseQuery(buildQuery("x.com", typeAAAA))
	resp := buildResponse(q, nil, rcodeSuccess, defaultTTL)
	rcode, ips := parseAnswer(t, resp)
	if rcode != rcodeSuccess {
		t.Fatalf("AAAA 空应答 rcode 应为 0，实际 %d", rcode)
	}
	if len(ips) != 0 {
		t.Fatalf("AAAA 不应返回 A 记录，实际 %v", ips)
	}
	// ANCOUNT 必须是 0
	if got := binary.BigEndian.Uint16(resp[6:8]); got != 0 {
		t.Fatalf("ANCOUNT 应为 0，实际 %d", got)
	}
}

// —— 策略测试 ——

type fakePolicy struct {
	viaEgress map[string]bool
	resolved  []string
}

func (p *fakePolicy) ShouldResolveViaEgress(d string) bool { return p.viaEgress[d] }
func (p *fakePolicy) PickEgress(d string) string {
	if p.viaEgress[d] {
		return "egA"
	}
	return ""
}
func (p *fakePolicy) Observe(d, eg string, ips []net.IP) {
	if eg == "" {
		eg = "本地"
	}
	p.resolved = append(p.resolved, d+"→"+eg)
}

func newTestServer(t *testing.T, pol Policy) *Server {
	t.Helper()
	s, err := New(Options{
		Addr:   "127.0.0.1:0",
		Local:  func(d string) ([]net.IP, error) { return []net.IP{net.ParseIP("9.9.9.9")}, nil },
		Policy: pol,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.SetEgressResolver("egA", func(d string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("1.1.1.1")}, nil
	})
	return s
}

func TestHandle_LocalPath(t *testing.T) {
	s := newTestServer(t, nil)
	resp := s.handle(buildQuery("local.com", typeA))
	_, ips := parseAnswer(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("9.9.9.9")) {
		t.Fatalf("应走本地解析，实际 %v", ips)
	}
	q, l, e, f := s.Stats()
	if q != 0 { // handle 不走 serve 的计数
		t.Fatalf("queries 计数不应增加: %d", q)
	}
	if l != 1 || e != 0 || f != 0 {
		t.Fatalf("统计不符: local=%d egress=%d fail=%d", l, e, f)
	}
}

func TestHandle_EgressPath(t *testing.T) {
	pol := &fakePolicy{viaEgress: map[string]bool{"youtube.com": true}}
	s := newTestServer(t, pol)
	resp := s.handle(buildQuery("youtube.com", typeA))
	_, ips := parseAnswer(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.1.1.1")) {
		t.Fatalf("应走出口解析，实际 %v", ips)
	}
	if len(pol.resolved) != 1 || pol.resolved[0] != "youtube.com→egA" {
		t.Fatalf("亲和回调不符: %v", pol.resolved)
	}
	_, l, e, _ := s.Stats()
	if l != 0 || e != 1 {
		t.Fatalf("统计不符: local=%d egress=%d", l, e)
	}
}

func TestHandle_EgressFailsFallbackToLocal(t *testing.T) {
	pol := &fakePolicy{viaEgress: map[string]bool{"x.com": true}}
	s := newTestServer(t, pol)
	// 出口解析器失败 → 应回落到本地
	s.SetEgressResolver("egA", func(d string) ([]net.IP, error) {
		return nil, errors.New("出口解析失败")
	})
	resp := s.handle(buildQuery("x.com", typeA))
	_, ips := parseAnswer(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("9.9.9.9")) {
		t.Fatalf("出口失败应回落本地，实际 %v", ips)
	}
}

func TestHandle_AAAA(t *testing.T) {
	s := newTestServer(t, nil)
	resp := s.handle(buildQuery("x.com", typeAAAA))
	_, ips := parseAnswer(t, resp)
	if len(ips) != 0 {
		t.Fatalf("AAAA 不应返回 A 记录，实际 %v", ips)
	}
}

func TestHandle_LocalFailureReturnsServfail(t *testing.T) {
	s, _ := New(Options{
		Addr: "127.0.0.1:0",
		Local: func(d string) ([]net.IP, error) {
			return nil, errors.New("解析失败")
		},
	})
	resp := s.handle(buildQuery("bad.com", typeA))
	rcode, _ := parseAnswer(t, resp)
	if rcode != rcodeServerFailure {
		t.Fatalf("本地解析失败应返回 SERVFAIL，实际 rcode=%d", rcode)
	}
}

// TestEndToEnd 真起一个 UDP 监听，跑一次完整往返。
func TestEndToEnd(t *testing.T) {
	s, err := New(Options{
		Addr:  "127.0.0.1:0",
		Local: func(d string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.1.2.3")}, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	conn, err := net.Dial("udp", s.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(buildQuery("hello.test", typeA)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	_, ips := parseAnswer(t, buf[:n])
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("10.1.2.3")) {
		t.Fatalf("往返结果不符: %v", ips)
	}

	q, _, _, _ := s.Stats()
	if q != 1 {
		t.Fatalf("queries 应计到 1，实际 %d", q)
	}
}

func TestServer_CloseIsIdempotent(t *testing.T) {
	s, _ := New(Options{Addr: "127.0.0.1:0", Local: func(string) ([]net.IP, error) { return nil, nil }})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Close()
	s.Close() // 不应 panic
}
