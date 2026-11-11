package ipgeo

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestExportCNLive 需要真实 xdb：SLX_TEST_XDB。
func TestExportCNLive(t *testing.T) {
	path := os.Getenv("SLX_TEST_XDB")
	if path == "" {
		t.Skip("未设置 SLX_TEST_XDB，跳过导出测试")
	}

	// 默认不含港澳台
	exp, err := ExportCNC(path, false)
	if err != nil {
		t.Fatalf("ExportCNC: %v", err)
	}
	if len(exp.CIDRs) == 0 {
		t.Fatal("导出结果为空")
	}
	if exp.HKMTExcluded == 0 {
		t.Fatal("应该排除掉一些港澳台网段")
	}
	for _, c := range exp.CIDRs {
		if !strings.Contains(c, "/") {
			t.Fatalf("网段格式不对: %q", c)
		}
	}
	t.Logf("导出 %d 条（%s 地址），排除港澳台 %d 段", len(exp.CIDRs), fmt.Sprintf("%.2f 亿", float64(exp.Addresses)/1e8), exp.HKMTExcluded)

	// 带上港澳台：条目数更多，且多出来的部分必须含港澳台
	withHK, err := ExportCNC(path, true)
	if err != nil {
		t.Fatalf("ExportCNC(withHKMT): %v", err)
	}
	if len(withHK.CIDRs) <= len(exp.CIDRs) {
		t.Fatal("带港澳台时条目数应该更多")
	}
	if withHK.HKMTExcluded != 0 {
		t.Fatal("带港澳台时不该有排除")
	}

	// 导出的每一条都必须是境内（抽样验证）
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	for i := 0; i < len(exp.CIDRs) && i < 20; i++ {
		iv, err := cidrToInterval(exp.CIDRs[i])
		if err != nil {
			t.Fatalf("导出的网段解析不了 %q: %v", exp.CIDRs[i], err)
		}
		ip := u32ToIP(iv.start)
		rg, err := l.Lookup(ip)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", ip, err)
		}
		if !rg.IsCN() {
			t.Fatalf("导出的 %s 查出来不是境内: %+v", ip, rg)
		}
	}
}

func TestWriteCacheFormat(t *testing.T) {
	got := WriteCacheFormat([]string{"1.0.0.0/24", "2.0.0.0/24"}, "ip2region-xdb", 1700000000)
	want := "#slx-cn 1700000000 ip2region-xdb\n1.0.0.0/24\n2.0.0.0/24\n"
	if got != want {
		t.Fatalf("格式不对:\n得到 %q\n期望 %q", got, want)
	}
}

func humanAddr(n uint64) string {
	return fmt.Sprintf("%d", n)
}
