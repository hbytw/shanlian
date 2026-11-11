package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"slx/internal/ipgeo"
)

// IPCmd 实现 `slx ip <ip>...`，用 ip2region 离线库查 IP 归属地。
//
// 与分流的关系（DESIGN.md §7.1）：
//
//	分流真正判定用的是 ipset 里的 CIDR 段表，只回答「在不在国内段」。
//	本命令回答的是「在哪」：国家/省/市/运营商。
//	排查「为什么这个域名走了隧道」时，这两条信息是缺一不可的 ——
//	尤其是段表过期漏段的那种情况。
//
//	xdb 文件是可选的：没有它只是这个命令不可用，不影响启动与分流。
func IPCmd(args []string) int {
	var xdbPath string
	var ips []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--xdb" && i+1 < len(args):
			i++
			xdbPath = args[i]
		case strings.HasPrefix(a, "--xdb="):
			xdbPath = strings.TrimPrefix(a, "--xdb=")
		case a == "-h" || a == "--help":
			fmt.Println("用法: slx ip [--xdb <路径>] <ip> [ip...]")
			fmt.Println("查 IP 归属地（国家/省/市/运营商），使用 ip2region 离线库。")
			return 0
		default:
			ips = append(ips, a)
		}
	}
	if len(ips) == 0 {
		fmt.Fprintln(os.Stderr, "用法: slx ip [--xdb <路径>] <ip> [ip...]")
		return 2
	}

	path := ipgeo.ResolvePath(xdbPath)
	l, err := ipgeo.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开归属地库失败: %v\n", err)
		if errors.Is(err, ipgeo.ErrNoDatabase) {
			fmt.Fprintf(os.Stderr,
				"提示: 归属地库是可选功能。下载 ip2region_v4.xdb 放到 %s 即可启用。\n"+
					"下载地址: https://github.com/lionsoul2014/ip2region （data/ip2region_v4.xdb）\n",
				path)
		}
		return 1
	}
	defer l.Close()

	for _, ip := range ips {
		r, err := l.Lookup(ip)
		if err != nil {
			fmt.Printf("%-40s  查询失败: %v\n", ip, err)
			continue
		}
		if r.Empty() {
			fmt.Printf("%-40s  不在库中\n", ip)
			continue
		}
		tag := "境外"
		if r.IsCN() {
			tag = "境内"
		}
		fmt.Printf("%-40s  %-4s  %s\n", ip, tag, r.String())
	}
	return 0
}
