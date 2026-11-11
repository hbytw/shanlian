package netcfg

import (
	"fmt"
	"strings"
)

// MasqSpec 描述一条 SNAT 规则。
type MasqSpec struct {
	// SourceCIDR 是要被 NAT 的源地址段，如 "10.254.0.0/24"。
	SourceCIDR string
	// OutIface 是出口网卡名，如 "eth0"。为空表示不限网卡。
	OutIface string
	// InIface 是入口网卡名。非空时只 NAT 从该接口进来的包，
	// 用于把「隧道流量」与其它同源地址的流量区分开。
	InIface string
	// Comment 是本条规则的用途备注，会写进 iptables comment。
	Comment string
}

// EnsureMasquerade 确保存在一条 MASQUERADE 规则（幂等）。
//
// 为什么用 MASQUERADE 而不是 SNAT --to-source：
// MASQUERADE 会自动跟随网卡当前地址变化，VPS 换 IP、DHCP 续租都不需要改规则。
//
// SourceCIDR 为空表示不限源地址（只按出接口匹配），
// 网关侧正是这种用法：凡是从隧道出去的一律改写成内层地址。
func (s *System) EnsureMasquerade(spec MasqSpec) (changed bool, err error) {
	exists, err := s.hasMasquerade(spec)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	args := []string{"-t", "nat", "-A", "POSTROUTING"}
	if spec.InIface != "" {
		args = append(args, "-i", spec.InIface)
	}
	if spec.SourceCIDR != "" {
		args = append(args, "-s", spec.SourceCIDR)
	}
	if spec.OutIface != "" {
		args = append(args, "-o", spec.OutIface)
	}
	args = append(args, "-m", "comment", "--comment", markOf(spec.Comment), "-j", "MASQUERADE")

	if out, err := s.runCmd(s.IPTables, args...); err != nil {
		return false, fmt.Errorf("添加 MASQUERADE 失败: %v (%s)\n"+
			"提示: 需要 root 或 CAP_NET_ADMIN", err, out)
	}
	return true, nil
}

// RemoveMasquerade 删除对应规则。
func (s *System) RemoveMasquerade(spec MasqSpec) error {
	exists, err := s.hasMasquerade(spec)
	if err != nil || !exists {
		return nil
	}
	args := []string{"-t", "nat", "-D", "POSTROUTING"}
	if spec.InIface != "" {
		args = append(args, "-i", spec.InIface)
	}
	if spec.SourceCIDR != "" {
		args = append(args, "-s", spec.SourceCIDR)
	}
	if spec.OutIface != "" {
		args = append(args, "-o", spec.OutIface)
	}
	args = append(args, "-m", "comment", "--comment", markOf(spec.Comment), "-j", "MASQUERADE")
	_, _ = s.runCmd(s.IPTables, args...)
	return nil
}

func (s *System) hasMasquerade(spec MasqSpec) (bool, error) {
	args := []string{"-t", "nat", "-C", "POSTROUTING"}
	if spec.InIface != "" {
		args = append(args, "-i", spec.InIface)
	}
	if spec.SourceCIDR != "" {
		args = append(args, "-s", spec.SourceCIDR)
	}
	if spec.OutIface != "" {
		args = append(args, "-o", spec.OutIface)
	}
	args = append(args, "-m", "comment", "--comment", markOf(spec.Comment), "-j", "MASQUERADE")

	_, err := s.runCmd(s.IPTables, args...)
	if err == nil {
		return true, nil
	}
	// `iptables -C` 在规则不存在时退出码为 1，这不算错误。
	// 真正的错误（如权限不足）会带 stderr 文本。
	return false, nil
}

// SNATSpec 描述一条显式源地址转换规则。
type SNATSpec struct {
	SourceCIDR string // 可空
	OutIface   string
	ToSource   string // 改写成的源地址，如 10.254.221.153
	Comment    string
}

// EnsureSNAT 确保存在一条 SNAT --to-source 规则（幂等）。
//
// 与 MASQUERADE 的区别：MASQUERADE 取「出接口的地址」，因此要求接口上必须有 IP；
// SNAT --to-source 直接指定地址，不需要接口持有它。
// 我们的隧道接口刻意不配地址（避免 ZeroTier 认领），所以网关侧必须用这种形式。
func (s *System) EnsureSNAT(spec SNATSpec) (bool, error) {
	base := []string{"-t", "nat"}
	if spec.SourceCIDR != "" {
		base = append(base, "-s", spec.SourceCIDR)
	}
	if spec.OutIface != "" {
		base = append(base, "-o", spec.OutIface)
	}
	base = append(base, "-m", "comment", "--comment", markOf(spec.Comment),
		"-j", "SNAT", "--to-source", spec.ToSource)

	chk := append([]string{"-t", "nat", "-C", "POSTROUTING"}, base[2:]...)
	if _, err := s.runCmd(s.IPTables, chk...); err == nil {
		return false, nil
	}
	add := append([]string{"-t", "nat", "-A", "POSTROUTING"}, base[2:]...)
	if out, err := s.runCmd(s.IPTables, add...); err != nil {
		return false, fmt.Errorf("添加 SNAT 失败: %v (%s)\n提示: 需要 root 或 CAP_NET_ADMIN", err, out)
	}
	return true, nil
}

// RemoveSNAT 删除显式 SNAT 规则。
func (s *System) RemoveSNAT(spec SNATSpec) {
	args := []string{"-t", "nat", "-D", "POSTROUTING"}
	if spec.SourceCIDR != "" {
		args = append(args, "-s", spec.SourceCIDR)
	}
	if spec.OutIface != "" {
		args = append(args, "-o", spec.OutIface)
	}
	args = append(args, "-m", "comment", "--comment", markOf(spec.Comment),
		"-j", "SNAT", "--to-source", spec.ToSource)
	_, _ = s.runCmd(s.IPTables, args...)
}

// EnsureForwardAccept 确保网卡间的转发被放行（幂等）。
//
// 有些发行版默认 FORWARD 策略是 DROP（Ubuntu 的 ufw、CentOS 的 firewalld 常见），
// 不放行的话现象是"能连上但完全没流量"，非常难查。
func (s *System) EnsureForwardAccept(iface string, outgoing bool) (changed bool, err error) {
	args := []string{}
	if outgoing {
		args = append(args, "-o", iface)
	} else {
		args = append(args, "-i", iface)
	}
	comment := markOf("forward-" + iface)

	chk := append([]string{"-C", "FORWARD"}, args...)
	chk = append(chk, "-m", "comment", "--comment", comment, "-j", "ACCEPT")
	if _, err := s.runCmd(s.IPTables, chk...); err == nil {
		return false, nil
	}

	// 必须插到链首，不能追加。
	//
	// 实测踩到的坑：firewalld / docker / 宝塔面板都会在 FORWARD 末尾塞一条
	// 「-j REJECT --reject-with icmp-host-prohibited」兜底。
	// 用 -A 追加的 ACCEPT 排在它后面，永远不会被匹配到 ——
	// 现象是「ICMP 能通但 TCP 一律 No route to host」，极难查。
	add := append([]string{"-I", "FORWARD", "1"}, args...)
	add = append(add, "-m", "comment", "--comment", comment, "-j", "ACCEPT")
	if out, err := s.runCmd(s.IPTables, add...); err != nil {
		return false, fmt.Errorf("放行 FORWARD 失败: %v (%s)", err, out)
	}
	return true, nil
}

// RemoveForwardAccept 删除放行规则。
func (s *System) RemoveForwardAccept(iface string, outgoing bool) {
	args := []string{}
	if outgoing {
		args = append(args, "-o", iface)
	} else {
		args = append(args, "-i", iface)
	}
	comment := markOf("forward-" + iface)
	del := append([]string{"-D", "FORWARD"}, args...)
	del = append(del, "-m", "comment", "--comment", comment, "-j", "ACCEPT")
	_, _ = s.runCmd(s.IPTables, del...)
}

// EnsurePostroutingMasqOnIface 给「从某网卡出去的流量」做源地址伪装。
//
// 网关角色用：把内网流量在进入隧道前，源地址改成隧道内层 IP，
// 这样出口节点看到的源地址是内层地址，它才好做自己的 SNAT。
func (s *System) EnsurePostroutingMasqOnIface(iface, sourceCIDR string) (bool, error) {
	return s.EnsureMasquerade(MasqSpec{
		SourceCIDR: sourceCIDR,
		OutIface:   iface,
		Comment:    "gateway-snat",
	})
}

// CleanupMarked 删除所有带本系统标记的 iptables 规则。
//
// 用于 uninstall：只删我们自己加的，用户原有规则一条不动。
func (s *System) CleanupMarked() (removed int) {
	for _, table := range []string{"nat", "filter"} {
		for _, chain := range []string{"POSTROUTING", "FORWARD"} {
			out, err := s.runCmd(s.IPTables, "-t", table, "-S", chain)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(out, "\n") {
				if !strings.Contains(line, `"--comment" "`+Marker+`"`) &&
					!strings.Contains(line, `--comment "`+Marker) {
					continue
				}
				// 把 "-A CHAIN ..." 变成 "-D CHAIN ..."
				del := strings.Replace(line, "-A "+chain, "-D "+chain, 1)
				args := append([]string{"-t", table}, splitArgs(del)...)
				if _, err := s.runCmd(s.IPTables, args...); err == nil {
					removed++
				}
			}
		}
	}
	return removed
}

// markOf 生成带统一前缀的注释。
func markOf(detail string) string {
	if detail == "" {
		return Marker
	}
	return Marker + ":" + detail
}

// splitArgs 按空白切分命令行（iptables -S 的输出格式稳定，不涉及引号转义）。
func splitArgs(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		out = append(out, strings.Trim(f, `"`))
	}
	return out
}

// EnsureMSSClamp 对经过隧道方向的 TCP SYN 做 MSS 钳制。
//
// 为什么必须有这一步：
// 内层 MTU 是 1380，而链路上的真实 MTU 往往是 1500/9000。
// 远端服务器会按自己的 MTU 发 1400+ 字节的 TCP 段，进入隧道后装不下，
// 表现为「握手能开始但 TLS 证书传不完」——curl 拿到空响应、浏览器一直转圈。
// 这是所有隧道方案的必做项，漏掉时的现象极具误导性（像是被墙了）。
//
// 用 --clamp-mss-to-pmtu 而不是写死数值：它会跟随路由 MTU 自适应，
// 将来改 MTU 不用同步改规则。
func (s *System) EnsureMSSClamp(dev string, outgoing bool) (changed bool, err error) {
	// 只在 mangle 表里对 SYN 包钳制，开销极低（每条连接一次）
	dir := "-o"
	if !outgoing {
		dir = "-i"
	}
	// 同样插到链首：mangle 表里也可能有先于我们的规则改动 MSS
	args := []string{"-t", "mangle", "-I", "FORWARD", "1", "-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN", dir, dev,
		"-m", "comment", "--comment", markOf("mss-" + dev),
		"-j", "TCPMSS", "--clamp-mss-to-pmtu"}

	chk := []string{"-t", "mangle", "-C", "FORWARD", "-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN", dir, dev,
		"-m", "comment", "--comment", markOf("mss-" + dev),
		"-j", "TCPMSS", "--clamp-mss-to-pmtu"}
	if _, err := s.runCmd(s.IPTables, chk...); err == nil {
		return false, nil
	}
	if out, err := s.runCmd(s.IPTables, args...); err != nil {
		return false, fmt.Errorf("设置 MSS 钳制失败: %v (%s)", err, out)
	}
	return true, nil
}

// RemoveMSSClamp 删除 MSS 钳制规则。
func (s *System) RemoveMSSClamp(dev string, outgoing bool) {
	dir := "-o"
	if !outgoing {
		dir = "-i"
	}
	_, _ = s.runCmd(s.IPTables, "-t", "mangle", "-D", "FORWARD", "-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN", dir, dev,
		"-m", "comment", "--comment", markOf("mss-"+dev),
		"-j", "TCPMSS", "--clamp-mss-to-pmtu")
}

// TunnelMark 是本系统给「从隧道进来的包」打的 fwmark 值。
//
// 为什么需要 mark：
// POSTROUTING 链不允许使用 -i（入接口只在 PREROUTING/FORWARD 有意义），
// 所以无法在 NAT 阶段直接「只匹配从隧道来的包」。
// 标准做法是在 PREROUTING 打个 mark，再在 POSTROUTING 按 mark 匹配。
//
// 这也是 DESIGN.md §7.2 第四道防护（环路防护）的落地方式：
// 即使将来有别的程序以同样源地址发流量（例如 ZeroTier 认领了隧道地址），
// 只要它不是从隧道接口进来的，就不会被打 mark，也就不会被误 NAT。
const TunnelMark = 0x51

// EnsureTunnelMark 在 PREROUTING 给从隧道接口进来的包打 mark（幂等）。
func (s *System) EnsureTunnelMark(iface string) (bool, error) {
	comment := markOf("tunnel-mark")
	chk := []string{"-t", "mangle", "-C", "PREROUTING", "-i", iface,
		"-m", "comment", "--comment", comment, "-j", "MARK", "--set-mark", fmt.Sprintf("%d", TunnelMark)}
	if _, err := s.runCmd(s.IPTables, chk...); err == nil {
		return false, nil
	}
	add := []string{"-t", "mangle", "-I", "PREROUTING", "1", "-i", iface,
		"-m", "comment", "--comment", comment, "-j", "MARK", "--set-mark", fmt.Sprintf("%d", TunnelMark)}
	if out, err := s.runCmd(s.IPTables, add...); err != nil {
		return false, fmt.Errorf("设置隧道 mark 失败: %v (%s)", err, out)
	}
	return true, nil
}

// RemoveTunnelMark 删除隧道 mark 规则。
func (s *System) RemoveTunnelMark(iface string) {
	_, _ = s.runCmd(s.IPTables, "-t", "mangle", "-D", "PREROUTING", "-i", iface,
		"-m", "comment", "--comment", markOf("tunnel-mark"),
		"-j", "MARK", "--set-mark", fmt.Sprintf("%d", TunnelMark))
}

// EnsureMasqueradeByMark 对打了隧道 mark 且从指定网卡出去的包做 MASQUERADE（幂等）。
func (s *System) EnsureMasqueradeByMark(outIface, comment string) (bool, error) {
	mark := fmt.Sprintf("0x%x", TunnelMark)
	base := []string{"-m", "mark", "--mark", mark, "-o", outIface,
		"-m", "comment", "--comment", markOf(comment), "-j", "MASQUERADE"}

	chk := append([]string{"-t", "nat", "-C", "POSTROUTING"}, base...)
	if _, err := s.runCmd(s.IPTables, chk...); err == nil {
		return false, nil
	}
	add := append([]string{"-t", "nat", "-A", "POSTROUTING"}, base...)
	if out, err := s.runCmd(s.IPTables, add...); err != nil {
		return false, fmt.Errorf("添加出口 MASQUERADE 失败: %v (%s)", err, out)
	}
	return true, nil
}

// RemoveMasqueradeByMark 删除按 mark 匹配的 MASQUERADE 规则。
func (s *System) RemoveMasqueradeByMark(outIface, comment string) {
	mark := fmt.Sprintf("0x%x", TunnelMark)
	_, _ = s.runCmd(s.IPTables, "-t", "nat", "-D", "POSTROUTING",
		"-m", "mark", "--mark", mark, "-o", outIface,
		"-m", "comment", "--comment", markOf(comment), "-j", "MASQUERADE")
}
