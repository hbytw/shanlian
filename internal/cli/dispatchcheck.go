package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"slx/internal/cnlist"
	"slx/internal/dispatch"
	"slx/internal/envdetect"
	"slx/internal/netcfg"
)

// dispatchChecks 检查分流所需的三个要素：接口、数据、规则。
//
// 分开检查是因为它们的故障表现完全不同：
//   - 缺接口 → 分流根本装不上，日志里会有明确提示
//   - 缺数据 → 拒绝启用（安全设计），境外流量不走隧道
//   - 缺规则 → 最隐蔽：进程在跑、日志正常，但流量悄悄走了原路
func dispatchChecks(env *envdetect.Env) []Check {
	var out []Check
	sys := netcfg.NewSystem(nil)

	// —— ZT 环路保护（独立于分流开关）——
	//
	// 刻意放在最前面，而且不受「分流是否在位」影响：
	// 它是隧道的安全底线，分流被暂停时也必须在位。
	// 历史上它跟着分流一起被撤，于是形成死锁：
	// 撤分流 → ZT 流量进隧道 → agent 连不回网关 → 没有出口 → 继续撤分流。
	out = append(out, ztGuardCheck(sys, env))

	// —— 分流数据 ——
	if st, err := os.Stat(cnlist.DefaultCachePath); err == nil {
		age := time.Since(st.ModTime())
		// 从缓存内容取条数
		n := 0
		if b, err := os.ReadFile(cnlist.DefaultCachePath); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if line != "" && !strings.HasPrefix(line, "#") {
					n++
				}
			}
		}
		if age > cnlist.CacheMaxAge {
			out = append(out, Check{
				Name: "分流数据", Layer: "分流", Status: StatusWarn,
				Detail: fmt.Sprintf("中国大陆网段已 %d 天未更新（%d 条）", int(age.Hours()/24), n),
				Hint:   "国内 IP 段每天都有变化；联网后重启 slx 会自动更新，或手动删除 " + cnlist.DefaultCachePath,
			})
		} else {
			out = append(out, Check{
				Name: "分流数据", Layer: "分流", Status: StatusPass,
				Detail: fmt.Sprintf("中国大陆网段 %d 条，%d 小时前更新", n, int(age.Hours())),
			})
		}
	} else {
		out = append(out, Check{
			Name: "分流数据", Layer: "分流", Status: StatusSkip,
			Detail: "尚未获取（首次运行 slx 时会自动下载）",
		})
	}

	// —— 分流规则 ——
	if !sys.IPSetExists(dispatch.SetCN) {
		out = append(out, Check{
			Name: "分流规则", Layer: "分流", Status: StatusSkip,
			Detail: "未安装（slx 未以网关模式运行，或分流被关闭）",
		})
		return out
	}

	cnCount := sys.IPSetCount(dispatch.SetCN)
	exCount := sys.IPSetCount(dispatch.SetExempt)

	var problems []string
	if cnCount < 100 {
		problems = append(problems, fmt.Sprintf("国内网段集合仅 %d 条", cnCount))
	}
	if exCount < len(dispatch.ReservedNets) {
		problems = append(problems, fmt.Sprintf("豁免集合仅 %d 条（应至少 %d 条）", exCount, len(dispatch.ReservedNets)))
	}
	if !sys.IPRuleExists(dispatch.RulePriorityMark,
		fmt.Sprintf("fwmark 0x%x", dispatch.MarkRoute), fmt.Sprintf("%d", dispatch.RouteTable)) {
		problems = append(problems, "fwmark 策略路由规则缺失")
	}
	if !sys.RouteInTableExists(dispatch.RouteTable, "", "slx0") {
		problems = append(problems, fmt.Sprintf("路由表 %d 里没有默认隧道路由", dispatch.RouteTable))
	}

	if len(problems) > 0 {
		out = append(out, Check{
			Name: "分流规则", Layer: "分流", Status: StatusFail,
			Detail: strings.Join(problems, "；"),
			Hint:   "分流规则会被 OpenWrt 防火墙重载清掉；slx 的看门狗会自动修复，若此处持续失败请检查日志",
		})
	} else {
		out = append(out, Check{
			Name: "分流规则", Layer: "分流", Status: StatusPass,
			Detail: fmt.Sprintf("国内网段 %d 条，豁免 %d 条，策略路由在位", cnCount, exCount),
		})
	}

	// —— 环路防护 ——
	ztOK := true
	for _, p := range []string{"slx_exempt"} {
		if sys.IPSetCount(p) < 0 {
			ztOK = false
		}
	}
	out = append(out, Check{
		Name: "环路防护", Layer: "分流", Status: boolToStatus(ztOK),
		Detail: exemptDetail(sys),
		Hint:   "ZT 网段必须豁免，否则隧道自己的流量会被吸进隧道形成死循环",
	})

	return out
}

func exemptDetail(sys *netcfg.System) string {
	n := sys.IPSetCount(dispatch.SetExempt)
	if n < 0 {
		return "豁免集合不存在"
	}
	return fmt.Sprintf("豁免集合 %d 条（私有段 + 隧道内层 + ZT 网段）", n)
}

// ztGuardCheck 体检「ZT 环路保护规则」是否在位。
//
// 这条规则（priority 100, to <ZT网段>, lookup main）是隧道不把自己
// 吸进隧道的最后一道防线，生命周期独立于分流开关：
// 分流被暂停时它也必须留着，否则出口 agent 连不回网关。
func ztGuardCheck(sys *netcfg.System, env *envdetect.Env) Check {
	guard := dispatch.NewZTGuard(ztNetworks(env))
	if guard.Empty() {
		return Check{
			Name: "ZT 环路保护", Layer: "分流", Status: StatusSkip,
			Detail: "本机没有 ZeroTier 接口，无需该规则",
		}
	}
	var missing []string
	for _, zt := range guard.Subnets() {
		if !sys.IPRuleExists(dispatch.RulePriorityZT,
			dispatch.ZTRuleSelector(zt), dispatch.TableMain) {
			missing = append(missing, zt)
		}
	}
	if len(missing) == 0 {
		return Check{
			Name: "ZT 环路保护", Layer: "分流", Status: StatusPass,
			Detail: fmt.Sprintf("ZT 优先规则在位（%v），独立于分流开关", guard.Subnets()),
		}
	}
	return Check{
		Name: "ZT 环路保护", Layer: "分流", Status: StatusFail,
		Detail: fmt.Sprintf("ZT 优先规则缺失：%v", missing),
		Hint: "缺这条规则时 ZT 网段流量会被吸进隧道，出口 agent 连不回网关（分流被暂停时尤其致命）；" +
			"手工补: ip rule add to <ZT网段> lookup main priority 100（slx 运行时会自动补回）",
	}
}

func boolToStatus(ok bool) Status {
	if ok {
		return StatusPass
	}
	return StatusFail
}
