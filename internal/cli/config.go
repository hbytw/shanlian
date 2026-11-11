package cli

import (
	"fmt"
	"os"
	"strings"
)

// Usage 打印命令总览。
func Usage() {
	fmt.Print(`slx —— 闪连智能出口网关

用法:
  slx <命令> [参数]

核心命令:
  doctor              一键环境体检（每项给通过/失败 + 修复建议）
  run                 启动守护进程（网关：接管流量 + 等 agent 接入）
  agent --gateway     启动出口 agent（反向连接网关；不需要 root / TUN / 公网 IP）
  config              查看/生成配置

诊断命令（随里程碑逐步可用）:
  status              一屏概览：节点、隧道、出口候选、当前路由
  peers               拓扑视图：节点、ZT 地址、状态、RTT、负载
  explain <目标>       解释某个域名/IP 为什么走这个出口
  probe [目标]         手动对全部出口跑一轮测速
  trace <IP>          逐出口试连，看谁通、谁快
  ip <IP>             查 IP 归属地（国家/省/市/运营商，ip2region 离线库）
  audit-cn            国内网段表 ↔ ip2region 归属地库 对拍，查漏段/错分
  export-cn           把 ip2region 的中国网段导成 cn.cache 格式（离线兜底用）
  routes              路由表 + 命中统计
  logs                查看日志

维护命令:
  install-hooks       写入分流规则（开机自恢复）
  uninstall           彻底清理规则、ipset 与状态
  version             显示版本

配置全部在 /etc/slx/config.yaml，通常只需要 network_key 一项。
其余（网卡、端口、ZT 网段、防火墙变体、DNS 上游）均自动推断。
`)
}

// NotYet 统一处理尚未实现的命令，让「未实现」这件事本身也是清晰的。
func NotYet(cmd, milestone string) int {
	fmt.Printf("命令 %q 将在里程碑 %s 实现，当前版本尚未提供。\n", cmd, milestone)
	fmt.Printf("进度参考: docs/ 或 DESIGN.md 的里程碑章节\n")
	return 3
}

// ConfigCmd 处理 config 子命令。
func ConfigCmd(args []string) int {
	path := configPath(args)

	showKey := false
	rest := args
	_ = rest
	for _, a := range args {
		switch a {
		case "--key", "-k":
			showKey = true
		}
	}

	c, res, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}

	if res.Created {
		fmt.Printf("已创建配置文件: %s\n\n", c.Path)
	}
	if res.GeneratedKey != "" {
		fmt.Println("已生成网络密钥（请复制到其它所有节点）:")
		fmt.Println()
		fmt.Println("    network_key: " + res.GeneratedKey)
		fmt.Println()
		fmt.Println("提示: 也可以运行 slx config --key 再次查看。")
		fmt.Println()
	}
	if res.MigratedFrom != 0 {
		fmt.Printf("配置已从版本 %d 迁移到 %d\n\n", res.MigratedFrom, c.SchemaVersion)
	}

	if showKey {
		fmt.Println(c.NetworkKey)
		return 0
	}

	fmt.Printf("配置文件: %s\n", c.Path)
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("schema_version : %d\n", c.SchemaVersion)
	fmt.Printf("network_key    : %s\n", maskKey(c.NetworkKey))
	if len(c.ProbeTargets) == 0 {
		fmt.Printf("probe_targets  : (空 —— 将从实际访问的境外域名自动推导)\n")
	} else {
		fmt.Printf("probe_targets  : %s\n", strings.Join(c.ProbeTargets, ", "))
	}
	fmt.Println()
	fmt.Println("可选覆盖项（留空即自动推断）:")
	fmt.Printf("  log_level=%q log_format=%q listen_addr=%q\n",
		c.EffLogLevel(), c.EffLogFormat(), c.EffListenAddr())
	fmt.Printf("  listen_port=%d ctrl_port=%d probe_bytes=%d\n",
		c.ListenPort, c.CtrlPort, c.EffProbeBytes())
	return 0
}

// maskKey 隐去打码密钥，避免日志/截图泄露。
func maskKey(k string) string {
	if len(k) <= 12 {
		return "****"
	}
	return k[:6] + "…" + k[len(k)-4:] + "（完整值用 slx config --key 查看）"
}

func configPath(args []string) string {
	for i, a := range args {
		if (a == "--config" || a == "-c") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--config=") {
			return strings.TrimPrefix(a, "--config=")
		}
	}
	if v := os.Getenv("SLX_CONFIG"); v != "" {
		return v
	}
	return ""
}
