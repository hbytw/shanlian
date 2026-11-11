// Package dispatch 实现「只接管境外流量」的分流规则。
//
// 核心机制（DESIGN.md §7.1）：
//
//	目标 ∉ 中国大陆网段 且 目标 ∉ 豁免网段 → 打 fwmark
//	fwmark → 策略路由表 100 → default dev <隧道>
//	其余流量走原来的 main 表，完全不受影响
//
// 环路防护（DESIGN.md §7.2）四道，这里落地三道（第四道在数据面）：
//  1. 保留段与私有段永不标记（否则 LAN、组播、链路本地会被送进隧道）
//  2. ZeroTier 网段显式豁免（隧道本身跑在它上面，进去就是死循环）
//  3. 策略路由优先级：ZT 网段 > fwmark 规则 > main
//
// 第 3 条那条 ZT 规则由本包的 ZTGuard 单独持有（guard.go）：
// 它的生命周期**独立于分流启用/暂停**，否则会形成
// 「撤分流 → 出口没了 → 更要撤分流」的死锁。
package dispatch

import (
	"fmt"
	"strings"
)

// 标记与路由表取值。
//
// 刻意与数据面的「隧道来源标记」（0x51）区分开：
//   - 0x51 表示「这个包是从隧道进来的」，用于限定 SNAT 范围；
//   - 0x100 表示「这个包应该走隧道」，用于策略路由。
//
// 两者语义完全不同，混用会导致流量莫名其妙地消失。
const (
	// MarkRoute 是「该走隧道」的 fwmark。
	MarkRoute uint32 = 0x100
	// RouteTable 是承载默认隧道路由的表号。
	RouteTable = 100
	// SetCN 是中国大陆网段的 ipset 名。
	SetCN = "slx_cn"
	// SetExempt 是「绝不进隧道」的豁免 ipset 名。
	SetExempt = "slx_exempt"

	// RulePriorityZT 让 ZT 网段优先走 main 表（数字越小优先级越高）。
	RulePriorityZT = 100
	// RulePriorityMark 是 fwmark 分流规则的优先级。
	RulePriorityMark = 1000
)

// ReservedNets 是永远不能进隧道的网段。
//
// 除了 RFC1918 私有段，还要排除：
//   - 0.0.0.0/8、127.0.0.0/8、169.254.0.0/16：本机与链路本地
//   - 224.0.0.0/4、240.0.0.0/4：组播与保留
//   - 100.64.0.0/10：运营商级 NAT（很多家宽出口在这个段里）
//
// 漏掉任何一个，都可能把本该走内网的流量吸进隧道。
var ReservedNets = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"224.0.0.0/4",
	"240.0.0.0/4",
}

// Spec 描述一次分流配置所需的全部输入。
type Spec struct {
	// TunnelDev 是隧道设备名。
	TunnelDev string
	// InnerCIDR 是隧道内层网段，必须豁免。
	InnerCIDR string
	// ZTSubnets 是 ZeroTier 网段，必须豁免。
	ZTSubnets []string
	// LANIfaces 是「要做分流」的接口（通常是内网侧）。
	//
	// 刻意只在这些接口上打标记，而不是所有接口：
	// 出口节点从 WAN 收到的回程包目的地址是它自己的公网 IP，
	// 若不加限定会被判成「境外」送进隧道，直接把出口自己搞瘫。
	LANIfaces []string
	// CNCIDRs 是中国大陆网段列表。
	CNCIDRs []string
	// ExtraExempt 是额外要豁免的网段（例如某些内网专线对端）。
	ExtraExempt []string
}

// State 是已生效的分流配置，供看门狗与清理使用。
type State struct {
	Spec Spec
}

// Problem 是一项缺失或异常的配置。
type Problem struct {
	What string
	Hint string
}

func (p Problem) String() string {
	s := p.What
	if p.Hint != "" {
		s += "（" + p.Hint + "）"
	}
	return s
}

// Setup 安装分流配置。幂等：重复执行不会产生重复规则。
//
// 注意：这里**不**碰 ZT 环路保护规则 —— 它的生命周期独立于分流，
// 由 ZTGuard 负责（见 guard.go 的说明）。
func Setup(sys System, spec Spec) (*State, error) {
	if spec.TunnelDev == "" {
		return nil, fmt.Errorf("分流需要隧道设备名")
	}
	if len(spec.CNCIDRs) < 100 {
		return nil, fmt.Errorf("中国大陆网段数据异常（只有 %d 条），拒绝启用分流\n"+
			"提示: 数据不全时会把境内流量误判成境外", len(spec.CNCIDRs))
	}

	// —— 1. 中国大陆网段 ——
	if err := sys.IPSetCreate(SetCN, len(spec.CNCIDRs)+4096); err != nil {
		return nil, err
	}
	n, err := sys.IPSetSwapReplace(SetCN, spec.CNCIDRs)
	if err != nil {
		return nil, fmt.Errorf("加载中国大陆网段失败: %w", err)
	}
	if n < 100 {
		return nil, fmt.Errorf("中国大陆网段装载结果异常（%d 条）", n)
	}

	// —— 2. 豁免集合 ——
	exempt := make([]string, 0, len(ReservedNets)+len(spec.ZTSubnets)+2)
	exempt = append(exempt, ReservedNets...)
	if spec.InnerCIDR != "" {
		exempt = append(exempt, spec.InnerCIDR)
	}
	exempt = append(exempt, spec.ZTSubnets...)
	exempt = append(exempt, spec.ExtraExempt...)

	if err := sys.IPSetCreate(SetExempt, 4096); err != nil {
		return nil, err
	}
	// 豁免集合内容小且变更少，直接 flush 重填即可
	sys.IPSetFlush(SetExempt)
	if _, fail := sys.IPSetAddBatch(SetExempt, exempt); fail > 0 {
		return nil, fmt.Errorf("写入豁免网段失败（%d 条未写入）", fail)
	}

	// —— 3. 分流标记规则 ——
	if len(spec.LANIfaces) == 0 {
		return nil, fmt.Errorf("没有可用于分流的接口\n" +
			"提示: 网关角色需要至少一个内网侧接口（如 br-lan）")
	}
	for _, iface := range spec.LANIfaces {
		if _, err := sys.EnsureMarkRule("PREROUTING", iface, MarkRoute,
			SetCN, SetExempt, "split-mark-"+iface); err != nil {
			return nil, err
		}
	}

	// —— 4. 策略路由规则 ——
	//
	// 优先级 1000（fwmark 走隧道）先装上。
	//
	// **ZT 网段那条 priority 100 的规则不在这里装**：
	// 它是环路保护规则，必须比「分流启用/暂停」活得更久 ——
	// 分流被暂停时它必须留下，否则出口 agent 永远连不回网关（死锁）。
	// 那条规则由 ZTGuard 独立负责，见 guard.go。
	if _, err := sys.EnsureIPRule(RulePriorityMark,
		fmt.Sprintf("fwmark 0x%x", MarkRoute), fmt.Sprintf("%d", RouteTable)); err != nil {
		return nil, err
	}

	// —— 5. 隧道路由表 ——
	if _, err := sys.EnsureRuleInTable(RouteTable, "", spec.TunnelDev); err != nil {
		return nil, err
	}

	return &State{Spec: spec}, nil
}

// Verify 检查分流配置是否仍然完整。
//
// 这是「防火墙重载自愈」的检查端：OpenWrt 的 firewall3/fw4 在服务重启、
// UCI 提交、界面保存时都会整体重建规则表，我们注入的规则会被抹掉，
// 而现象是「境外流量突然变慢或不通」，且日志里什么都没有。
//
// 注意：这里**不**检查 ZT 环路保护规则 —— 它由 ZTGuard 独立看护
// （ZTGuard.Verify）。两边的检查必须分开：分流被暂停时
// dispatch.Verify 根本不会被调用，但保护规则仍然要在位。
func (s *State) Verify(sys System) []Problem {
	var probs []Problem
	spec := s.Spec

	if n := sys.IPSetCount(SetCN); n < 100 {
		probs = append(probs, Problem{
			What: fmt.Sprintf("中国大陆网段集合缺失或过少（%d 条）", n),
			Hint: "通常是被外部命令清除了 ipset",
		})
	}
	if n := sys.IPSetCount(SetExempt); n < len(ReservedNets) {
		probs = append(probs, Problem{
			What: fmt.Sprintf("豁免网段集合不完整（%d 条）", n),
			Hint: "缺豁免会导致环路，必须恢复",
		})
	}

	for _, iface := range spec.LANIfaces {
		if !sys.HasMarkRule("PREROUTING", "split-mark-"+iface) {
			probs = append(probs, Problem{
				What: fmt.Sprintf("分流标记规则丢失（接口 %s）", iface),
				Hint: "多半是防火墙重载把它清掉了",
			})
		}
	}

	if !sys.IPRuleExists(RulePriorityMark, fmt.Sprintf("fwmark 0x%x", MarkRoute),
		fmt.Sprintf("%d", RouteTable)) {
		probs = append(probs, Problem{What: "fwmark 策略路由规则丢失"})
	}
	if !sys.RouteInTableExists(RouteTable, "", spec.TunnelDev) {
		probs = append(probs, Problem{
			What: fmt.Sprintf("路由表 %d 里的默认隧道路由丢失", RouteTable),
		})
	}
	return probs
}

// Teardown 撤销全部分流配置。
//
// 顺序与安装相反，且每步都容忍失败：清理阶段不应该因为某条规则
// 已经不存在而中断，否则会留下半套配置 —— 那比全留更危险。
//
// # 这里绝对不能删 ZT 环路保护规则
//
// 历史上这里有一句：
//
//	for _, zt := range spec.ZTSubnets {
//		sys.RemoveIPRule(RulePriorityZT, "to "+zt, "main")
//	}
//
// 看着像「对称清理」，实际制造了一个死锁：
//
//  1. 看门狗发现「没有可用出口 agent」→ 调 Teardown 撤下分流规则
//     （让流量退回直连，这个行为本身是对的）；
//  2. Teardown 把 ZT 环路保护规则一起撤了；
//  3. ZT 网段的流量不再被豁免 → 被 fwmark 吸进隧道 → 隧道自己的出站流量
//     也进隧道 → 出口 agent 连不回网关；
//  4. 没有可用出口 → 看门狗继续撤分流 → 永远起不来。
//
// 也就是「撤分流 → 出口没了 → 更要撤分流」的正反馈死锁。
//
// 现在 ZT 保护规则由 ZTGuard 独立持有：分流暂停不碰它，
// 只有进程退出（ZTGuard.Remove）才撤。见 guard.go。
func (s *State) Teardown(sys System) {
	spec := s.Spec

	sys.ClearTable(RouteTable)
	sys.RemoveIPRule(RulePriorityMark, fmt.Sprintf("fwmark 0x%x", MarkRoute),
		fmt.Sprintf("%d", RouteTable))
	// ZT 保护规则：**刻意不删**。理由见上面的注释。
	for _, iface := range spec.LANIfaces {
		sys.RemoveMarkRule("PREROUTING", "split-mark-"+iface)
	}
	sys.IPSetDestroy(SetCN)
	sys.IPSetDestroy(SetCN + "_new")
	sys.IPSetDestroy(SetExempt)
}

// EnsureLANIfaces 去重并过滤空的接口名。
func EnsureLANIfaces(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
