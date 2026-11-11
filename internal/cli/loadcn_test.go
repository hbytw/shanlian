package cli

import (
	"os"
	"testing"

	"slx/internal/cnlist"
)

// TestLoadCNListFallsBackToOffline 验证「在线源挂 + 无缓存」时能落到离线兜底。
//
// 这是兜底存在的唯一理由：原逻辑下这种情况会直接不启用分流，
// 等于代理整个不可用。
func TestLoadCNListFallsBackToOffline(t *testing.T) {
	xdb := os.Getenv("SLX_TEST_XDB")
	if xdb == "" {
		t.Skip("未设置 SLX_TEST_XDB，跳过兜底测试")
	}
	t.Setenv("SLX_IP2REGION_XDB", xdb)

	// 一个必然连不上的源：连接被拒，立刻失败，不拖时间。
	sources := []cnlist.Source{{
		Name:   "dead",
		URL:    "http://127.0.0.1:1/nope",
		Format: cnlist.FormatCIDR,
	}}
	res, err := loadCNList(sources, nil)
	if err != nil {
		t.Fatalf("离线兜底应该接住这次失败，实际报错: %v", err)
	}
	if !res.Offline {
		t.Fatal("结果应标记为离线兜底")
	}
	if len(res.CIDRs) == 0 {
		t.Fatal("兜底表不该是空的")
	}
	t.Logf("兜底接住：%d 条网段，来源 %s", len(res.CIDRs), res.Source)
}
