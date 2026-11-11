package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 钩子相关路径。
const (
	// openwrtFirewallUser 是 fw3 的「用户规则」入口。
	//
	// fw3 在每次 reload 时会把所有 iptables 规则清空重建，
	// 然后 source 这个文件。所以把「通知 slx 立刻自查」写在这里，
	// 防火墙一重载就能马上恢复分流规则，而不用等看门狗的 10 秒周期。
	openwrtFirewallUser = "/etc/firewall.user"

	// hookMarker 用来识别我们自己写的行，重复安装不会堆叠。
	hookMarker = "# slx-managed-hook"

	// slxBinCandidates 是按优先级尝试的二进制路径。
	// OpenWrt 上通常在 /usr/bin，其它发行版可能在 /usr/local/bin。
	slxBinFallback = "/usr/bin/slx"
)

// InstallHooks 实现 `slx install-hooks`。
//
// 作用：在防火墙重载之后，第一时间通知守护进程重建分流规则。
//
// # 为什么需要它
//
// 分流规则是 iptables 规则 + ipset + 策略路由。系统里任何一次防火墙 reload
// （重启、改配置、装了新服务）都会把它们清掉。当前靠看门狗每 10 秒巡检自愈 ——
// 能用，但用户会看到最长 10 秒的「境外网站突然全断」。
//
// 装了钩子之后，防火墙 reload 的瞬间就会通知守护进程，恢复时间降到毫秒级。
//
// # 为什么是「通知」而不是「直接重建规则」
//
// 让钩子直接调 `slx` 去写规则会引入第二个写者，与守护进程的看门狗并发修改同一批规则，
// 可能产生互相覆盖的竞态。所以钩子只发一个信号，让守护进程自己立刻自查 ——
// 单一写者，没有竞态，逻辑也集中在一处。
func InstallHooks(args []string) int {
	dryRun := false
	for _, a := range args {
		if a == "--dry-run" || a == "-n" {
			dryRun = true
		}
	}

	if _, err := os.Stat("/etc/config/firewall"); err != nil {
		fmt.Println("未检测到 OpenWrt 防火墙配置（/etc/config/firewall）。")
		fmt.Println()
		fmt.Println("本命令目前只为 OpenWrt 的 fw3 提供钩子。")
		fmt.Println("其它系统靠看门狗兜底：规则被清掉后 10 秒内自动恢复，无需额外配置。")
		fmt.Println()
		fmt.Println("如果你希望更快恢复，可以手动在防火墙重载脚本里加一行：")
		fmt.Printf("  %s notify\n", slxPath())
		return 0
	}

	bin := slxPath()
	line := fmt.Sprintf("command -v %s >/dev/null 2>&1 && %s notify >/dev/null 2>&1 %s",
		bin, bin, hookMarker)

	fmt.Printf("将向 %s 追加一行：\n  %s\n\n", openwrtFirewallUser, line)

	if dryRun {
		fmt.Println("[dry-run] 未实际写入。去掉 --dry-run 生效。")
		return 0
	}

	cur, err := os.ReadFile(openwrtFirewallUser)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", openwrtFirewallUser, err)
		return 1
	}
	text := string(cur)

	// 已经装过就更新那一行，不重复追加
	if strings.Contains(text, hookMarker) {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, hookMarker) {
				out = append(out, line)
				continue
			}
			out = append(out, l)
		}
		text = strings.Join(out, "\n")
		fmt.Println("检测到旧钩子，已更新为当前路径。")
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += line + "\n"
	}

	if err := os.WriteFile(openwrtFirewallUser, []byte(text), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", openwrtFirewallUser, err)
		fmt.Println("提示：需要 root 权限。")
		return 1
	}

	fmt.Println("已安装。")
	fmt.Println()
	fmt.Println("验证方式：")
	fmt.Println("  1. 手动清空规则： iptables -t mangle -F PREROUTING")
	fmt.Println("  2. 触发防火墙重载： /etc/init.d/firewall reload")
	fmt.Println("  3. 立即查看：       slx status    （分流应已恢复，不用等 10 秒）")
	return 0
}

// UninstallHooks 移除钩子（供 `slx uninstall` 调用）。
func removeHooks() {
	cur, err := os.ReadFile(openwrtFirewallUser)
	if err != nil {
		return
	}
	text := string(cur)
	if !strings.Contains(text, hookMarker) {
		return
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, hookMarker) {
			continue
		}
		out = append(out, l)
	}
	_ = os.WriteFile(openwrtFirewallUser, []byte(strings.Join(out, "\n")), 0o755)
}

// slxPath 猜一个可执行的 slx 路径。
func slxPath() string {
	if p, err := os.Executable(); err == nil {
		// 解析符号链接，拿到真实路径
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
			return real
		}
		return p
	}
	return slxBinFallback
}

// Notify 实现 `slx notify`：通知正在运行的守护进程立刻自查一次。
//
// 它自己不做任何规则改动 —— 只是给守护进程发个 SIGUSR1。
// 找不到 pid 文件时静默成功（守护进程没跑，本来也没什么可修的）。
func Notify(args []string) int {
	pid, err := readPIDFile()
	if err != nil || pid <= 0 {
		// 没有 pid 文件 = 守护进程没在跑。这不是错误。
		return 0
	}
	if err := signalUSR1(pid); err != nil {
		fmt.Fprintf(os.Stderr, "通知守护进程（pid %d）失败: %v\n", pid, err)
		return 1
	}
	return 0
}
