package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"slx/internal/config"
	"slx/internal/dispatch"
	"slx/internal/envdetect"
	"slx/internal/forward"
	"slx/internal/logx"
	"slx/internal/mesh"
	"slx/internal/netcfg"
	"slx/internal/picker"
	"slx/internal/tun"
)

// dataPlane 是数据面的运行时句柄。
type dataPlane struct {
	plane *forward.Plane
	dev   *tun.Device
	sys   *netcfg.System
	// dispatch 是分流状态（可能为 nil，表示未启用）。
	dispatch *dispatchState
	// ztGuard 是 ZT 环路保护规则。
	//
	// 它的生命周期独立于 dispatch：分流被暂停时它必须留下，
	// 否则出口 agent 连不回网关（死锁）。见 dispatch.ZTGuard。
	ztGuard *dispatch.ZTGuard
	// picker 是选路器（nil 表示未启用自动选路）。
	picker *picker.Selector
	// innerIP 是本节点的隧道内层地址。
	innerIP string
	// cleanup 在退出时撤销本机写入的规则与路由。
	cleanup func()
}

// setupDataPlane 按角色建立数据面。
//
// 顺序很重要：先做环路检查，再建 TUN，最后写规则。
// 检查不通过必须直接拒绝启动 —— 带着环路隐患跑起来比不跑更糟，
// 因为它会把用户的流量吸进一个出不来的洞。
func setupDataPlane(
	ctx context.Context,
	log *slog.Logger,
	c *config.Config,
	env *envdetect.Env,
	nodeID string,
	disc *mesh.Discovery,
) (*dataPlane, error) {

	innerCIDR := c.EffInnerCIDR()
	tunName := c.EffTunName()

	// —— 1. 决定出口 ——
	//
	// 只把「用户显式配置的出口」当作固定出口。自动选路交给 picker：
	// 启动瞬间其实还不知道谁在线、谁快，这时候挑一个写死会把手动选路
	// 变成事实上的静态绑定，后面评分再准也换不掉。
	fixedEgress := c.Egress
	egressID := fixedEgress

	// —— 2. 环路检查（必须在建 TUN 之前）——
	ztNets := ztNetworks(env)
	sys := netcfg.NewSystem(nil)
	problems, err := sys.CheckLoopRisk(netcfg.GuardSpec{
		TunnelDev: tunName,
		InnerCIDR: innerCIDR,
		ZTSubnets: ztNets,
	})
	if err != nil {
		log.Warn("环路检查未能执行",
			logx.Event("guard.check_failed"),
			logx.FieldErr, err.Error(),
			logx.Hint("通常是 ip 命令不可用；请确认 iproute 已安装"))
	}
	for _, p := range problems {
		if p.Fatal {
			return nil, fmt.Errorf("检测到路由环路风险，拒绝启动:\n%s", p.String())
		}
		log.Warn("发现可疑配置", logx.Event("guard.warn"), "detail", p.What, "hint", p.Hint)
	}

	// —— 3. 建 TUN ——
	dev, err := tun.Open(tunName, tun.DefaultMTU)
	if err != nil {
		return nil, err
	}

	isGateway := egressID != ""

	innerIP, err := forward.NodeInnerIP(innerCIDR, nodeID)
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	ones, err := forward.PrefixFor(innerCIDR)
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("内层网段 %q 非法: %w", innerCIDR, err)
	}
	cidr := fmt.Sprintf("%s/%d", innerIP, ones)

	// 刻意不给隧道配 IPv4 地址（只用 ConfigureNoAddr）。
	//
	// 实测踩到的坑：ZeroTier 会绑定到本机所有「带地址」的接口，
	// 并把 slx0 的地址当作自己对外发包的源地址。结果是 ZT 自己的 P2P
	// 流量在隧道里来回跑，把带宽吃掉、还形成难以定位的环路。
	// 不给地址，ZT 就不会认领它；内层路由改用下面这行显式建立。
	if err := dev.ConfigureNoAddr(); err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("配置 TUN 失败: %w", err)
	}
	log.Info("TUN 已就绪",
		logx.Event("tun.ready"),
		"dev", dev.Name(), "inner", cidr, "mtu", dev.MTU(),
		"detail", "隧道接口不配 IP 地址（避免 ZeroTier 认领）；内层路由显式建立")

	// 收包网卡自己的转发开关也必须打开。
	// 只开全局 ip_forward 不够：内核在转发路径上还会检查
	// conf.<iface>.forwarding，而新接口继承的是 default.forwarding（可能是 0）。
	// 这个漏掉时的现象是「隧道包进来了、抓包能看到、就是不转发」，极难定位。
	if err := sys.EnableInterfaceForwarding(dev.Name()); err != nil {
		log.Warn("打开隧道网卡转发开关失败",
			logx.Event("netcfg.if_forwarding_failed"),
			logx.FieldErr, err.Error(),
			logx.Hint("该开关未开启时，从隧道进来的包不会被内核转发"))
	}

	// 反向路径过滤：隧道回程包必须能通过，否则表现为「能发不能收」。
	// 注意这里必须把 all 归零，设成宽松模式(2)也不够 —— 详见方法注释。
	prevRPF, rpfChanged, rpfErr := sys.AllowAsymmetricReturnPath(dev.Name())
	if rpfErr != nil {
		log.Warn("调整反向路径过滤失败",
			logx.Event("netcfg.rp_filter_failed"),
			logx.FieldErr, rpfErr.Error(),
			logx.Hint("隧道回程包可能被内核丢弃（表现为能发不能收、抓包却能看到包进来）"))
	} else if rpfChanged {
		log.Info("已允许隧道非对称回程",
			logx.Event("netcfg.rp_filter_relaxed"),
			"dev", dev.Name(), "prev_all", prevRPF,
			"detail", "隧道回程包源地址是任意公网地址，反向路由不在隧道上，必须放开反向路径过滤")
	}

	// 内层地址挂到 loopback 上（/32），而不是配在隧道网卡上。
	//
	// 为什么是 lo：
	//   · ZeroTier 只绑定「非 loopback」接口，所以它不会认领这个地址
	//     —— 这解决了 M2 那个「ZT 把隧道地址当源地址、把 P2P 流量灌进隧道」的问题；
	//   · 内核把它视为「本地地址」，于是 SNAT 的源地址是合法的本地地址，
	//     回程包在路由阶段也不会被当成非法目的地址丢掉
	//     （不挂 lo 时 10.254.x 不属于任何接口，实测回程包在路由阶段被丢弃，
	//      InAddrErrors 增长、既不进 FORWARD 也不进 INPUT）。
	if _, err := sys.EnsureAddrOnLoopback(innerIP); err != nil {
		log.Warn("把内层地址挂到 loopback 失败",
			logx.Event("netcfg.lo_addr_failed"),
			logx.FieldErr, err.Error(),
			logx.Hint("这会导致隧道回程包在路由阶段被丢弃"))
	}

	// 内层网段的显式路由：出口靠它把回程包（目的地址是网关内层 IP）送回隧道。
	// 地址挂在 lo 上不会自动生成 10.254.0.0/16 的 connected 路由，仍需显式加。
	if _, err := sys.EnsureRoute(netcfg.RouteSpec{Dst: innerCIDR, Dev: dev.Name()}); err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("建立内层路由失败: %w", err)
	}

	dp := &dataPlane{dev: dev, sys: sys, innerIP: innerIP}

	// —— 4. 写入规则 ——
	//
	// 两类规则都无条件装上，不做角色判断。原因：
	//   · 网关 SNAT 规则是「-o slx0」匹配，只有真的要经隧道出去的包才会命中；
	//     纯出口节点上永远没有这种包，装了也无副作用。
	//   · 出口 SNAT 规则是「-s 内层网段」匹配，只有隧道来的包才会命中；
	//     纯网关节点上也不会有。
	// 而角色在启动瞬间其实是未知的（出口地址要等对端被发现后才知道），
	// 早先按 egressID 判断 isGateway 导致网关 SNAT 规则被漏掉，
	// 现象是「包能到出口但回不来」。宁可多装一条不生效的规则。
	if err := setupGatewayNetcfg(log, sys, env, dev.Name(), innerIP, innerCIDR); err != nil {
		_ = dev.Close()
		return nil, err
	}
	if err := setupEgressNetcfg(log, sys, env, dev.Name(), innerCIDR); err != nil {
		_ = dev.Close()
		return nil, err
	}

	// —— 5. 组装数据面 ——
	//
	// 用户态 NAT 表：网关用它做地址改写与还原，不依赖内核 conntrack。
	nat, err := forward.NewNAT(innerIP)
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("初始化地址转换表失败: %w", err)
	}
	// 选路器：评分权重用默认值（带宽 0.45 / 延迟 0.35 / 丢包 0.20）。
	pick := picker.NewSelector(picker.DefaultWeights())
	dp.picker = pick

	plane, err := forward.New(forward.Options{
		Dev:         dev,
		Discovery:   disc,
		Log:         log,
		InnerCIDR:   innerCIDR,
		EgressID:    egressID,
		FixedEgress: fixedEgress,
		SelfID:      nodeID,
		NAT:         nat,
		Picker:      pick,
	})
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	dp.plane = plane

	// —— 6. 准备分流 ——
	//
	// 只做准备（取国内网段、算接口），不立即安装规则：
	// 规则由看门狗在确认有可用出口后才装。先装规则再等出口，
	// 会让用户在这段时间里境外网站全超时。
	ds, derr := setupDispatch(ctx, log, c, env, innerCIDR, dev.Name())
	if derr != nil {
		log.Warn("分流未启用，继续以不分流模式运行",
			logx.Event("dispatch.prepare_failed"),
			logx.FieldLayer, logx.LayerDispatch,
			logx.FieldErr, derr.Error(),
			logx.Hint("数据面本身仍然可用；只是不会自动接管境外流量"))
	}
	if ds != nil {
		ds.refresh = make(chan struct{}, 1)
	}
	dp.dispatch = ds

	// —— 7. ZT 环路保护 ——
	//
	// 装在数据面这一层，而不是分流那一层：它的生命周期独立于
	// 「分流启用 / 暂停」，即使分流被撤掉它也必须留着，
	// 否则 ZT 流量会被吸进隧道，出口 agent 再也连不回网关（死锁）。
	//
	// 装在分流规则之前 —— 分流规则由看门狗稍后才装，那时保护规则已经在位。
	dp.ztGuard = setupZTGuard(log, sys, env)

	if isGateway {
		log.Info("数据面就绪（网关）",
			logx.Event("plane.ready"), "role", "gateway",
			logx.FieldEgress, egressID, "inner", cidr)
	} else {
		log.Info("数据面就绪（出口）",
			logx.Event("plane.ready"), "role", "egress", "inner", cidr,
			logx.Hint("等待网关发来流量；首次收到会登记网关地址"))
	}

	dp.cleanup = func() {
		// 分流规则先撤（顺序上应在隧道设备消失之前）
		dp.dispatch.teardown()
		// ZT 环路保护规则最后撤：它跟分流无关，只跟进程有关。
		// 进程退出时隧道也要没了，留着它没有意义。
		dp.ztGuard.Remove(sys)
		// 只清理我们自己加的规则，用户原有规则不动
		sys.RemoveSNAT(netcfg.SNATSpec{OutIface: dev.Name(), ToSource: innerIP, Comment: "gateway-snat"})
		if env.DefaultIface != "" {
			sys.RemoveMasqueradeByMark(env.DefaultIface, "egress-snat")
		}
		sys.RemoveTunnelMark(dev.Name())
		_ = sys.RemoveRoute(netcfg.RouteSpec{Dst: innerCIDR, Dev: dev.Name()})
		sys.RemoveAddrFromLoopback(innerIP)
		sys.RestoreAllRPFilter(prevRPF)
		sys.RemoveForwardAccept(dev.Name(), false)
		sys.RemoveForwardAccept(dev.Name(), true)
		sys.RemoveMSSClamp(dev.Name(), false)
		sys.RemoveMSSClamp(dev.Name(), true)
		_ = dev.Close()
	}
	return dp, nil
}

// setupGatewayNetcfg 安装网关侧规则。
func setupGatewayNetcfg(log *slog.Logger, sys *netcfg.System, env *envdetect.Env,
	dev, devAddr, innerCIDR string) error {

	// 网关侧**不再安装内核 SNAT 规则**。
	//
	// 源地址改写由用户态 NAT 完成（见 internal/forward/nat.go）。
	// 曾经用 `-o slx0 -j SNAT --to-source <内层地址>` + conntrack 自动反 NAT，
	// 但回程包会在 netfilter 的 raw/mangle 之后消失，两台不同发行版的 VPS
	// 与 WSL 上都能复现，且手工构造同样字节的包却能正常转发 —— 属于
	// 运行时规则状态与内核 NAT 的交互问题，规避成本远高于自己接管。
	//
	// 不装这条规则还有个额外好处：不会为隧道路径产生 conntrack NAT 记录，
	// 也就不会撞上某些系统上 `-m conntrack --ctstate INVALID -j DROP` 的坑。
	_ = devAddr

	// MSS 钳制：内层 MTU 1380 小于链路 MTU，不钳制会导致大包被丢、
	// 表现为 TLS 握手卡住（极易被误判成被墙）。
	if changed, err := sys.EnsureMSSClamp(dev, true); err != nil {
		log.Warn("MSS 钳制失败", logx.Event("netcfg.mss_fail"), logx.FieldErr, err.Error(),
			logx.Hint("不做钳制时大包会被丢弃，表现为网页一直加载但不出内容"))
	} else if changed {
		log.Info("已设置 MSS 钳制", logx.Event("netcfg.mss"), "dev", dev)
	}

	// 网关需要转发（内网 → 隧道，隧道 → 内网）
	if env.DefaultIface != "" {
		if _, err := sys.EnsureForwardAccept(dev, false); err != nil {
			log.Warn("放行 FORWARD 失败", logx.Event("netcfg.forward_fail"),
				logx.FieldErr, err.Error())
		}
		if _, err := sys.EnsureForwardAccept(dev, true); err != nil {
			log.Warn("放行 FORWARD 失败", logx.Event("netcfg.forward_fail"),
				logx.FieldErr, err.Error())
		}
	}
	return nil
}

// setupEgressNetcfg 安装出口侧规则。
func setupEgressNetcfg(log *slog.Logger, sys *netcfg.System, env *envdetect.Env,
	dev, innerCIDR string) error {

	changed, err := sys.EnsureIPForward()
	if err != nil {
		return fmt.Errorf("开启 IP 转发失败: %w", err)
	}
	if changed {
		log.Info("已开启 IP 转发", logx.Event("netcfg.ip_forward"))
	}

	wan := env.DefaultIface
	if wan == "" {
		log.Warn("识别不到默认路由接口，出口 SNAT 规则跳过",
			logx.Event("netcfg.no_wan"),
			logx.Hint("出口节点必须能上网；检查 ip route show default"))
		return nil
	}

	// 只 NAT「从隧道进来」的包：先打 mark，再按 mark 匹配。
	//
	// 不能在 POSTROUTING 里用 -i 限定来源接口 —— iptables 直接拒绝
	// （Can't use -i with POSTROUTING，实测踩过）。
	// 这也是第四道环路防护：即使有其它程序用同样的内层源地址发包，
	// 只要不是从隧道进来的就不会被打 mark，也就不会被误 NAT。
	if _, err := sys.EnsureTunnelMark(dev); err != nil {
		return fmt.Errorf("设置隧道 mark 失败: %w", err)
	}
	changed, err = sys.EnsureMasqueradeByMark(wan, "egress-snat")
	if err != nil {
		return fmt.Errorf("出口 SNAT 规则失败: %w", err)
	}
	if changed {
		log.Info("已写入出口 SNAT 规则",
			logx.Event("netcfg.egress_snat"),
			"source", innerCIDR, "out", wan,
			"detail", "内层地址在此改写为公网地址")
	}

	// MSS 钳制：出口侧也要做，否则从隧道进来再出去的大包同样会被丢。
	if _, err := sys.EnsureMSSClamp(dev, false); err != nil {
		log.Warn("MSS 钳制失败", logx.Event("netcfg.mss_fail"), logx.FieldErr, err.Error())
	}

	// 允许 TUN ↔ WAN 之间转发。
	// 很多发行版默认 FORWARD 是 DROP，不放行的现象是"连上了但完全没流量"。
	for _, outgoing := range []bool{false, true} {
		if _, err := sys.EnsureForwardAccept(dev, outgoing); err != nil {
			log.Warn("放行 FORWARD 失败", logx.Event("netcfg.forward_fail"),
				logx.FieldErr, err.Error(),
				logx.Hint("若网卡间转发被防火墙拦截，表现为完全无法上网"))
		}
	}
	return nil
}

// pickEgress 从候选池里挑一个出口（M2 阶段：取第一个）。
//
// M4 会替换成基于实测评分的选路器。
func pickEgress(disc *mesh.Discovery, selfID string) string {
	t := disc.Table()
	cands := t.EgressCandidates(time.Now())
	for _, p := range cands {
		if p.NodeID == selfID {
			continue
		}
		return p.NodeID
	}
	return ""
}

// ztNetworks 把 ZT 接口的 CIDR 转成网络地址形式（去掉主机位）。
func ztNetworks(env *envdetect.Env) []string {
	var out []string
	for _, z := range env.ZTIfaces {
		if _, ipnet, err := net.ParseCIDR(z.CIDR); err == nil {
			out = append(out, ipnet.String())
		}
	}
	return out
}

// summarizeStats 把计数渲染成一行，便于日志与 CLI 展示。
func summarizeStats(s forward.Snapshot) string {
	return fmt.Sprintf("收 %d 包 / 发 %d 包，错误 %d，无路由 %d",
		s.TunRead, s.TunWrite, s.SendErr, s.NoRoute)
}

// isDockerBridge 等辅助函数在 envdetect 里已有；这里只保留字符串清理。
