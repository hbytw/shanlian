package cli

import (
	"context"
	"log/slog"
	"time"

	"slx/internal/dispatch"
	"slx/internal/envdetect"
	"slx/internal/logx"
	"slx/internal/netcfg"
)

// ztGuardInterval 是 ZT 环路保护规则的自检周期。
//
// 比分流看门狗（10 秒）略长一点：这条规则不在流量路径上，
// 它只在「防火墙重载把规则清掉」时需要补回来，15 秒的窗口完全够用。
const ztGuardInterval = 15 * time.Second

// setupZTGuard 安装 ZT 环路保护规则。
//
// 这条规则的生命周期**独立于「分流启用 / 暂停」**（详见 dispatch.ZTGuard）：
//
//	分流被暂停时它必须留下。历史上它跟着分流一起被撤，
//	于是形成死锁：撤分流 → ZT 流量被吸进隧道 → 出口 agent 连不回网关
//	→ 没有可用出口 → 分流继续被撤 → 永远起不来。
//
// 所以它装在**数据面**这一层（而不是分流那一层）：
// 只要进程还在跑、隧道还在，这条规则就在。
// 分流开关（配置里的 dispatch）也管不到它 —— 它是隧道的安全底线，
// 不是「分流功能」的一部分。
func setupZTGuard(log *slog.Logger, sys *netcfg.System, env *envdetect.Env) *dispatch.ZTGuard {
	guard := dispatch.NewZTGuard(ztNetworks(env))
	if guard.Empty() {
		return guard
	}
	if err := guard.Install(sys); err != nil {
		log.Warn("安装 ZT 环路保护规则失败",
			logx.Event("guard.zt_rule_failed"),
			logx.FieldLayer, logx.LayerDispatch,
			logx.FieldErr, err.Error(),
			"subnets", guard.Subnets(),
			logx.Hint("缺这条规则时 ZT 网段流量可能被吸进隧道形成死循环；"+
				"可手工补: ip rule add to <ZT网段> lookup main priority 100"))
		return guard
	}
	log.Info("ZT 环路保护规则已就位",
		logx.Event("guard.zt_rule_ready"),
		logx.FieldLayer, logx.LayerDispatch,
		"subnets", guard.Subnets(),
		logx.Hint("该规则独立于分流开关：分流暂停时也会保留，防止隧道流量被吸进隧道"))
	return guard
}

// watchZTGuard 独立看护 ZT 环路保护规则。
//
// 与分流看门狗分开跑：分流可以被暂停（出口没了就让流量退回直连），
// 但这条保护规则必须一直在 —— 它是隧道不把自己吸进隧道的最后一道防线。
//
// 单独一个循环而不是挂在分流看门狗上，原因有两个：
//   - 分流可能被配置关掉，那时看门狗根本不存在，保护规则就没人看了；
//   - 分流被暂停时看门狗不再检查规则完整性，同样没人看。
func watchZTGuard(ctx context.Context, log *slog.Logger, sys *netcfg.System, g *dispatch.ZTGuard) {
	if g == nil || g.Empty() {
		return
	}
	tick := time.NewTicker(ztGuardInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			probs := g.Verify(sys)
			if len(probs) == 0 {
				continue
			}
			log.Warn("ZT 环路保护规则缺失，正在补回",
				logx.Event("guard.zt_rule_healing"),
				logx.FieldLayer, logx.LayerDispatch,
				"problems", problemTexts(probs),
				logx.Hint("通常是 OpenWrt 防火墙重载清空了规则表"))
			if err := g.Install(sys); err != nil {
				log.Error("ZT 环路保护规则补回失败",
					logx.Event("guard.zt_rule_heal_failed"),
					logx.FieldLayer, logx.LayerDispatch,
					logx.FieldErr, err.Error())
			}
		}
	}
}

// problemTexts 把问题列表渲染成可读的一组字符串。
func problemTexts(probs []dispatch.Problem) []string {
	out := make([]string, 0, len(probs))
	for _, p := range probs {
		out = append(out, p.String())
	}
	return out
}
