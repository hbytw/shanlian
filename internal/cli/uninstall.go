package cli

import (
	"fmt"
	"os"
	"strings"

	"slx/internal/config"
	"slx/internal/dispatch"
	"slx/internal/envdetect"
	"slx/internal/mesh"
	"slx/internal/netcfg"
	"slx/internal/tun"
)

// UninstallCmd 彻底清理 slx 在系统上留下的痕迹。
//
// 设计原则：只删「我们自己加的」，绝不碰用户原有的规则与配置。
// 所有 slx 写入的 iptables 规则都带 "slx:" 前缀的 comment，
// 按 comment 精确识别，不靠命名约定猜。
func UninstallCmd(args []string) int {
	defer removeHooks()
	keepConfig := false
	for _, a := range args {
		switch a {
		case "--keep-config", "-k":
			keepConfig = true
		}
	}

	fmt.Println("slx uninstall —— 清理系统上的 slx 痕迹")
	fmt.Println(strings.Repeat("=", 56))

	c, _, err := loadConfig(configPath(args))
	if err != nil {
		// 配置读不了也要继续清理：残留规则的危害大于配置错误
		fmt.Printf("  [警告] 读取配置失败（%v），按默认参数清理\n", err)
		c = &config.Config{}
	}

	innerCIDR := c.EffInnerCIDR()
	tunName := c.EffTunName()
	sys := netcfg.NewSystem(nil)

	// —— 1. 分流配置 ——
	st := &dispatch.State{Spec: dispatch.Spec{
		TunnelDev: tunName,
		InnerCIDR: innerCIDR,
	}}
	// Teardown 对不存在的规则是幂等的，不需要先判断
	st.Teardown(sys)
	fmt.Println("  [完成] 已撤销分流规则（ip rule / 路由表 / ipset / mangle）")

	// —— 1b. ZT 环路保护规则 ——
	//
	// 这条规则由 ZTGuard 独立持有（不随分流一起被拆，见 dispatch.ZTGuard），
	// 所以卸载时要单独撤 —— 否则它会一直留在路由器的规则表里。
	guard := dispatch.NewZTGuard(ztNetworks(envdetect.Detect()))
	guard.Remove(sys)
	if !guard.Empty() {
		fmt.Printf("  [完成] 已撤销 ZT 环路保护规则（%v）\n", guard.Subnets())
	}

	// —— 2. 数据面规则 ——
	if sys.CleanupMarked() > 0 {
		fmt.Println("  [完成] 已删除带 slx 标记的 iptables 规则")
	} else {
		fmt.Println("  [跳过] 没有需要删除的 iptables 规则")
	}
	sys.RemoveTunnelMark(tunName)
	sys.RemoveMSSClamp(tunName, false)
	sys.RemoveMSSClamp(tunName, true)
	sys.RemoveSNAT(netcfg.SNATSpec{OutIface: tunName, ToSource: "", Comment: "gateway-snat"})

	// —— 3. 隧道设备 ——
	if err := removeTUNDevice(tunName); err != nil {
		fmt.Printf("  [跳过] 删除隧道设备 %s: %v\n", tunName, err)
	} else {
		fmt.Printf("  [完成] 已删除隧道设备 %s\n", tunName)
	}

	// —— 4. 状态缓存 ——
	if err := mesh.ClearState(mesh.DefaultStatePath); err != nil {
		fmt.Printf("  [警告] 删除状态缓存: %v\n", err)
	} else {
		fmt.Println("  [完成] 已删除状态缓存")
	}

	// —— 5. sysctl 配置 ——
	if err := os.Remove("/etc/sysctl.d/99-slx.conf"); err == nil {
		fmt.Println("  [完成] 已删除 IP 转发持久化配置")
	} else if !os.IsNotExist(err) {
		fmt.Printf("  [警告] 删除 sysctl 配置: %v\n", err)
	}

	// —— 6. 配置文件 ——
	if keepConfig {
		fmt.Printf("  [保留] 配置文件 %s（含 network_key）\n", c.Path)
	} else {
		fmt.Printf("  [保留] 配置文件 %s 未删除\n", c.Path)
		fmt.Println("         network_key 是全网共享密钥，误删会导致所有节点失联。")
		fmt.Println("         确实要删请手动执行： rm -f " + c.Path)
	}

	fmt.Println()
	fmt.Println("清理完成。注意：")
	fmt.Println("  · 本命令不会停止正在运行的 slx 进程，请先 systemctl stop slx")
	fmt.Println("    （OpenWrt: /etc/init.d/slx stop）")
	fmt.Println("  · 也不会恢复你原来的代理工具配置 —— 如需切回请在 LuCI 里重新启用")
	return 0
}

// removeTUNDevice 删除 TUN 设备。
//
// 如果守护进程还在运行，设备会被它持有，删除会失败 —— 这是预期行为，
// 提示用户先停进程即可。
func removeTUNDevice(name string) error {
	if name == "" {
		name = tun.DefaultName
	}
	// 用 ip link del 而不是关文件描述符：设备可能由上一次运行的进程创建
	if out, err := netcfg.DefaultRunner("ip", "link", "del", name); err != nil {
		if strings.Contains(out, "Cannot find device") ||
			strings.Contains(out, "does not exist") {
			return fmt.Errorf("设备不存在")
		}
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}
