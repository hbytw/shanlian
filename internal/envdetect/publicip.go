package envdetect

import "net"

// PublicIPv4 返回本机的公网 IPv4 地址（若有）。
//
// 用途：VPS 作为出口节点时，需要把这个地址通告给网关，
// 好让网关在 ZeroTier 不可达时走直连。
//
// 为什么不能自动处理所有情况：
// Oracle Cloud 等平台的 VNIC 只有内网地址（如 10.0.0.245），
// 公网 IP 由平台做 1:1 NAT 映射，本机查不到。这类节点必须靠
// 配置或外部探测获知自己的公网地址 —— 见 DESIGN.md §0.5。
//
// 返回空串表示「本机没有可直接使用的公网地址」。
func PublicIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, it := range ifaces {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := it.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 == nil {
				continue
			}
			if isGlobalIPv4(v4) {
				return v4.String()
			}
		}
	}
	return ""
}

// isGlobalIPv4 判断是否为全球可路由地址（排除私有、回环、链路本地、CGNAT 等）。
func isGlobalIPv4(ip net.IP) bool {
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10 是运营商级 NAT 段，不是公网
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}
