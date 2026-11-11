package dnsfwd

import (
	"net"
	"testing"
	"time"
)

// hangPolicy 让指定域名走出口解析（出口解析器会卡住），
// 其它域名走本地解析。
type hangPolicy struct {
	hang string
}

func (p *hangPolicy) ShouldResolveViaEgress(domain string) bool {
	return domain == p.hang
}

func (p *hangPolicy) PickEgress(domain string) string { return "eg1" }

func (p *hangPolicy) Observe(domain, egressID string, ips []net.IP) {}

// TestServe_SlowQueryDoesNotBlockOthers 是 2026-11-11 P2 现场事故的回归测试：
//
// 出口解析卡住（网关侧死锁）时，**其它查询必须照常得到应答**。
//
// 事故经过：DNS 转发器原来是**串行**处理查询的，
// 第一个卡住的查询把整个服务堵死 → 客户端 20 秒超时 → 全屋断网。
// 修法：每个查询独立处理（并发 + 有界），卡住的那条只影响它自己。
func TestServe_SlowQueryDoesNotBlockOthers(t *testing.T) {
	block := make(chan struct{}) // 永不关闭 = 出口解析永久卡住
	defer close(block)

	srv, err := New(Options{
		Addr: "127.0.0.1:0",
		Local: func(domain string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("1.2.3.4")}, nil
		},
		ByEgress: func(nodeID string) Resolver {
			return func(domain string) ([]net.IP, error) {
				<-block
				return nil, nil
			}
		},
		Policy: &hangPolicy{hang: "hang.example.com"},
	})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("启动转发器失败: %v", err)
	}
	defer srv.Close()

	addr := srv.Addr()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	// 先发一个会卡住的查询。
	if _, err := conn.Write(buildQuery("hang.example.com", typeA)); err != nil {
		t.Fatalf("发送查询失败: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// 再发一个正常查询 —— 它必须很快就有应答。
	start := time.Now()
	if _, err := conn.Write(buildQuery("fast.example.com", typeA)); err != nil {
		t.Fatalf("发送查询失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("正常查询没有及时得到应答（被慢查询堵死了）: %v", err)
	}
	if n < 12 {
		t.Fatal("应答太短")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("正常查询响应过慢: %s", d)
	}

	// 应答必须是对 fast.example.com 的那条（不能串到卡住的那条上）。
	rcode, ips := parseAnswer(t, buf[:n])
	if rcode != rcodeSuccess {
		t.Fatalf("rcode 应为 0，实际 %d", rcode)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.2.3.4")) {
		t.Fatalf("应答内容不对: %v", ips)
	}
}

// TestServe_ConcurrentQueries 验证并发查询都能拿到应答。
func TestServe_ConcurrentQueries(t *testing.T) {
	srv, err := New(Options{
		Addr: "127.0.0.1:0",
		Local: func(domain string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("1.2.3.4")}, nil
		},
	})
	if err != nil {
		t.Fatalf("创建转发器失败: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("启动转发器失败: %v", err)
	}
	defer srv.Close()

	conn, err := net.Dial("udp", srv.Addr())
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			if _, err := conn.Write(buildQuery("x.example.com", typeA)); err != nil {
				done <- err
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 1500)
			if _, err := conn.Read(buf); err != nil {
				done <- err
				return
			}
			done <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("第 %d 个查询失败: %v", i, err)
		}
	}
}
