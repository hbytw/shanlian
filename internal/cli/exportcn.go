package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"slx/internal/ipgeo"
)

// ExportCNCmd 实现 `slx export-cn`，把 ip2region 库里的中国网段
// 导成 cn.cache 格式的 CIDR 列表。
//
// 用途有两个：
//
//  1. 离线兜底 —— 在线源（clang.cn / APNIC）取不到且本机没缓存时，
//     slx 会自动用 xdb 现导一份顶上（见 loadCNList）。
//  2. 烤进镜像 —— 导出的文件格式与 cn.cache 一致，可以直接当缓存用，
//     这样设备第一次启动就有境内网段，不必等联网。
//
// 默认不含港澳台：ip2region 把港澳台算作中国，而分流里港澳台通常
// **应该**走隧道，混进境内列表会让这些流量走直连。
func ExportCNCmd(args []string) int {
	var xdbPath, outPath string
	var withHKMT bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--xdb" && i+1 < len(args):
			i++
			xdbPath = args[i]
		case strings.HasPrefix(a, "--xdb="):
			xdbPath = strings.TrimPrefix(a, "--xdb=")
		case a == "--out" && i+1 < len(args):
			i++
			outPath = args[i]
		case strings.HasPrefix(a, "--out="):
			outPath = strings.TrimPrefix(a, "--out=")
		case a == "--with-hkmt":
			withHKMT = true
		case a == "-h" || a == "--help":
			fmt.Println("用法: slx export-cn [--xdb <归属地库>] [--out <文件>] [--with-hkmt]")
			fmt.Println("把 ip2region 库里的中国网段导成 cn.cache 格式的 CIDR 列表。")
			fmt.Println("默认不含港澳台 —— 那部分通常应该走隧道。")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "未知参数: %s\n", a)
			return 2
		}
	}

	path := ipgeo.ResolvePath(xdbPath)
	exp, err := ipgeo.ExportCNC(path, withHKMT)
	if err != nil {
		fmt.Fprintf(os.Stderr, "导出失败: %v\n", err)
		return 1
	}
	content := ipgeo.WriteCacheFormat(exp.CIDRs, "ip2region-xdb", time.Now().Unix())
	if outPath == "" {
		fmt.Print(content)
	} else {
		if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写文件失败: %v\n", err)
			return 1
		}
	}
	// 摘要走 stderr：stdout 只留纯数据，方便重定向。
	fmt.Fprintf(os.Stderr, "导出 %d 条网段（%s 地址），来自 %s\n",
		len(exp.CIDRs), humanCount(exp.Addresses), path)
	if !withHKMT && exp.HKMTExcluded > 0 {
		fmt.Fprintf(os.Stderr, "已排除 %d 段港澳台网段（要包含用 --with-hkmt）\n", exp.HKMTExcluded)
	}
	if outPath != "" {
		fmt.Fprintf(os.Stderr, "已写入 %s\n", outPath)
	}
	return 0
}
