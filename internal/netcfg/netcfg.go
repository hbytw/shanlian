// Package netcfg 负责修改系统网络配置：转发开关、NAT 规则、路由。
//
// 这里是整个系统唯一「动系统状态」的地方，所以纪律很硬：
//   - 所有操作幂等（重复执行不报错、不重复插入规则）；
//   - 所有规则带唯一标记，便于精确清理，不误删用户自己的规则；
//   - 任何可能造成流量环路的配置，写入前必须先通过环路检查。
package netcfg

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Marker 是本系统写入的所有规则/路由的统一标记。
//
// 用注释标记而不是靠命名约定：iptables 的 comment 匹配能精确识别我们插入的行，
// uninstall 时按标记删除，绝不会碰用户自己的规则。
const Marker = "slx"

// Runner 抽象命令执行，便于测试。
type Runner func(name string, args ...string) (string, error)

// DefaultRunner 真实执行命令。
func DefaultRunner(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// System 封装一台机器上的网络配置操作。
type System struct {
	// IPTables 是实际使用的 iptables 命令名。
	// OpenWrt 新版本用 iptables-nft，老版本与 CentOS 7 用 iptables(legacy)。
	IPTables string
	// IP 是 ip 命令名。
	IP string
	// ProcRoot 是 /proc 路径前缀，测试时可替换。
	ProcRoot string

	run Runner
}

// NewSystem 探测可用的命令并返回操作句柄。
func NewSystem(run Runner) *System {
	if run == nil {
		run = DefaultRunner
	}
	s := &System{ProcRoot: "/proc", run: run, IP: "ip", IPTables: "iptables"}

	// 选择实际存在的 iptables 命令
	for _, name := range []string{"iptables", "iptables-nft", "iptables-legacy"} {
		if _, err := exec.LookPath(name); err == nil {
			s.IPTables = name
			break
		}
	}
	return s
}

// runCmd 执行并返回去尾的输出。
func (s *System) runCmd(name string, args ...string) (string, error) {
	out, err := s.run(name, args...)
	return strings.TrimSpace(out), err
}

// —— 转发开关 ——

// EnsureIPForward 打开 IPv4 转发并持久化。
//
// 返回是否发生了变更。持久化写 /etc/sysctl.d/，因为
// 「重启后转发又关了」是最典型的"昨天还好好的"故障。
func (s *System) EnsureIPForward() (changed bool, err error) {
	cur, err := os.ReadFile(s.ProcRoot + "/sys/net/ipv4/ip_forward")
	if err == nil && strings.TrimSpace(string(cur)) == "1" {
		return false, nil
	}

	if _, err := s.runCmd("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		// sysctl 不可用时直接写 proc
		if werr := os.WriteFile(s.ProcRoot+"/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); werr != nil {
			return false, fmt.Errorf("开启 IP 转发失败: %v（写 /proc 也失败: %v）\n"+
				"提示: 出口节点需要 root 权限才能开启转发", err, werr)
		}
	}

	// 持久化，避免重启后失效
	const confPath = "/etc/sysctl.d/99-slx.conf"
	const line = "net.ipv4.ip_forward=1"
	if b, err := os.ReadFile(confPath); err == nil && strings.Contains(string(b), line) {
		return true, nil
	}
	if err := os.MkdirAll("/etc/sysctl.d", 0o755); err == nil {
		_ = os.WriteFile(confPath, []byte("# 由 slx 写入：出口节点需要转发\n"+line+"\n"), 0o644)
	}
	return true, nil
}

// SetSysctl 设置一个内核参数（尽力而为，失败只返回错误不中断流程）。
//
// 直接写 /proc 而不是调 sysctl 命令：OpenWrt 精简镜像可能没有 sysctl 可执行文件。
func (s *System) SetSysctl(key, value string) error {
	path := s.ProcRoot + "/sys/" + strings.ReplaceAll(key, ".", "/")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("写 %s 失败: %w", path, err)
	}
	return nil
}

// GetSysctl 读取一个内核参数。
func (s *System) GetSysctl(key string) string {
	b, err := os.ReadFile(s.ProcRoot + "/sys/" + strings.ReplaceAll(key, ".", "/"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// EnableInterfaceForwarding 打开指定网卡的转发开关。
//
// 很多人只记得 `net.ipv4.ip_forward=1`（全局），但内核在转发路径上
// 还会检查 **收包网卡自己的** conf.<if>.forwarding：
//
//	if (!IN_DEV_FORWARD(in_dev)) goto drop_or_local;
//
// 新建的 TUN 接口继承的是 net.ipv4.conf.default.forwarding，
// 而不少镜像把它设成 0。于是出现极难排查的现象：
// 全局转发是开的、隧道包也进来了、抓包能看到，但内核就是不转发。
// 实测在腾讯云 CentOS 7 上就踩到了这个。
func (s *System) EnableInterfaceForwarding(iface string) error {
	key := "net.ipv4.conf." + iface + ".forwarding"
	if s.GetSysctl(key) == "1" {
		return nil
	}
	return s.SetSysctl(key, "1")
}

// AllowAsymmetricReturnPath 让隧道回程包能通过反向路径过滤。
//
// # 为什么必须动 all.rp_filter
//
// 隧道是典型的非对称路由：回程包从隧道进来，源地址是任意公网地址，
// 而这些地址的反向路由指向 WAN 而不是隧道，于是被反向路径过滤丢掉 ——
// 现象是「发得出去、完全收不到回来」，抓包却能看到包确实进了隧道。
//
// 实测参数矩阵（同一个包、同一套规则，只改 rp_filter）：
//
//	all=2, tun=2  → 丢包
//	all=2, tun=0  → 丢包      ← 说明 max(all, iface) 生效
//	all=1, tun=0  → 丢包
//	all=0, tun=0  → 通过
//
// Linux 计算生效值时取 max(conf.all.rp_filter, conf.<iface>.rp_filter)，
// 所以只要 all 非 0，把接口单独设成 0 也不会生效；宽松模式 (2) 同样不够。
// 必须把 all 归零。
//
// # 为什么这样做反而是对用户配置扰动最小的
//
// all 归零之后，每张网卡自己的值会**直接生效**：
// 原本被 max() 拉高的接口会回到自己的设定，也就是说
// WAN / LAN 上已经显式配置的严格模式会被完整保留，
// 只有从没配过的接口才会变成 0。
//
// 返回改动前的 all 值，供退出时还原。
func (s *System) AllowAsymmetricReturnPath(iface string) (prevAll string, changed bool, err error) {
	prevAll = s.GetSysctl("net.ipv4.conf.all.rp_filter")
	allChanged := false
	if prevAll != "0" {
		if e := s.SetSysctl("net.ipv4.conf.all.rp_filter", "0"); e != nil {
			return prevAll, false, fmt.Errorf("关闭全局反向路径过滤失败: %w", e)
		}
		allChanged = true
	}
	if s.GetSysctl("net.ipv4.conf."+iface+".rp_filter") != "0" {
		if e := s.SetSysctl("net.ipv4.conf."+iface+".rp_filter", "0"); e != nil {
			return prevAll, allChanged, fmt.Errorf("关闭 %s 反向路径过滤失败: %w", iface, e)
		}
		return prevAll, true, nil
	}
	return prevAll, allChanged, nil
}

// RestoreAllRPFilter 还原全局反向路径过滤设定（退出时调用）。
func (s *System) RestoreAllRPFilter(prev string) {
	if prev == "" || prev == "0" {
		return
	}
	_ = s.SetSysctl("net.ipv4.conf.all.rp_filter", prev)
}

// GetIPForward 读取当前转发状态。
func (s *System) GetIPForward() bool {
	b, err := os.ReadFile(s.ProcRoot + "/sys/net/ipv4/ip_forward")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}
