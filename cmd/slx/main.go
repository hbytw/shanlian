// Command slx 是闪连（SLX）智能出口网关的唯一入口。
//
// 一个二进制，按能力组合工作：网关（建 TUN、接管流量）/ 出口（收包、SNAT、上网）。
// 没有 Web 控制台，CLI 是全部交互面。
package main

import (
	"fmt"
	"os"

	"slx/internal/buildinfo"
	"slx/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		cli.Usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var code int
	switch cmd {
	case "version", "-v", "--version":
		fmt.Printf("slx %s\n", buildinfo.String())
		fmt.Printf("协议版本 %d..%d\n", buildinfo.ProtoMin, buildinfo.ProtoMax)

	case "doctor":
		code = cli.Doctor()

	case "config":
		code = cli.ConfigCmd(args)

	case "run":
		code = cli.Run(args)

	case "agent":
		code = cli.Agent(args)

	case "status":
		code = cli.StatusCmd(args)

	case "peers":
		code = cli.PeersCmd(args)

	case "probe":
		code = cli.Probe(args)

	case "explain":
		code = cli.Explain(args)

	case "trace":
		code = cli.Trace(args)

	case "ip":
		code = cli.IPCmd(args)

	case "audit-cn":
		code = cli.AuditCNCmd(args)

	case "export-cn":
		code = cli.ExportCNCmd(args)

	case "routes":
		code = cli.Routes()

	case "logs":
		code = cli.Logs(args)

	case "install-hooks":
		code = cli.InstallHooks(args)

	case "notify":
		code = cli.Notify(args)

	case "uninstall":
		code = cli.UninstallCmd(args)

	case "help", "-h", "--help":
		cli.Usage()

	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", cmd)
		cli.Usage()
		code = 2
	}

	os.Exit(code)
}
