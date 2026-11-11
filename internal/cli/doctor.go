// Package cli 实现 slx 的命令行界面。
//
// 设计约束（DESIGN.md §8）：
//   - 没有 Web 控制台，CLI 是唯一交互面，所以必须好用；
//   - 诊断命令要能直接回答「为什么不通/为什么慢」，而不是丢一堆日志；
//   - 每个失败项都要给可执行的修复建议（hint）。
package cli

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"slx/internal/envdetect"
	"slx/internal/ipgeo"
)

// Status 是检查项的结论。
type Status int

const (
	StatusPass Status = iota
	StatusWarn
	StatusFail
	StatusSkip
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "通过"
	case StatusWarn:
		return "警告"
	case StatusFail:
		return "失败"
	case StatusSkip:
		return "跳过"
	default:
		return "?"
	}
}

// Color 返回带颜色的状态标签（仅在终端输出时使用）。
func (s Status) Color() string {
	switch s {
	case StatusPass:
		return "\x1b[32m通过\x1b[0m"
	case StatusWarn:
		return "\x1b[33m警告\x1b[0m"
	case StatusFail:
		return "\x1b[31m失败\x1b[0m"
	case StatusSkip:
		return "\x1b[90m跳过\x1b[0m"
	}
	return "?"
}

// Check 是一个体检项。
type Check struct {
	Name string
	// Layer 标明该检查项属于哪一层，便于把故障归类。
	Layer  string
	Status Status
	Detail string
	// Hint 是失败时的可执行修复建议。
	Hint string
}

// Doctor 执行全部静态环境检查并输出结果。
func Doctor() int {
	env := envdetect.Detect()
	checks := buildChecks(env)
	renderChecks(checks)

	pass, warn, fail := 0, 0, 0
	// ── DNS 自检 ──
	// 真机踩到过两次：5353 被 avahi 占用导致转发器起不来；
	// resolv.conf 指回 dnsmasq 导致死循环。这两项都很隐蔽，
	// 放进体检里提前暴露。
	checks = append(checks, checkDNS()...)

	for _, c := range checks {
		switch c.Status {
		case StatusPass:
			pass++
		case StatusWarn:
			warn++
		case StatusFail:
			fail++
		}
	}
	fmt.Printf("\n共 %d 项：通过 %d，警告 %d，失败 %d\n", len(checks), pass, warn, fail)
	if fail > 0 {
		fmt.Println("\n按上面的「修复」建议处理后重新运行 slx doctor。")
		return 1
	}
	return 0
}

func buildChecks(env *envdetect.Env) []Check {
	var cs []Check
	add := func(c Check) { cs = append(cs, c) }

	// 1. 运行身份与特权
	if os.Geteuid() == 0 {
		add(Check{Name: "运行身份", Layer: "转发", Status: StatusPass, Detail: "root"})
	} else {
		add(Check{
			Name:   "运行身份",
			Layer:  "转发",
			Status: StatusWarn,
			Detail: fmt.Sprintf("uid=%d（非 root）", os.Geteuid()),
			Hint:   "网关角色需要 root 或 CAP_NET_ADMIN；纯出口角色可以非 root 运行",
		})
	}

	// 2. 平台与架构
	archNote := env.GOARCH
	if !env.Is64Bit {
		archNote += " (32 位)"
	}
	add(Check{
		Name: "平台架构", Layer: "转发", Status: StatusPass,
		Detail: fmt.Sprintf("%s/%s，内核 %s，发行版 %s", env.GOOS, archNote, env.Kernel, env.Distro),
	})

	// 3. TUN 设备
	if env.HasTUN {
		add(Check{Name: "TUN 设备", Layer: "转发", Status: StatusPass, Detail: "/dev/net/tun 可打开"})
	} else {
		add(Check{Name: "TUN 设备", Layer: "转发", Status: StatusFail, Detail: env.TUNProblem, Hint: env.TUNProblem})
	}

	// 4. 防火墙实现识别
	switch env.Firewall {
	case envdetect.FWIPTablesLegacy, envdetect.FWIPTablesNft:
		add(Check{Name: "防火墙实现", Layer: "分流", Status: StatusPass, Detail: string(env.Firewall)})
	case envdetect.FWNone:
		add(Check{
			Name: "防火墙实现", Layer: "分流", Status: StatusFail,
			Detail: "未找到 iptables 或 nft",
			Hint:   "安装 iptables（OpenWrt: opkg install iptables；CentOS: yum install iptables）",
		})
	default:
		add(Check{
			Name: "防火墙实现", Layer: "分流", Status: StatusWarn,
			Detail: "iptables 存在但无法判断变体（legacy / nft）",
			Hint:   "运行 iptables --version 确认；两者 SLX 都支持，但需要知道用哪套",
		})
	}

	// 5. ipset
	if env.HasIPSet {
		add(Check{Name: "ipset", Layer: "分流", Status: StatusPass, Detail: "已安装"})
	} else {
		hint := "安装 ipset 用于国内 IP 段分流"
		if env.IsOpenWrt {
			hint = "opkg update && opkg install ipset"
		}
		add(Check{Name: "ipset", Layer: "分流", Status: StatusFail, Detail: "未安装", Hint: hint})
	}

	// 6. ZeroTier 接口
	if len(env.ZTIfaces) == 0 {
		add(Check{
			Name: "ZeroTier 接口", Layer: "组网", Status: StatusFail,
			Detail: "未发现 zt* 接口",
			Hint:   "确认 zerotier-one 已运行并已加入网络（zerotier-cli listnetworks 应为 OK）",
		})
	} else {
		var parts []string
		for _, z := range env.ZTIfaces {
			parts = append(parts, fmt.Sprintf("%s(%s mtu=%d)", z.Name, z.CIDR, z.MTU))
		}
		add(Check{Name: "ZeroTier 接口", Layer: "组网", Status: StatusPass, Detail: strings.Join(parts, "; ")})
	}

	// 7. 默认路由
	if env.DefaultIface != "" {
		add(Check{Name: "默认路由", Layer: "分流", Status: StatusPass, Detail: "出口接口 " + env.DefaultIface})
	} else {
		add(Check{
			Name: "默认路由", Layer: "分流", Status: StatusFail,
			Detail: "未能解析默认路由",
			Hint:   "检查 /proc/net/route 或 ip route show default",
		})
	}

	// 8. LAN 接口
	if env.LANIface != "" {
		add(Check{Name: "LAN 接口", Layer: "分流", Status: StatusPass, Detail: env.LANIface + " " + env.LANIP})
	} else {
		add(Check{
			Name: "LAN 接口", Layer: "分流", Status: StatusWarn,
			Detail: "未识别到内网接口",
			Hint:   "纯出口节点不需要；网关角色需要至少一个带私有地址的接口",
		})
	}

	// 9. 内存
	if env.MemTotalKB > 0 {
		totalMB := env.MemTotalKB / 1024
		availMB := env.MemAvailKB / 1024
		st, hint := StatusPass, ""
		if totalMB < 64 {
			st, hint = StatusWarn, "内存偏小，建议使用精简版构建并关闭吞吐探测"
		}
		add(Check{
			Name: "内存", Layer: "转发", Status: st,
			Detail: fmt.Sprintf("总量 %d MB，可用 %d MB", totalMB, availMB), Hint: hint,
		})
	}

	// 10. SELinux
	switch env.SELinux {
	case "enforcing":
		add(Check{
			Name: "SELinux", Layer: "转发", Status: StatusWarn,
			Detail: "enforcing",
			Hint:   "可能拦截建 TUN/改路由。临时放宽：setenforce 0；持久化需按发行版配置策略或打标签",
		})
	case "permissive":
		add(Check{Name: "SELinux", Layer: "转发", Status: StatusPass, Detail: "permissive"})
	default:
		add(Check{Name: "SELinux", Layer: "转发", Status: StatusSkip, Detail: env.SELinux})
	}

	// 11. 状态目录可写
	tmpOK := dirWritable("/tmp")
	if tmpOK {
		add(Check{Name: "临时目录", Layer: "转发", Status: StatusPass, Detail: "/tmp 可写（状态缓存不落闪存）"})
	} else {
		add(Check{Name: "临时目录", Layer: "转发", Status: StatusFail, Detail: "/tmp 不可写", Hint: "检查挂载与权限"})
	}

	// 12. 时钟（粗略）
	add(clockCheck())

	// 13-15. 分流相关（M3）
	for _, c := range dispatchChecks(env) {
		add(c)
	}

	// 16. IP 归属地库（ip2region xdb）
	//
	// 可选项：没有它只是少一个查归属地的能力，不影响启动与分流，
	// 所以报「警告」而不是「失败」——体检里的失败项是要人去修的，
	// 不该把可选项报成故障。
	{
		path := ipgeo.ResolvePath("")
		if err := ipgeo.VerifyFile(path); err != nil {
			add(Check{
				Name: "IP 归属地库", Layer: "分流", Status: StatusWarn,
				Detail: err.Error(),
				Hint: "可选功能：下载 ip2region_v4.xdb 放到 " + path +
					"（https://github.com/lionsoul2014/ip2region 的 data/ip2region_v4.xdb），" +
					"之后可用 slx ip <ip> 查归属地",
			})
		} else {
			add(Check{Name: "IP 归属地库", Layer: "分流", Status: StatusPass, Detail: path})
		}
	}

	return cs
}

func renderChecks(cs []Check) {
	color := isTerminal()
	fmt.Println("slx doctor —— 环境体检")
	fmt.Println(strings.Repeat("=", 64))
	byLayer := map[string][]Check{}
	var order []string
	for _, c := range cs {
		if _, ok := byLayer[c.Layer]; !ok {
			order = append(order, c.Layer)
		}
		byLayer[c.Layer] = append(byLayer[c.Layer], c)
	}
	sort.Strings(order)

	for _, layer := range order {
		fmt.Printf("\n【%s】\n", layer)
		for _, c := range byLayer[layer] {
			tag := c.Status.String()
			if color {
				tag = c.Status.Color()
			}
			fmt.Printf("  [%s] %-14s %s\n", tag, c.Name, c.Detail)
			if c.Hint != "" && c.Status != StatusPass {
				fmt.Printf("         └─ 修复: %s\n", c.Hint)
			}
		}
	}
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".slx-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// checkDNS 检查 DNS 转发器的运行前提。
func checkDNS() []Check {
	var out []Check

	// 1. resolv.conf 上游是否全是回环（会导致 DNS 死循环）
	upstreams := resolvUpstreams()
	allLoopback := true
	for _, u := range upstreams {
		if ip := net.ParseIP(u); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			allLoopback = false
			break
		}
	}
	if len(upstreams) == 0 || allLoopback {
		out = append(out, Check{
			Name:   "DNS 上游",
			Layer:  "DNS",
			Status: StatusPass,
			Detail: "系统配置指向本机（回环），已自动改用公共 DNS 兜底",
			Hint:   "这是 OpenWrt 的常态（resolv.conf 指 dnsmasq）；本版本已规避死循环",
		})
	} else {
		out = append(out, Check{
			Name:   "DNS 上游",
			Layer:  "DNS",
			Status: StatusPass,
			Detail: strings.Join(upstreams, ", "),
		})
	}

	// 2. 5353 是否被占（avahi / mDNS）——这是最常见的启动失败原因
	if c, err := net.ListenPacket("udp", "127.0.0.1:5353"); err != nil {
		out = append(out, Check{
			Name:   "DNS 端口 5353",
			Layer:  "DNS",
			Status: StatusWarn,
			Detail: "已被占用（通常是 avahi-daemon 的 mDNS）",
			Hint:   "dns_listen 请用 127.0.0.1:5335，不要用 5353",
		})
	} else {
		c.Close()
		out = append(out, Check{
			Name:   "DNS 端口 5353",
			Layer:  "DNS",
			Status: StatusPass,
			Detail: "空闲（但建议 dns_listen 仍用 5335）",
		})
	}

	return out
}

// resolUpstreams 读 /etc/resolv.conf 的 nameserver 列表。
func resolvUpstreams() []string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) >= 2 {
			out = append(out, fs[1])
		}
	}
	return out
}
