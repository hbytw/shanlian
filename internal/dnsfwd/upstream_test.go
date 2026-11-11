package dnsfwd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSystemUpstreamsFiltersLoopback 验证回环上游被过滤。
//
// 这是真机事故的回归测试：OpenWrt 的 /etc/resolv.conf 是 nameserver 127.0.0.1
// （dnsmasq 自己）。若不过滤，把 dnsmasq 上游指向本转发器就形成死循环，
// 表现为「监听正常但永不回应」，全屋 DNS 瘫痪。
func TestSystemUpstreamsFiltersLoopback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "resolv.conf")

	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "只有回环 → 走兜底",
			body: "search lan\nnameserver 127.0.0.1\nnameserver ::1\n",
			want: fallbackUpstreams,
		},
		{
			name: "混合 → 只留公网",
			body: "nameserver 127.0.0.1\nnameserver 8.8.8.8\nnameserver 223.5.5.5\n",
			want: []string{"8.8.8.8", "223.5.5.5"},
		},
		{
			name: "空文件 → 兜底",
			body: "",
			want: fallbackUpstreams,
		},
		{
			name: "无 nameserver 行 → 兜底",
			body: "search lan\noptions timeout:2\n",
			want: fallbackUpstreams,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got := systemUpstreamsFrom(p)
			if len(got) != len(c.want) {
				t.Fatalf("期望 %v，实际 %v", c.want, got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("期望 %v，实际 %v", c.want, got)
				}
			}
		})
	}
}

// TestSystemUpstreamsMissingFile 验证文件不存在时走兜底。
func TestSystemUpstreamsMissingFile(t *testing.T) {
	got := systemUpstreamsFrom(filepath.Join(t.TempDir(), "nonexistent"))
	if len(got) != len(fallbackUpstreams) {
		t.Fatalf("文件不存在应走兜底，实际 %v", got)
	}
}

// TestSystemResolverNeverUsesLoopback 验证真正的 SystemResolver 不会拿回环当上游。
func TestSystemResolverNeverUsesLoopback(t *testing.T) {
	for _, u := range systemUpstreams() {
		if u == "127.0.0.1" || u == "::1" {
			t.Fatalf("上游里出现了回环地址 %s，会导致 DNS 死循环", u)
		}
	}
}
