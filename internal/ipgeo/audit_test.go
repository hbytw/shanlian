package ipgeo

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestRangeToCIDRs(t *testing.T) {
	cases := []struct {
		start, end uint32
		want       string
	}{
		{0x0A000000, 0x0A0000FF, "10.0.0.0/24"},
		{0x0A000000, 0x0A0001FF, "10.0.0.0/23"},
		{0x0A000001, 0x0A000001, "10.0.0.1/32"},
		{0xFFFFFFFF, 0xFFFFFFFF, "255.255.255.255/32"},
	}
	for _, c := range cases {
		got := rangeToCIDRs(c.start, c.end)
		if len(got) != 1 || got[0] != c.want {
			t.Fatalf("rangeToCIDRs(%08x,%08x) = %v, want [%s]", c.start, c.end, got, c.want)
		}
	}
}

func TestCIDRToIntervalRoundTrip(t *testing.T) {
	cases := []struct {
		cidr string
		want string
	}{
		{"10.0.0.0/24", "10.0.0.0-10.0.0.255"},
		{"1.1.1.1/32", "1.1.1.1-1.1.1.1"},
		{"223.5.5.0/24", "223.5.5.0-223.5.5.255"},
	}
	for _, c := range cases {
		iv, err := cidrToInterval(c.cidr)
		if err != nil {
			t.Fatalf("cidrToInterval(%s): %v", c.cidr, err)
		}
		got := fmt.Sprintf("%s-%s", u32ToIP(iv.start), u32ToIP(iv.end))
		if got != c.want {
			t.Fatalf("cidrToInterval(%s) = %s, want %s", c.cidr, got, c.want)
		}
	}
}

func TestMergeAndSubtract(t *testing.T) {
	merged := mergeIntervals([]interval{
		{10, 20}, {15, 30}, {40, 50}, {51, 60},
	})
	if len(merged) != 2 {
		t.Fatalf("合并后应剩 2 段，实际 %v", merged)
	}
	if merged[0] != (interval{10, 30}) || merged[1] != (interval{40, 60}) {
		t.Fatalf("合并结果不对: %v", merged)
	}

	// {10,30} 去掉 {20,25} → {10,19}+{26,30}；{40,60} 去掉 {40,50} → {51,60}
	got := subtract(merged, mergeIntervals([]interval{{20, 25}, {40, 50}}))
	want := []interval{{10, 19}, {26, 30}, {51, 60}}
	if len(got) != len(want) {
		t.Fatalf("相减结果段数不对: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("相减结果第 %d 段 = %v, want %v", i, got[i], want[i])
		}
	}

	// 完全被盖住时必须返回空 —— 这里踩过一个坑：
	// 用 {0,0} 当「已覆盖」的哨兵，而空判断 start<=end 把它当成有效区间，
	// 于是每次相减都多吐一个 0.0.0.0/32。
	if got := subtract(merged, merged); len(got) != 0 {
		t.Fatalf("自己减自己应为空，实际 %v", got)
	}
	if got := subtract([]interval{{10, 20}}, []interval{{5, 100}}); len(got) != 0 {
		t.Fatalf("被大区间完全盖住应为空，实际 %v", got)
	}
}

func TestOverlapLevel(t *testing.T) {
	geo := []interval{{10, 20}, {30, 40}}
	if overlapLevel(interval{12, 18}, geo) != overlapFull {
		t.Fatal("完全落在 geo 内应是 full")
	}
	if overlapLevel(interval{18, 25}, geo) != overlapPartial {
		t.Fatal("跨边界应是 partial")
	}
	if overlapLevel(interval{25, 28}, geo) != overlapNone {
		t.Fatal("完全在外应是 none")
	}
}

// TestAuditCNLive 需要真实数据：
//
//	SLX_TEST_XDB      指向 ip2region_v4.xdb
//	SLX_TEST_CN_LIST  指向国内网段表（每行一个 CIDR）
func TestAuditCNLive(t *testing.T) {
	xdbPath := os.Getenv("SLX_TEST_XDB")
	listPath := os.Getenv("SLX_TEST_CN_LIST")
	if xdbPath == "" || listPath == "" {
		t.Skip("未设置 SLX_TEST_XDB / SLX_TEST_CN_LIST，跳过实数据对拍")
	}
	geo, err := LoadCNRanges(xdbPath)
	if err != nil {
		t.Fatalf("LoadCNRanges: %v", err)
	}
	if geo.Segments() == 0 || geo.Addresses() == 0 {
		t.Fatal("没有枚举到中国大陆网段")
	}

	// 枚举出的段必须与查询接口一致 —— 这一步同时验证 xdb 的字节序解析对不对。
	l, err := Open(xdbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	checked := 0
	for _, iv := range geo.intervals {
		if checked >= 20 {
			break
		}
		mid := iv.start + (iv.end-iv.start)/2
		ip := u32ToIP(mid)
		rg, err := l.Lookup(ip)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", ip, err)
		}
		if !rg.IsCN() {
			t.Fatalf("枚举出的 CN 段里的 %s 查出来不是境内: %+v", ip, rg)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("没有可抽样的 CN 段")
	}

	raw, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatalf("读取网段表: %v", err)
	}
	var cidrs []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cidrs = append(cidrs, line)
	}
	if len(cidrs) == 0 {
		t.Fatal("网段表是空的")
	}

	a := AuditCN(cidrs, geo, l)
	if a.ListValid == 0 {
		t.Fatal("网段表里没有一条能解析")
	}
	if a.ListValid != len(cidrs) {
		t.Fatalf("有 %d 条网段解析不了", len(cidrs)-a.ListValid)
	}
	t.Logf("网段表 %d 条 / 归属地库 %d 段：一致 %d，边界不一致 %d", a.ListEntries, geo.Segments(), a.AgreeEntries, a.PartialEntries)
	t.Logf("漏段：口径差异 %d 段(%d 地址) / 疑似真漏段 %d 段(%d 地址)",
		len(a.ScopeDiffRanges), a.ScopeDiffAddrs, len(a.MissingRanges), a.MissingAddrs)
	t.Logf("错分：语义差异 %d 段(%d 地址) / 疑似真错分 %d 段(%d 地址)",
		len(a.SemanticDiffRanges), a.SemanticDiffAddrs, len(a.FalsePositiveRanges), a.FalsePositiveAddrs)
}
