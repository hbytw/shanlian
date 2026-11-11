package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"slx/internal/cnlist"
	"slx/internal/config"
	"slx/internal/dispatch"
	"slx/internal/envdetect"
	"slx/internal/ipgeo"
	"slx/internal/logx"
	"slx/internal/netcfg"
)

// 分流看门狗的检查周期。
//
// 10 秒是个折中：OpenWrt 防火墙重载后 10 秒内恢复，用户基本感知不到
// （重载瞬间已经在跑的连接由 conntrack 保住，只有新连接会短暂走错）。
// 再快会白白消耗低端路由器的 CPU。
const dispatchWatchInterval = 10 * time.Second

// dispatchState 持有分流配置与看门狗所需的上下文。
type dispatchState struct {
	sys *netcfg.System
	spc dispatch.Spec
	st  *dispatch.State
	cn  *cnlist.Result

	// enabled 表示当前分流是否在生效。
	enabled bool
	// refresh 用于「立刻自查一次」：防火墙重载后由钩子触发，
	// 把恢复时间从看门狗的 10 秒降到毫秒级。
	refresh chan struct{}
	// lastProblemCount 用于抑制重复告警。
	lastProblemCount int

	// forwardPresent 记录「启动时哪些 FORWARD 放行规则本来就在」。
	//
	// 用途：看门狗只补回**本来就有**的规则，不新增本来就没有的 ——
	// 于是不需要知道当前是 packet 模式还是 proxy 模式，
	// 也不会顺手把内网网卡之间的转发也放行（那是安全策略的变化，
	// 不该由看门狗顺手做）。
	forwardPresent map[string]map[bool]bool
}

// cleanupStaleRoutes 清理上一轮残留的、指向**已不存在设备**的路由。
//
// # 判据（必须保守）
//
// 只删「设备已经不在了」的路由 —— 那一定是上一轮进程的残留
// （设备随进程消失，路由却留下来了）。
// 活路由一条都不动，也不动别的表。
//
// # 什么时候跑
//
// **只在启动时跑一次**（不是看门狗、不是运行期）。
// 运行期做任何「检测到异常就清理/重建」的动作都可能引发连锁反应
// —— 上线纪律：宁可不动，也不要误触发。
func cleanupStaleRoutes(log *slog.Logger, sys *netcfg.System) {
	if sys == nil {
		return
	}
	routes, err := sys.TableRoutes(dispatch.RouteTable)
	if err != nil {
		return
	}
	removed := 0
	for _, r := range routes {
		dev := netcfg.RouteDevice(r)
		if dev == "" || netcfg.DeviceExists(dev) {
			continue
		}
		if sys.RemoveTableRoute(dispatch.RouteTable, "", dev) {
			removed++
			log.Info("清理上一轮残留的失效路由",
				logx.FieldEvent, "dispatch.stale_route_removed",
				"table", dispatch.RouteTable,
				"dev", dev,
				logx.Hint("上一轮进程被强杀，路由残留而网卡已消失；不清理会让境外流量被黑洞"))
		}
	}
	if removed == 0 {
		return
	}
	// 表里已经空了 ⇒ 指向它的 fwmark 规则也会把包丢进黑洞 ⇒ 一并撤掉。
	// （这条 ip rule 是我们自己装的，规格固定，撤掉最坏结果是分流不生效。）
	if sys.IPRuleExists(dispatch.RulePriorityMark, "fwmark 0x100", strconv.Itoa(dispatch.RouteTable)) {
		sys.RemoveIPRule(dispatch.RulePriorityMark, "fwmark 0x100", strconv.Itoa(dispatch.RouteTable))
		log.Info("撤掉指向空路由表的分流规则",
			logx.FieldEvent, "dispatch.stale_rule_removed",
			"priority", dispatch.RulePriorityMark)
	}
}

// setupDispatch 准备分流：取国内网段数据，算出要做分流的接口。
//
// 这里只做准备，不立即安装规则 —— 规则要等确认有可用出口后再装，
// 否则会把用户流量送进一个还没有出口的隧道里（表现为境外网站全挂）。
func setupDispatch(ctx context.Context, log *slog.Logger, c *config.Config, env *envdetect.Env,
	innerCIDR, tunDev string) (*dispatchState, error) {

	if !c.EffDispatch() {
		log.Info("分流已按配置关闭", logx.Event("dispatch.disabled"),
			logx.Hint("需要在配置里 dispatch: true 才能接管境外流量"))
		return nil, nil
	}

	// —— 做分流的接口 ——
	ifaces := dispatch.EnsureLANIfaces(c.DispatchIfaces)
	if len(ifaces) == 0 {
		// 自动识别：优先用内网侧接口
		if env.LANIface != "" {
			ifaces = []string{env.LANIface}
		}
	}
	if len(ifaces) == 0 {
		log.Warn("找不到可用于分流的接口，跳过分流",
			logx.Event("dispatch.no_iface"),
			logx.Hint("请在配置里指定 dispatch_ifaces: [\"br-lan\"]（内网侧接口名）"))
		return nil, nil
	}

	// —— 国内网段数据 ——
	sources := cnlist.DefaultSources
	if len(c.CNListURLs) > 0 {
		sources = make([]cnlist.Source, 0, len(c.CNListURLs))
		for i, u := range c.CNListURLs {
			sources = append(sources, cnlist.Source{
				Name: fmt.Sprintf("自定义源%d", i+1), URL: u, Format: cnlist.FormatCIDR})
		}
	}

	log.Info("正在获取中国大陆网段", logx.Event("dispatch.cnlist_loading"),
		"cache", c.CNListURLs)

	res, err := loadCNList(sources, log)
	if err != nil {
		log.Error("获取中国大陆网段失败，不启用分流",
			logx.Event("dispatch.cnlist_failed"),
			logx.FieldLayer, logx.LayerDispatch,
			logx.FieldErr, err.Error(),
			logx.Hint("分流依赖这份数据；取不到时把境内流量也送进隧道会更糟，所以选择不接管"))
		return nil, err
	}
	log.Info("中国大陆网段已就绪", logx.Event("dispatch.cnlist_ready"),
		"summary", res.Summary())

	// 交叉校验（国内网段表 ↔ ip2region 归属地库）**不在启动路径上做**。
	//
	// 线上事故（网关角色跑在 OpenWrt aarch64 小内存路由器上）：
	// 这一步要枚举 ip2region xdb 里约 55 万个网段并做区间运算，
	// 峰值内存上百 MB、CPU 跑满，把 ZeroTier 守护进程饿死
	//（ARP 应答超时、keepalive 超时），20 秒内所有出口 agent 集体掉线。
	// 日志里 audit 自己只用了 1.6 秒就返回，但它之后的 25 秒里
	// relay 完全不响应 keepalive —— 真正的问题不是那 1.6 秒，
	// 而是它引发的内存/CPU 余波。
	//
	// 所以结论是：**光异步还不够**。异步只解决「启动不等它」，
	// 峰值照样发生，照样会把 ZT 饿死。
	//
	// 审计是**诊断性**能力：它回答「两份数据源是不是在互相打架」，
	// 不回答「分流现在能不能用」。分流能不能用由另外几道关卡保证
	//（网段条数不够时 dispatch.Setup 拒绝启用；数据取不到时不接管流量）。
	// 所以它属于「发现问题」而不是「防止问题」——
	// 发现问题完全可以由人手动做：slx audit-cn。
	//
	// 现在它的位置是：
	//   - 默认不跑（要看随时 slx audit-cn，不影响运行中的服务）；
	//   - 显式打开（SLX_AUDIT_CN=1）时，在独立 goroutine 里异步执行，
	//     启动路径绝不等待它。
	startBackgroundAudit(ctx, res, log)

	ztNets := ztNetworks(env)
	spc := dispatch.Spec{
		TunnelDev: tunDev,
		InnerCIDR: innerCIDR,
		ZTSubnets: ztNets,
		LANIfaces: ifaces,
		CNCIDRs:   res.CIDRs,
	}

	// 装之前先做环路检查：宁可拒绝启动，也不要带着环路跑
	probs, _ := netcfg.NewSystem(nil).CheckLoopRisk(netcfg.GuardSpec{
		TunnelDev: tunDev, InnerCIDR: innerCIDR, ZTSubnets: ztNets,
	})
	for _, p := range probs {
		if p.Fatal {
			return nil, fmt.Errorf("分流前环路检查未通过:\n%s", p.String())
		}
	}

	sys := netcfg.NewSystem(nil)

	// —— 清理上一轮的残留（只在启动时做一次）——
	//
	// 场景：进程被 kill -9 / OOM / 掉电时，cleanup 没跑 ⇒
	// ip rule 1000（fwmark → table 100）与 table 100 里的
	// "default dev slx0" 残留下来，而 slx0 已随进程消失
	// ⇒ 被标记 0x100 的包被内核丢掉 ⇒ **境外流量被黑洞**
	// （不是回退直连），而 procd 一小时只重启 5 次 ⇒ 黑洞状态被固化。
	//
	// 判据必须保守：**只删指向「已经不存在」的设备的路由**，
	// 一条活路由都不动；也不碰任何别的表、别的规则。
	// 运行期绝不做这种事 —— 这里只在启动时跑一次。
	cleanupStaleRoutes(log, sys)

	// 快照「启动时哪些 FORWARD 放行规则在位」。
	//
	// 为什么要快照：OpenWrt 的防火墙一重载（fw3 reload / UCI 提交 /
	// 装新服务）就会把整张规则表清空重建，我们插进去的 FORWARD 放行规则
	// 随之消失 —— 而 ipset、mangle 打标、ip rule、路由表都还在
	// ⇒ **状态页显示一切正常，但境外流量永久断**（审计报告根因 M）。
	//
	// 快照之后，看门狗只补回「本来就有」的那些，不多也不少。
	forwardPresent := map[string]map[bool]bool{}
	snapshotForward(sys, forwardPresent, tunDev)
	for _, iface := range ifaces {
		snapshotForward(sys, forwardPresent, iface)
	}

	return &dispatchState{
		sys:            sys,
		spc:            spc,
		cn:             res,
		forwardPresent: forwardPresent,
	}, nil
}

// snapshotForward 记录某个网卡的 FORWARD 放行规则当前是否存在。
func snapshotForward(sys *netcfg.System, into map[string]map[bool]bool, iface string) {
	if iface == "" {
		return
	}
	m := map[bool]bool{}
	for _, outgoing := range []bool{false, true} {
		m[outgoing] = sys.HasForwardAccept(iface, outgoing)
	}
	into[iface] = m
}

// ensureForwardRules 补回被防火墙重载冲掉的 FORWARD 放行规则。
//
// **只补「本来就有」的**（见 dispatchState.forwardPresent 的说明）：
// 启动时装过的会被补回，没装过的不会被顺手放行。
//
// 这是「状态全绿但网络全断」这个隐藏地雷的解药：
// 以前 FORWARD 规则只在启动时装一次，防火墙一重载就没了，
// 而看门狗只管 ipset/mangle/ip rule ⇒ 永远补不回来。
func (d *dispatchState) ensureForwardRules(log *slog.Logger) {
	if d == nil || d.sys == nil {
		return
	}
	for iface, present := range d.forwardPresent {
		for _, outgoing := range []bool{false, true} {
			if !present[outgoing] {
				continue
			}
			if d.sys.HasForwardAccept(iface, outgoing) {
				continue
			}
			if _, err := d.sys.EnsureForwardAccept(iface, outgoing); err != nil {
				log.Warn("补装 FORWARD 放行规则失败",
					logx.FieldEvent, "dispatch.forward_heal_failed",
					logx.FieldErr, err.Error(),
					logx.FieldHint, "常见原因是防火墙重载后规则未补回；这会让状态页全绿但境外流量不通")
				continue
			}
			log.Info("已补回被防火墙重载清掉的 FORWARD 放行规则",
				logx.FieldEvent, "dispatch.forward_healed",
				"iface", iface,
				"outgoing", outgoing)
		}
	}
}

// watch 是分流看门狗。
//
// 三个职责：
//  1. 出口可用时确保分流规则在位（被防火墙重载清掉就补回来）；
//  2. 出口不可用时**主动撤掉分流**，让流量退回直连 —— 有隧道但走不通，
//     比没有隧道更糟：用户看到的是境外网站全部超时；
//  3. 恢复后自动重新接管。
//
// watch 周期判定并维护分流规则。ready 报告「当前是否有可用出口」，
// 由调用方按数据面模式提供（packet 看 forward.Plane，proxy 看是否有 agent 在线），
// 这样分流逻辑两种模式完全共用，不必复制一份。
func (d *dispatchState) watch(ctx context.Context, log *slog.Logger, ready func() bool) {
	if d == nil {
		return
	}
	tick := time.NewTicker(dispatchWatchInterval)
	defer tick.Stop()

	// 首次检查稍等一会，给发现流程一点时间
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}

	for {
		d.evaluate(log, ready())
		// FORWARD 放行规则不在 dispatch 的 Verify 检查项里
		// （它们装在 filter 表，与 ipset/mangle 无关），
		// 所以这里单独补一次 —— 被防火墙重载冲掉时立即恢复。
		d.ensureForwardRules(log)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-d.refresh:
			// 被显式唤醒（防火墙重载）。立刻重判一次。
			log.Info("收到自查请求，立即检查分流规则",
				logx.Event("dispatch.refresh_requested"))
		}
	}
}

// evaluate 执行一次「是否启用 + 是否健康」的判定。
func (d *dispatchState) evaluate(log *slog.Logger, hasEgress bool) {

	if !hasEgress {
		if d.enabled {
			log.Warn("出口不可用，临时撤下分流规则（流量退回直连）",
				logx.Event("dispatch.suspended"),
				logx.FieldLayer, logx.LayerDispatch,
				logx.Hint("出口恢复后会自动重新接管；期间境外流量走原路"))
			d.st.Teardown(d.sys)
			d.st = nil
			d.enabled = false
		}
		return
	}

	if !d.enabled {
		st, err := dispatch.Setup(d.sys, d.spc)
		if err != nil {
			log.Error("启用分流失败",
				logx.Event("dispatch.setup_failed"),
				logx.FieldLayer, logx.LayerDispatch,
				logx.FieldErr, err.Error())
			return
		}
		d.st = st
		d.enabled = true
		d.lastProblemCount = 0
		log.Info("分流已启用",
			logx.Event("dispatch.enabled"),
			"ifaces", d.spc.LANIfaces,
			"cn_entries", len(d.spc.CNCIDRs),
			"table", dispatch.RouteTable)
		return
	}

	// 已启用 → 校验完整性
	probs := d.st.Verify(d.sys)
	if len(probs) == 0 {
		d.lastProblemCount = 0
		return
	}

	// 规则丢了（典型原因：防火墙重载）→ 重建
	if len(probs) != d.lastProblemCount {
		names := make([]string, 0, len(probs))
		for _, p := range probs {
			names = append(names, p.String())
		}
		log.Warn("分流规则缺失，正在自愈",
			logx.Event("dispatch.healing"),
			logx.FieldLayer, logx.LayerDispatch,
			"problems", names,
			logx.Hint("常见原因是 OpenWrt 防火墙重载清空了规则表；若频繁出现请检查 firewall 集成是否生效"))
	}
	d.lastProblemCount = len(probs)

	st, err := dispatch.Setup(d.sys, d.spc)
	if err != nil {
		log.Error("分流自愈失败",
			logx.Event("dispatch.heal_failed"),
			logx.FieldErr, err.Error())
		return
	}
	d.st = st
}

// teardown 撤销分流（进程退出时调用）。
func (d *dispatchState) teardown() {
	if d == nil || !d.enabled || d.st == nil {
		return
	}
	d.st.Teardown(d.sys)
	d.enabled = false
}

// Summary 返回一行状态摘要。
func (d *dispatchState) Summary() string {
	if d == nil {
		return "未启用"
	}
	if !d.enabled {
		return "已准备但未生效（等待出口）"
	}
	return fmt.Sprintf("已启用，接口 %v，国内网段 %d 条", d.spc.LANIfaces, len(d.spc.CNCIDRs))
}

// auditCNList 把国内网段表和 ip2region 归属地库对拍，有分歧就告警。
//
// 这是可选能力：没放归属地库就什么都不做，校验失败也绝不能拦住分流 ——
// 分流的底线是「有数据就能跑」，校验只是多一层体检。
//
// 注意：这个函数**很重**（枚举约 55 万个网段 + 区间运算），
// 只允许在后台线程里调用，或者由用户手动跑 slx audit-cn。
// 绝不能放在启动路径上（见 setupDispatch 里的说明）。
func auditCNList(res *cnlist.Result, log *slog.Logger) {
	if res == nil {
		return
	}
	path := ipgeo.ResolvePath("")
	if _, err := os.Stat(path); err != nil {
		return // 没放归属地库就不校验
	}
	geo, err := ipgeo.LoadCNRanges(path)
	if err != nil {
		log.Warn("交叉校验跳过：归属地库不可用",
			logx.Event("dispatch.cnlist_audit_skip"),
			logx.FieldLayer, logx.LayerDispatch,
			logx.FieldErr, err.Error(),
			logx.Hint("归属地库是可选能力，跳过不影响分流；要启用就放一份 ip2region_v4.xdb 到 "+path))
		return
	}
	lookup, err := ipgeo.Open(path)
	if err != nil {
		return // 查询器打不开就只报原始差异，不分类
	}
	defer lookup.Close()
	a := ipgeo.AuditCN(res.CIDRs, geo, lookup)
	if a.Clean() {
		log.Info("国内网段表与归属地库一致",
			logx.Event("dispatch.cnlist_audit_ok"),
			"entries", a.ListValid,
			"addresses", a.ListAddresses)
		return
	}
	log.Warn("国内网段表与归属地库有分歧",
		logx.Event("dispatch.cnlist_audit_diverge"),
		logx.FieldLayer, logx.LayerDispatch,
		"false_positive_ranges", len(a.FalsePositiveRanges),
		"false_positive_addresses", a.FalsePositiveAddrs,
		"missing_ranges", len(a.MissingRanges),
		"missing_addresses", a.MissingAddrs,
		logx.Hint("错分=境外被当境内会走直连；漏段=境内被当境外会进隧道。用 slx audit-cn 看细节"))
}

// auditStartDelay 是后台交叉校验的启动延迟。
//
// 不是为了「让它慢慢跑」，而是为了避开启动最脆弱的那段时间：
// ZeroTier 的 ARP 应答与 keepalive 在启动瞬间最容易被拖垮，
// 这时候抢 CPU/内存最容易把隧道自己饿死。
//
// 做成 var 而不是 const：测试要把它调成 0，才能验证「后台执行」这件事。
var auditStartDelay = 30 * time.Second

// auditEnvVar 是开启后台交叉校验的环境变量。
//
// 用环境变量而不是配置项：项目的配置哲学是「配置文件只有 2 项，
// 其余可调项走环境变量」（见 internal/config 的包注释）。
const auditEnvVar = "SLX_AUDIT_CN"

// startBackgroundAudit 在后台线程里做「国内网段表 ↔ 归属地库」交叉校验。
//
// # 为什么默认不做
//
// 校验要枚举 ip2region xdb 的全部约 55 万个网段并做区间运算：
// 峰值内存可达上百 MB，CPU 也要跑满一小会儿。在小内存路由器上，
// 这足以把 ZeroTier 守护进程饿死（ARP 应答超时 → keepalive 超时 →
// 出口 agent 集体掉线）。**异步并不能消除这个峰值**，
// 只是把「启动被卡住」换成「后台把隧道饿死」——后者更隐蔽。
//
// # 为什么可以不做
//
// 校验只是**体检**：它回答「两份数据源是不是在互相打架」，
// 不回答「分流现在能不能用」。分流能不能用由另外几道关卡保证：
// 网段条数不够时 dispatch.Setup 直接拒绝启用，
// 数据取不到时干脆不接管流量。所以校验属于「发现问题」而不是「防止问题」，
// 而发现问题完全可以由人手动做：slx audit-cn。
//
// # 打开方式
//
// 环境变量 SLX_AUDIT_CN=1。打开后：
//
//   - 校验在**独立 goroutine** 里跑，启动路径绝不等待它；
//   - 延后 auditStartDelay 才动手，避开启动最脆弱的时段；
//   - 结果照旧以 dispatch.cnlist_audit_ok / dispatch.cnlist_audit_diverge
//     事件打进日志（与手动跑 slx audit-cn 的结论口径一致）。
//
// # 线程安全
//
// slog.Logger 是并发安全的（标准库保证 Logger 的方法可被并发调用，
// 底层的 JSON/Text handler 内部有互斥锁），所以这里把启动用的 logger
// 直接交给后台 goroutine 即可，不需要额外加锁。
func startBackgroundAudit(ctx context.Context, res *cnlist.Result, log *slog.Logger) {
	if res == nil || log == nil || !auditEnabled() {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(auditStartDelay):
		}
		log.Info("开始后台交叉校验国内网段表",
			logx.Event("dispatch.cnlist_audit_started"),
			logx.FieldLayer, logx.LayerDispatch,
			"entries", len(res.CIDRs),
			logx.Hint("校验在后台进行，不影响分流；结果稍后以 dispatch.cnlist_audit_* 事件输出"))
		auditCNList(res, log)
	}()
}

// auditEnabled 报告是否要做后台交叉校验。
func auditEnabled() bool {
	v := strings.TrimSpace(os.Getenv(auditEnvVar))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
