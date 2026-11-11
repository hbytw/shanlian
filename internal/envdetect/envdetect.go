// Package envdetect 探测运行环境。
//
// 这是「不硬编码」原则的执行者：网卡名、防火墙变体、端口、ZT 网段、内存容量
// 全部在运行时推断，不写死在代码里，也不要求用户配置。
//
// 所有探测都只读，不会修改系统状态。真正的修改在各功能模块里，
// 且必须建立在探测结果之上。
package envdetect

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// FirewallKind 是防火墙实现变体。
type FirewallKind string

const (
	FWUnknown        FirewallKind = "unknown"
	FWIPTablesLegacy FirewallKind = "iptables-legacy"
	FWIPTablesNft    FirewallKind = "iptables-nft"
	FWNone           FirewallKind = "none"
)

// ZTIface 描述一个 ZeroTier 虚拟接口。
type ZTIface struct {
	Name string
	CIDR string // 形如 192.168.99.1/24
	IP   string
	Bits int
	MTU  int
}

// Env 是探测到的运行环境快照。
type Env struct {
	GOOS    string
	GOARCH  string
	Is64Bit bool

	Distro    string // openwrt / centos / ubuntu / debian / other
	IsOpenWrt bool
	Kernel    string
	Hostname  string

	Firewall    FirewallKind
	HasIPSet    bool
	HasNFT      bool
	HasIPTables bool
	HasIPCmd    bool
	HasTUN      bool
	TUNProblem  string // HasTUN 为 false 时的可执行修复建议

	DefaultIface string
	LANIface     string
	LANIP        string

	ZTIfaces []ZTIface

	MemTotalKB int64
	MemAvailKB int64

	SELinux string // enabled / disabled / permissive / unknown
}

// Detect 采集环境信息。永不返回错误——任何单项探测失败都只影响该字段，
// 由 doctor 命令把缺失项报告给用户。
func Detect() *Env {
	e := &Env{
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Is64Bit: strconv.IntSize == 64,
		Kernel:  readTrim("/proc/sys/kernel/osrelease"),
	}
	e.Hostname, _ = os.Hostname()

	detectDistro(e)
	detectTUN(e)
	detectFirewall(e)
	detectMem(e)
	detectSELinux(e)
	detectInterfaces(e)

	return e
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// detectDistro 识别发行版。OpenWrt 的 /etc/os-release 与常规发行版不同。
func detectDistro(e *Env) {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		e.IsOpenWrt = true
		e.Distro = "openwrt"
		return
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		e.Distro = "other"
		return
	}
	s := strings.ToLower(string(data))
	switch {
	case strings.Contains(s, "centos"):
		e.Distro = "centos"
	case strings.Contains(s, "ubuntu"):
		e.Distro = "ubuntu"
	case strings.Contains(s, "debian"):
		e.Distro = "debian"
	case strings.Contains(s, "openwrt"):
		e.IsOpenWrt = true
		e.Distro = "openwrt"
	default:
		e.Distro = "other"
	}
}

// detectTUN 检查能否创建 TUN 设备。
//
// 这是网关角色的硬前提。OpenWrt 精简镜像常缺 kmod-tun，
// 所以要给出精确到安装命令的提示，而不是只说"失败了"。
func detectTUN(e *Env) {
	if runtime.GOOS != "linux" {
		e.HasTUN = false
		e.TUNProblem = "当前平台不是 Linux，SLX 的网关角色只在 Linux 上可用"
		return
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		e.HasTUN = false
		if e.IsOpenWrt {
			e.TUNProblem = "缺少 /dev/net/tun —— 执行 opkg update && opkg install kmod-tun"
		} else if e.Distro == "centos" {
			e.TUNProblem = "缺少 /dev/net/tun —— 执行 modprobe tun，并确认 /etc/modules-load.d/ 里已登记"
		} else {
			e.TUNProblem = "缺少 /dev/net/tun —— 执行 modprobe tun"
		}
		return
	}
	// 尝试打开，验证权限（普通用户可能无权限，但出口角色不需要 TUN）
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		e.HasTUN = false
		if os.IsPermission(err) {
			e.TUNProblem = "无权打开 /dev/net/tun —— 需要 root 或 CAP_NET_ADMIN"
		} else {
			e.TUNProblem = "打开 /dev/net/tun 失败: " + err.Error()
		}
		return
	}
	_ = f.Close()
	e.HasTUN = true
}

// detectFirewall 判断实际可用的防火墙实现。
//
// 同一个二进制要同时支持 iptables-legacy、iptables-nft 与纯 nftables，
// 否则在新旧 OpenWrt、CentOS 7、Ubuntu 之间必然有一端跑不起来。
func detectFirewall(e *Env) {
	e.HasIPCmd = hasCmd("ip")

	if path := lookPath("iptables"); path != "" {
		e.HasIPTables = true
		if out, err := exec.Command(path, "--version").Output(); err == nil {
			v := strings.ToLower(string(out))
			switch {
			case strings.Contains(v, "nf_tables"):
				e.Firewall = FWIPTablesNft
			case strings.Contains(v, "legacy"):
				e.Firewall = FWIPTablesLegacy
			default:
				e.Firewall = FWIPTablesLegacy
			}
		} else {
			e.Firewall = FWUnknown
		}
	}

	if lookPath("nft") != "" {
		e.HasNFT = true
	}
	if lookPath("ipset") != "" {
		e.HasIPSet = true
	}

	if !e.HasIPTables && !e.HasNFT {
		e.Firewall = FWNone
	}
}

// detectMem 读取内存总量与可用量，用于自动推算 NAT 表容量等。
func detectMem(e *Env) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			e.MemTotalKB = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			e.MemAvailKB = parseKB(line)
		}
		if e.MemTotalKB > 0 && e.MemAvailKB > 0 {
			return
		}
	}
}

func parseKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(fields[1], 10, 64)
	return n
}

// detectSELinux 判断 SELinux 状态。CentOS 7 默认 enforcing，会拦截改网络配置。
func detectSELinux(e *Env) {
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err != nil {
		e.SELinux = "disabled"
		return
	}
	v := readTrim("/sys/fs/selinux/enforce")
	if v == "1" {
		e.SELinux = "enforcing"
	} else {
		e.SELinux = "permissive"
	}
}

// detectInterfaces 识别默认路由接口、LAN 接口与 ZeroTier 接口。
//
// 仅在 Linux 上做完整推断：非 Linux 平台没有 /proc/net/route 与 ZT 虚拟网卡，
// 硬凑出来的结果（如 WSL 的 vEthernet）只会误导人。
func detectInterfaces(e *Env) {
	if runtime.GOOS != "linux" {
		return
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	e.DefaultIface = defaultRouteIface()

	// 收集候选 LAN 接口，两轮决定：
	//   优先「有私有地址且不是默认路由出口」的接口（典型的内网侧接口）；
	//   没有的话退回默认路由接口本身 —— 旁路由场景下它就是内网接口
	//   （你的环境：br-lan 192.168.1.254/24，且 default via 192.168.1.1 dev br-lan）。
	type cand struct {
		name  string
		cidr  string
		isDef bool
	}
	var cands []cand

	for _, it := range ifaces {
		if it.Flags&net.FlagUp == 0 {
			continue
		}
		name := it.Name

		// ZeroTier 接口名由 ZT 自己生成，形如 zt6vwy3rne
		if strings.HasPrefix(name, "zt") && len(name) >= 3 {
			if z := ztIface(it); z.Name != "" {
				e.ZTIfaces = append(e.ZTIfaces, z)
			}
			continue
		}

		if isVirtualIface(name) {
			continue
		}
		ip, bits, ok := firstIPv4(it)
		if !ok || !isPrivateIPv4(ip) {
			continue
		}
		cands = append(cands, cand{
			name:  name,
			cidr:  ip + "/" + strconv.Itoa(bits),
			isDef: name == e.DefaultIface,
		})
	}

	// 第一轮：非默认路由接口
	for _, c := range cands {
		if !c.isDef {
			e.LANIface, e.LANIP = c.name, c.cidr
			return
		}
	}
	// 第二轮：默认路由接口本身就是内网侧（旁路由）
	for _, c := range cands {
		e.LANIface, e.LANIP = c.name, c.cidr
		return
	}
}

func ztIface(it net.Interface) ZTIface {
	ip, bits, ok := firstIPv4(it)
	if !ok {
		return ZTIface{}
	}
	return ZTIface{
		Name: it.Name,
		IP:   ip,
		Bits: bits,
		CIDR: fmt.Sprintf("%s/%d", ip, bits),
		MTU:  it.MTU,
	}
}

func firstIPv4(it net.Interface) (string, int, bool) {
	addrs, err := it.Addrs()
	if err != nil {
		return "", 0, false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			ones, _ := ipnet.Mask.Size()
			return v4.String(), ones, true
		}
	}
	return "", 0, false
}

// defaultRouteIface 从 /proc/net/route 解析默认路由接口。
//
// /proc/net/route 的列顺序是：
//
//	Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
//	  0       1         2      3     4     5    6     7    8    9     10
//
// 默认路由的判据是 Destination=00000000 且 **Mask=00000000**（第 7 列），
// 不是 Gateway 为 0 —— 之前踩过这个坑，实机上线后 doctor 立刻报了默认路由解析失败。
func defaultRouteIface() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // 跳过表头
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		if fields[1] == "00000000" && fields[7] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// isVirtualIface 判断是否为不应参与「LAN 接口」推断的虚拟接口。
//
// 注意：br-lan 是 OpenWrt 的标准 LAN 网桥，**必须**参与推断；
// 而 docker 的网络网桥名为 br- 加 12 位十六进制，需要排除。
func isVirtualIface(name string) bool {
	for _, p := range []string{"lo", "docker", "veth", "tun", "tap", "tailscale", "zt", "sit", "gre", "ip6", "dummy", "wg", "virbr"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	if isDockerBridge(name) {
		return true
	}
	return false
}

// isDockerBridge 识别 docker 自动创建的网络网桥（br- + 12 位十六进制）。
func isDockerBridge(name string) bool {
	if !strings.HasPrefix(name, "br-") || len(name) != 15 {
		return false
	}
	for _, c := range name[3:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isPrivateIPv4(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return ip.IsPrivate()
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func hasCmd(name string) bool { return lookPath(name) != "" }

// Summary 返回一行环境摘要，用于启动日志。
func (e *Env) Summary() string {
	parts := []string{e.GOOS + "/" + e.GOARCH, e.Distro}
	if e.IsOpenWrt {
		parts = append(parts, "openwrt")
	}
	parts = append(parts, "fw="+string(e.Firewall))
	if len(e.ZTIfaces) > 0 {
		names := make([]string, 0, len(e.ZTIfaces))
		for _, z := range e.ZTIfaces {
			names = append(names, z.CIDR)
		}
		parts = append(parts, "zt="+strings.Join(names, ","))
	}
	return strings.Join(parts, " ")
}
