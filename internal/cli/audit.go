package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"slx/internal/cnlist"
	"slx/internal/ipgeo"
)

// AuditCNCmd 实现 `slx audit-cn`，把当前使用的国内网段表
// 和 ip2region 归属地库对拍。
//
// 为什么要有这个命令（DESIGN.md §7.1、§7.3）：
//
//	分流完全依赖「国内 IP 段表」这一份数据，而这份数据会过期、会漏段。
//	一旦漏段，境内流量会被当成境外送进隧道 —— 用户看到的只是「今天变慢了」，
//	不会有任何报错。错分更糟：境外流量走直连，可能被墙。
//
//	ip2region 的 xdb 是另一套独立来源，两者对拍就能把「静默的错」暴露出来。
//	注意它只是体检工具：结论是「两边不一致」，哪边对要人来判断
//	（xdb 的免费数据更新不规律，cnlist 的源也可能滞后）。
func AuditCNCmd(args []string) int {
	var xdbPath, listPath string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--xdb" && i+1 < len(args):
			i++
			xdbPath = args[i]
		case strings.HasPrefix(a, "--xdb="):
			xdbPath = strings.TrimPrefix(a, "--xdb=")
		case a == "--list" && i+1 < len(args):
			i++
			listPath = args[i]
		case strings.HasPrefix(a, "--list="):
			listPath = strings.TrimPrefix(a, "--list=")
		case a == "-h" || a == "--help":
			fmt.Println("用法: slx audit-cn [--xdb <归属地库>] [--list <国内网段缓存>]")
			fmt.Println("把当前在用的国内 IP 段表和 ip2region 归属地库对拍，报出分歧。")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "未知参数: %s\n", a)
			return 2
		}
	}
	if listPath == "" {
		listPath = cnlist.DefaultCachePath
	}

	// —— 归属地库 ——
	xdb := ipgeo.ResolvePath(xdbPath)
	geo, err := ipgeo.LoadCNRanges(xdb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取归属地库失败: %v\n", err)
		if errors.Is(err, ipgeo.ErrNoDatabase) {
			fmt.Fprintf(os.Stderr, "提示: 归属地库是可选功能，放一份 ip2region_v4.xdb 到 %s 即可。\n", xdb)
		}
		return 1
	}

	// —— 国内网段表 ——
	//
	// 优先用设备上正在用的那份缓存：审计要对着「实际在用的东西」查，
	// 而不是现拉一份新的 —— 那会掩盖正在发生的问题。
	res, err := cnlist.LoadCached(listPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取国内网段缓存失败: %v\n", err)
		fmt.Fprintf(os.Stderr, "提示: 先跑一次 slx 让它生成本地缓存，或用 --list 指定文件\n")
		return 1
	}

	// —— 对拍 ——
	//
	// lookup 要一起开着：它用来把「语义差异」（运营商的国际出口段）
	// 从「疑似错分」里分出来 —— 没有它，报告会把这类段算成错分。
	lookup, err := ipgeo.Open(xdb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开归属地查询器失败: %v\n", err)
		return 1
	}
	defer lookup.Close()
	a := ipgeo.AuditCN(res.CIDRs, geo, lookup)

	fmt.Println("国内网段交叉校验：cnlist ↔ ip2region")
	fmt.Println(strings.Repeat("=", 68))
	fmt.Printf("  国内网段表   %d 条（来源 %s，%s前更新），覆盖 %s 地址\n",
		len(res.CIDRs), res.Source, humanAge(res.CacheAge), humanCount(a.ListAddresses))
	fmt.Printf("  归属地库     %d 段 / 合并 %d 段，覆盖 %s 地址\n",
		geo.Segments(), a.GeoRanges, humanCount(a.GeoAddresses))
	fmt.Println(strings.Repeat("-", 68))
	fmt.Printf("  一致         %d 条\n", a.AgreeEntries)
	if a.PartialEntries > 0 {
		fmt.Printf("  边界不一致   %d 条（两边都覆盖但切分不同，不算错）\n", a.PartialEntries)
	}

	fmt.Println("\n  漏段（cnlist 少收：ip2region 说境内，表里没有）")
	fmt.Printf("    ├─ 口径差异   %5d 段 / %8s 地址  ← 港澳台算不算境内，两边定义不同\n",
		len(a.ScopeDiffRanges), humanCount(a.ScopeDiffAddrs))
	fmt.Printf("    └─ 疑似真漏段 %5d 段 / %8s 地址  ← 境内流量会被白送进隧道\n",
		len(a.MissingRanges), humanCount(a.MissingAddrs))

	fmt.Println("\n  错分（cnlist 多收：表里算境内，归属地库不认）")
	fmt.Printf("    ├─ 语义差异   %5d 段 / %8s 地址  ← 运营商国际出口段，按位置算在境外\n",
		len(a.SemanticDiffRanges), humanCount(a.SemanticDiffAddrs))
	fmt.Printf("    └─ 疑似真错分 %5d 段 / %8s 地址  ← 境外流量会走直连，可能被墙\n",
		len(a.FalsePositiveRanges), humanCount(a.FalsePositiveAddrs))

	if len(a.MissingRanges) > 0 {
		fmt.Println("\n  疑似真漏段样本：")
		for _, s := range limitList(a.MissingRanges, 10) {
			fmt.Println("    - " + s)
		}
	}
	if len(a.FalsePositiveSamples) > 0 {
		fmt.Println("\n  疑似真错分样本（cnlist 收了，归属地库不认）：")
		for _, s := range limitList(a.FalsePositiveSamples, 10) {
			fmt.Println("    - " + s)
		}
	}
	if len(a.InvalidEntries) > 0 {
		fmt.Printf("\n  有 %d 条网段解析不了（不是合法 CIDR）\n", len(a.InvalidEntries))
	}
	if a.Clean() {
		fmt.Println("\n结论：两边完全一致。")
		return 0
	}
	fmt.Println("\n结论：两边有分歧。真漏段会让境内流量白走隧道，真错分会让境外流量走直连；")
	fmt.Println("      两份数据都可能有滞后，改哪边要结合实际情况判断。")
	return 1
}

// limitList 截断列表并标注剩余数量。
func limitList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	out := append([]string{}, items[:n]...)
	out = append(out, fmt.Sprintf("…… 还有 %d 条", len(items)-n))
	return out
}
