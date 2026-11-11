package netcfg

import (
	"fmt"
	"strings"
)

// RouteSpec 描述一条路由。
type RouteSpec struct {
	// Dst 目标网段或主机，如 "8.8.8.8/32" 或 "10.254.0.0/24"。空字符串表示默认路由。
	Dst string
	// Dev 出接口名。
	Dev string
	// Table 路由表号，0 表示 main 表。
	Table int
}

func (r RouteSpec) dstOr0() string {
	if r.Dst == "" {
		return "default"
	}
	return r.Dst
}

// args 生成 ip route 命令参数（不含动作）。
func (r RouteSpec) args() []string {
	var a []string
	if r.Table != 0 {
		a = append(a, "route", "show", "table", fmt.Sprintf("%d", r.Table))
	}
	return a
}

// EnsureRoute 确保路由存在（幂等）。
func (s *System) EnsureRoute(spec RouteSpec) (bool, error) {
	exists, err := s.RouteExists(spec)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	args := []string{"route", "add", spec.dstOr0(), "dev", spec.Dev}
	if spec.Table != 0 {
		args = append(args, "table", fmt.Sprintf("%d", spec.Table))
	}
	if out, err := s.runCmd(s.IP, args...); err != nil {
		if strings.Contains(out, "File exists") {
			return false, nil
		}
		return false, fmt.Errorf("添加路由 %s 失败: %v (%s)", spec.dstOr0(), err, out)
	}
	return true, nil
}

// RemoveRoute 删除路由（不存在则忽略）。
func (s *System) RemoveRoute(spec RouteSpec) error {
	args := []string{"route", "del", spec.dstOr0(), "dev", spec.Dev}
	if spec.Table != 0 {
		args = append(args, "table", fmt.Sprintf("%d", spec.Table))
	}
	_, err := s.runCmd(s.IP, args...)
	if err != nil {
		return nil // 不存在就当作已清理
	}
	return nil
}

// RouteExists 判断路由是否存在。
func (s *System) RouteExists(spec RouteSpec) (bool, error) {
	args := []string{"route", "show"}
	if spec.Dst != "" {
		args = append(args, spec.dstOr0())
	}
	if spec.Table != 0 {
		args = append(args, "table", fmt.Sprintf("%d", spec.Table))
	}
	out, err := s.runCmd(s.IP, args...)
	if err != nil {
		return false, nil
	}
	return strings.Contains(out, "dev "+spec.Dev), nil
}

// RoutesViaDev 返回所有从指定设备出去的路由（用于环路检查）。
func (s *System) RoutesViaDev(dev string) ([]string, error) {
	out, err := s.runCmd(s.IP, "route", "show", "dev", dev)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// —— 环路防护 ——

// Problem 是一个检测到的环路风险。
type Problem struct {
	// Fatal 为 true 表示这是必然导致环路的配置，必须阻止启动。
	Fatal bool
	What  string
	Hint  string
}

func (p Problem) String() string {
	lv := "警告"
	if p.Fatal {
		lv = "致命"
	}
	s := fmt.Sprintf("[%s] %s", lv, p.What)
	if p.Hint != "" {
		s += "\n        修复: " + p.Hint
	}
	return s
}

// GuardSpec 描述环路检查所需的上下文。
type GuardSpec struct {
	// TunnelDev 是隧道设备名（如 slx0）。
	TunnelDev string
	// InnerCIDR 是隧道内层网段（如 10.254.0.0/24）。
	InnerCIDR string
	// ZTSubnets 是 ZeroTier 网段。隧道本身跑在这些网段上，
	// 它们一旦被路由进隧道就是必死循环。
	ZTSubnets []string
}

// CheckLoopRisk 检查是否存在把隧道自身流量卷进隧道的配置。
//
// 这是 DESIGN.md §7.2 的核心：ZT 网段通常是 10.x / 192.168.x，
// 这些私有地址不在"中国大陆 IP 段"列表里，极易被误判成"境外流量"。
// 一旦真的被送进隧道，隧道自己又要走 ZT 出去 → 死循环。
//
// 返回的 Problem 里 Fatal=true 的项必须在启动前拦住。
func (s *System) CheckLoopRisk(spec GuardSpec) ([]Problem, error) {
	var probs []Problem

	routes, err := s.RoutesViaDev(spec.TunnelDev)
	if err != nil {
		// 设备还不存在（首次启动时我们就在建它之前做检查），
		// `ip route show dev X` 会失败。此时不可能有指向它的路由，
		// 直接视为无风险，而不是把它当成检查失败 —— 否则每次首次启动都会误报。
		routes = nil
	}

	for _, r := range routes {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		// 第一条：默认路由绝不能指向隧道设备。
		if strings.HasPrefix(r, "default") {
			probs = append(probs, Problem{
				Fatal: true,
				What:  fmt.Sprintf("默认路由指向了隧道设备 %s：%s", spec.TunnelDev, r),
				Hint:  "默认路由必须留在原出口。只把需要代理的目标路由到隧道（策略路由 + fwmark）",
			})
		}
		// 第二条：ZT 网段绝不能指向隧道设备。
		for _, zt := range spec.ZTSubnets {
			if zt == "" {
				continue
			}
			if strings.Contains(r, zt) || strings.Contains(r, cidrPrefix(zt)) {
				probs = append(probs, Problem{
					Fatal: true,
					What:  fmt.Sprintf("ZeroTier 网段 %s 被路由进了隧道 %s", zt, spec.TunnelDev),
					Hint:  "隧道本身的流量走这个网段，进去就出不来。把 ZT 网段加入豁免集合",
				})
			}
		}
	}

	// 第三条：内层网段应当就在隧道设备上，不应出现在别处
	if spec.InnerCIDR != "" {
		out, err := s.runCmd(s.IP, "route", "show", spec.InnerCIDR)
		if err == nil && out != "" && !strings.Contains(out, "dev "+spec.TunnelDev) {
			probs = append(probs, Problem{
				Fatal: false,
				What:  fmt.Sprintf("隧道内层网段 %s 没有指向 %s：%s", spec.InnerCIDR, spec.TunnelDev, out),
				Hint:  "检查是否已有冲突的网段配置",
			})
		}
	}

	return probs, nil
}

// VerifyZTReachable 确认 ZT 网段确实经由 ZT 接口可达。
//
// 这是第三道防护的验证：策略路由优先级必须是
// 「ZT 网段 > fwmark 规则 > main」，否则 ZT 自己的流量会先命中 fwmark。
func (s *System) VerifyZTReachable(ztSubnet, ztDev string) (bool, string) {
	out, err := s.runCmd(s.IP, "route", "get", firstHost(ztSubnet))
	if err != nil {
		return false, fmt.Sprintf("无法查询 %s 的路由: %v", ztSubnet, err)
	}
	if strings.Contains(out, "dev "+ztDev) {
		return true, out
	}
	return false, out
}

// firstHost 从 CIDR 取一个用于探测的代表地址。
func firstHost(cidr string) string {
	if i := strings.Index(cidr, "/"); i > 0 {
		return cidr[:i]
	}
	return cidr
}

// cidrPrefix 取 CIDR 的网络部分，用于宽松匹配路由输出。
func cidrPrefix(cidr string) string {
	if i := strings.Index(cidr, "/"); i > 0 {
		return cidr[:i]
	}
	return cidr
}

// —— loopback 上的内层地址 ——

// EnsureAddrOnLoopback 把一个 /32 地址挂到 lo 上（幂等）。
//
// 用途：让隧道内层地址成为「合法的本地地址」。
//
// 为什么需要：
//  1. SNAT --to-source 用一个不属于任何接口的地址，内核在某些路径上会
//     认为源地址非法，行为不可预期；
//  2. 隧道回程包的目的地址就是内层地址。它不属于任何接口时，
//     内核在路由阶段会把它当成要去别处的包，实测会被直接丢弃
//     （InAddrErrors 增长，既不进 FORWARD 也不进 INPUT）。
//
// 为什么挂在 lo 而不是隧道网卡：
//
//	ZeroTier 只绑定非 loopback 接口。挂 lo 上 ZT 不会认领，
//	避免了「ZT 把隧道地址当自己源地址、P2P 流量灌进隧道」那个坑。
func (s *System) EnsureAddrOnLoopback(ip string) (bool, error) {
	cidr := ip + "/32"
	if out, err := s.runCmd(s.IP, "addr", "show", "dev", "lo"); err == nil {
		if strings.Contains(out, ip+"/32") {
			return false, nil
		}
	}
	if out, err := s.runCmd(s.IP, "addr", "add", cidr, "dev", "lo"); err != nil {
		if strings.Contains(out, "File exists") {
			return false, nil
		}
		return false, fmt.Errorf("把 %s 加到 lo 失败: %v (%s)", cidr, err, out)
	}
	return true, nil
}

// RemoveAddrFromLoopback 从 lo 上删除内层地址。
func (s *System) RemoveAddrFromLoopback(ip string) {
	_, _ = s.runCmd(s.IP, "addr", "del", ip+"/32", "dev", "lo")
}
