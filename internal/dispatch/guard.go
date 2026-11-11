package dispatch

import (
	"fmt"
	"strings"
)

// TableMain 是内核主路由表的名字。
//
// ZT 保护规则指向它：ZT 网段的流量按普通路由走，不进策略路由表 100。
const TableMain = "main"

// ZTGuard 负责 ZeroTier 网段的「环路保护」规则。
//
// 规则形如：
//
//	100: from all to 192.168.99.0/24 lookup main
//
// 作用：ZT 网段的流量永远走 main 表，绝不会被
// 「fwmark → 策略路由表 100 → default dev 隧道」那条链吸进隧道。
// 隧道本身就跑在 ZT 上，它自己的出站流量一旦进了隧道就是死循环：
// ARP 应答超时、keepalive 超时，出口 agent 集体掉线。
//
// # 为什么它的生命周期独立于「分流启用 / 暂停」
//
// 分流看门狗（internal/cli/dispatch.go 的 evaluate）在「没有可用出口 agent」时
// 会**主动撤下分流规则**，让流量退回直连。这本身是对的：
// 有隧道但走不通，比没有隧道更糟（用户看到的是境外网站全部超时）。
//
// 但如果这条保护规则跟着分流一起被撤掉，就会形成死锁：
//
//	撤分流 → ZT 网段不再被豁免 → ZT 流量被吸进隧道 → agent 连不回网关
//	→ 没有可用出口 → 分流继续被撤 → 永远起不来
//
// 所以它绑定的是**进程**，不是**分流状态**：
//
//	进程启动时装一次；分流暂停 / 恢复都不动它；
//	只有进程退出（或 slx uninstall）才撤。
//
// 可验证的行为见 dispatch_test.go：
// 「暂停分流（Teardown）之后，ZT 保护规则依然存在」。
type ZTGuard struct {
	subnets []string
}

// NewZTGuard 创建保护器。空串与重复项会被过滤掉。
func NewZTGuard(subnets []string) *ZTGuard {
	seen := map[string]bool{}
	var out []string
	for _, s := range subnets {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return &ZTGuard{subnets: out}
}

// Subnets 返回受保护的网段。
func (g *ZTGuard) Subnets() []string {
	if g == nil {
		return nil
	}
	return g.subnets
}

// Empty 表示没有需要保护的网段（比如没装 ZeroTier）。
func (g *ZTGuard) Empty() bool {
	return g == nil || len(g.subnets) == 0
}

// Install 确保 ZT 保护规则在位（幂等）。
//
// 幂等很重要：看门狗会反复调用它（OpenWrt 防火墙重载会把规则清掉）。
func (g *ZTGuard) Install(sys System) error {
	if g == nil {
		return nil
	}
	var firstErr error
	for _, zt := range g.subnets {
		if _, err := sys.EnsureIPRule(RulePriorityZT, ztSelector(zt), TableMain); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("添加 ZT 优先规则失败（%s）: %w", zt, err)
			}
		}
	}
	return firstErr
}

// Verify 检查保护规则是否还在。
//
// OpenWrt 的 firewall3/fw4 在服务重启、UCI 提交、界面保存时都会整体重建
// 规则表，我们插进去的规则会被抹掉。规则一没，ZT 流量就会被吸进隧道形成环路 ——
// 所以这条必须有人一直看着。
func (g *ZTGuard) Verify(sys System) []Problem {
	var probs []Problem
	if g == nil {
		return nil
	}
	for _, zt := range g.subnets {
		if !sys.IPRuleExists(RulePriorityZT, ztSelector(zt), TableMain) {
			probs = append(probs, Problem{
				What: fmt.Sprintf("ZT 优先规则丢失（%s）", zt),
				Hint: "缺这条规则时 ZT 流量可能被吸进隧道形成环路",
			})
		}
	}
	return probs
}

// Remove 撤销保护规则。
//
// **只在进程退出或 slx uninstall 时调用** —— 分流暂停绝不能走到这里。
func (g *ZTGuard) Remove(sys System) {
	if g == nil {
		return
	}
	for _, zt := range g.subnets {
		sys.RemoveIPRule(RulePriorityZT, ztSelector(zt), TableMain)
	}
}

// ztSelector 生成 ZT 保护规则的选择器。
//
// 刻意与分流规则用同一套拼法（"to <网段>"），这样 ip rule 的匹配与
// EnsureIPRule / IPRuleExists 的判断三边一致。
func ztSelector(zt string) string {
	return ZTRuleSelector(zt)
}

// ZTRuleSelector 生成 ZT 保护规则的选择器（"to <网段>"）。
//
// 导出给体检命令用：检查「规则还在不在」必须用与安装时完全一样的拼法，
// 否则会出现「明明有规则却报缺失」。
func ZTRuleSelector(zt string) string {
	return "to " + zt
}
