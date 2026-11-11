package ipgeo

import (
	"os"
	"testing"
)

func TestParseRegion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Region
	}{
		{
			name: "境内五段",
			in:   "中国|广东省|深圳市|电信|CN",
			want: Region{Country: "中国", Province: "广东省", City: "深圳市", ISP: "电信", ISO: "CN"},
		},
		{
			name: "境外五段且运营商未知",
			in:   "Australia|Queensland|Brisbane|0|AU",
			want: Region{Country: "Australia", Province: "Queensland", City: "Brisbane", ISP: "", ISO: "AU"},
		},
		{
			name: "未知占位 0 一律清空",
			in:   "United States|California|0|Google LLC|US",
			want: Region{Country: "United States", Province: "California", City: "", ISP: "Google LLC", ISO: "US"},
		},
		{
			name: "历史四段格式",
			in:   "中国|广东省|深圳市|电信",
			want: Region{Country: "中国", Province: "广东省", City: "深圳市", ISP: "电信"},
		},
		{
			name: "空结果",
			in:   "",
			want: Region{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseRegion(c.in)
			if got != c.want {
				t.Fatalf("parseRegion(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestRegionIsCN(t *testing.T) {
	if !(Region{Country: "中国", ISO: "CN"}).IsCN() {
		t.Fatal("中国 应判定为境内")
	}
	if !(Region{Country: "中国"}).IsCN() {
		t.Fatal("只有国家名没有 ISO 也应判定为境内")
	}
	if (Region{Country: "Japan", ISO: "JP"}).IsCN() {
		t.Fatal("境外 IP 不应判定为境内")
	}
}

func TestRegionEmpty(t *testing.T) {
	if !(Region{}).Empty() {
		t.Fatal("零值应为空结果")
	}
	if (Region{Country: "中国"}).Empty() {
		t.Fatal("有国家不应为空结果")
	}
}

// TestLookupLive 需要真实的 xdb 文件。
//
// 用环境变量指定：SLX_TEST_XDB=/path/to/ip2region_v4.xdb
// 没有这个文件时跳过 —— xdb 有 11MB，不适合进仓库。
func TestLookupLive(t *testing.T) {
	path := os.Getenv("SLX_TEST_XDB")
	if path == "" {
		t.Skip("未设置 SLX_TEST_XDB，跳过实库查询测试")
	}
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer l.Close()

	r, err := l.Lookup("113.92.157.29")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !r.IsCN() {
		t.Fatalf("113.92.157.29 应判定为境内，实际 %+v", r)
	}
	if r.Province == "" {
		t.Fatalf("境内 IP 应有省份，实际 %+v", r)
	}

	r2, err := l.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup(8.8.8.8): %v", err)
	}
	if r2.IsCN() {
		t.Fatalf("8.8.8.8 不应判定为境内，实际 %+v", r2)
	}
}

func TestResolvePath(t *testing.T) {
	if got := ResolvePath("/tmp/a.xdb"); got != "/tmp/a.xdb" {
		t.Fatalf("显式路径应优先，实际 %q", got)
	}
	t.Setenv(PathEnv, "/tmp/env.xdb")
	if got := ResolvePath(""); got != "/tmp/env.xdb" {
		t.Fatalf("环境变量应生效，实际 %q", got)
	}
	t.Setenv(PathEnv, "")
	if got := ResolvePath(""); got != DefaultXDBPath {
		t.Fatalf("默认路径应为 %q，实际 %q", DefaultXDBPath, got)
	}
}
