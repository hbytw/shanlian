package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"slx/internal/buildinfo"
	"slx/internal/config"
	"slx/internal/envdetect"
	"slx/internal/forward"
	"slx/internal/identity"
	"slx/internal/logx"
	"slx/internal/mesh"
)

// saveInterval 是状态缓存的落盘周期。
//
// 缓存在 /tmp（tmpfs），写它不磨损闪存，所以可以写得勤一些，
// 让 slx peers 的快照足够新鲜。
const saveInterval = 3 * time.Second

// loadConfig 是 config.Load 的薄封装，统一路径处理。
func loadConfig(path string) (*config.Config, config.LoadResult, error) {
	if path == "" {
		path = config.DefaultPath
	}
	return config.Load(path)
}

// Run 启动守护进程。
func Run(args []string) int {
	cfgPath := configPath(args)
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run", "--check":
			dryRun = true
		}
	}

	c, res, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 1
	}

	log := logx.New(logx.Options{Level: c.EffLogLevel(), Format: c.EffLogFormat()})
	env := envdetect.Detect()

	nodeID, err := identity.DeriveNodeID(c.KeyBytes())
	if err != nil {
		log.Error("节点身份派生失败",
			logx.Event("identity.derive_failed"),
			logx.FieldErr, err.Error(),
			logx.Hint("确认 /etc/machine-id 或至少一块物理网卡存在"))
		return 1
	}

	log.Info("slx 启动",
		logx.Event("startup"),
		"version", buildinfo.String(),
		logx.FieldNodeID, nodeID,
		"config", c.Path,
		logx.FieldLayer, env.Summary())

	if res.Created {
		log.Info("首次运行，已生成配置文件",
			logx.Event("config.created"), "path", c.Path,
			logx.Hint("把 network_key 复制到其它节点: slx config --key"))
	}
	if res.GeneratedKey != "" {
		log.Warn("已生成新的网络密钥",
			logx.Event("config.new_key"),
			"network_key", res.GeneratedKey,
			logx.Hint("这个密钥需要分发到所有节点；后续不再重复打印"))
	}
	if res.MigratedFrom != 0 {
		log.Info("配置已迁移", logx.Event("config.migrated"),
			"from", res.MigratedFrom, "to", c.SchemaVersion)
	}

	roles := detectRoles(env)
	log.Info("能力判定", logx.Event("roles"), "roles", roles, "dry_run", dryRun)

	if len(env.ZTIfaces) == 0 {
		// proxy 模式不依赖 ZeroTier：出口 agent 主动拨进来，本身就不需要 ZT 通路。
		// 这是新架构相对 packet 模式最实质的解放 —— 家里没有 ZT、或 ZT 挂了也能用。
		if c.EffTransportMode() == config.ModeProxy {
			log.Warn("没有 ZeroTier 接口，proxy 模式仍可工作",
				logx.Event("discovery.no_zt"),
				logx.FieldLayer, logx.LayerMesh,
				logx.Hint("出口由 agent 直连提供，不需要 ZT；若要看到其它节点拓扑再装 ZT"))
		} else {
			log.Error("没有 ZeroTier 接口，无法进行节点发现",
				logx.Event("discovery.no_zt"),
				logx.FieldLayer, logx.LayerMesh,
				logx.Hint("先确认 zerotier-one 已运行并加入网络: zerotier-cli listnetworks 应显示 OK"))
			return 1
		}
	}

	// —— 数据面引用用原子指针持有 ——
	// 发现模块需要回调数据面，而数据面又依赖发现结果（出口地址），
	// 用一个原子指针打破这个循环，避免复杂的初始化顺序约束。
	var planeRef atomic.Pointer[forward.Plane]

	disc, err := mesh.New(mesh.Options{
		NodeID:       nodeID,
		Port:         c.ListenPort,
		ZTIfaces:     env.ZTIfaces,
		Capabilities: roles,
		Transports:   []string{"zt", "direct"},
		Log:          log,
		OnData: func(pkt []byte, from *net.UDPAddr) {
			if p := planeRef.Load(); p != nil {
				p.OnTunnelData(pkt, from)
			}
		},
	})
	if err != nil {
		log.Error("建立发现通道失败",
			logx.Event("discovery.init_failed"),
			logx.FieldLayer, logx.LayerMesh,
			logx.FieldErr, err.Error())
		return 1
	}

	pub := c.PublicAddr
	source := "配置"
	if pub == "" {
		pub = envdetect.PublicIPv4()
		source = "自动识别"
	}
	if pub != "" {
		disc.SetAdvertiseAddr(pub)
		log.Info("已通告直连地址", logx.Event("discovery.advertise"),
			"addr", pub+":"+itoa(disc.Port()), "source", source)
	} else {
		log.Info("未找到公网地址，仅经 ZeroTier 可达",
			logx.Event("discovery.zt_only"),
			logx.FieldLayer, logx.LayerMesh,
			logx.Hint("若本机在 provider NAT 后面（Oracle/腾讯等），请配置 public_addr: <公网IP>"))
	}

	table := disc.Table()
	if n := table.LoadState(mesh.DefaultStatePath, nodeID, mesh.StateMaxAge); n > 0 {
		log.Info("从缓存恢复拓扑", logx.Event("state.restored"), "peers", n,
			logx.Hint("这些节点先标为「迟滞」，收到真实消息后才转为在线"))
	}

	if dryRun {
		fmt.Printf("\n[dry-run] 节点 ID = %s\n", nodeID)
		fmt.Printf("[dry-run] 能力 = %v\n", roles)
		fmt.Printf("[dry-run] 发现端口 = %d，ZT 接口 = %d 个\n", disc.Port(), len(env.ZTIfaces))
		fmt.Printf("[dry-run] 内层网段 = %s，隧道设备 = %s\n", c.EffInnerCIDR(), c.EffTunName())
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return serve(ctx, log, c, env, nodeID, roles, disc, &planeRef)
}

// serve 是主循环：跑发现、等出口、起数据面、周期落盘。
func serve(ctx context.Context, log *slog.Logger, c *config.Config, env *envdetect.Env,
	nodeID string, roles []string, disc *mesh.Discovery, planeRef *atomic.Pointer[forward.Plane]) int {

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	startedAt := time.Now()

	// proxy 模式走独立的主循环：数据面由 gvisor + 反向 agent 组成，
	// 与 packet 模式的 forward.Plane 完全不同（见 DESIGN.md §18）。
	if c.EffTransportMode() == config.ModeProxy {
		return serveProxy(ctx, log, c, env, nodeID, roles, disc, startedAt)
	}

	// pid 文件：供 `slx notify` 找到我们
	if err := writePIDFile(); err != nil {
		log.Debug("pid 文件写入失败", logx.Event("pid.write_failed"), logx.FieldErr, err.Error())
	}
	defer removePIDFile()

	// SIGUSR1 = 「立刻自查一次」。防火墙重载后由 hooks 触发。
	refreshCh := make(chan os.Signal, 1)
	signal.Notify(refreshCh, refreshSignal())
	defer signal.Stop(refreshCh)

	discErr := make(chan error, 1)
	go func() { discErr <- disc.Run(runCtx) }()

	save := time.NewTicker(saveInterval)
	defer save.Stop()

	// 数据面计数周期输出：只在有活动时打印，避免空转刷屏。
	// 这几个计数器直接回答「包走到哪一步被丢了」：
	//   TunRead  本机从 TUN 读到的包（说明内核确实把包交过来了）
	//   Sent     成功封帧发往对端的包
	//   NoRoute  找不到出口/回程网关
	//   SendErr  封帧或发送失败
	//   TunWrite 从隧道收到并写回 TUN 的包
	statsTick := time.NewTicker(5 * time.Second)
	defer statsTick.Stop()
	var lastStats forward.Snapshot

	saveState := func() {
		if err := disc.Table().SaveState(mesh.DefaultStatePath, nodeID); err != nil {
			log.Debug("状态缓存写入失败",
				logx.Event("state.save_failed"), logx.FieldErr, err.Error())
		}
	}

	log.Info("进入运行态", logx.Event("runtime.ready"),
		logx.FieldNodeID, nodeID, "port", disc.Port())

	// —— 起数据面 ——
	//
	// 不等待「发现到出口」再建 TUN：TUN 与规则建立是本地操作，
	// 与对端是否在线无关。出口地址由数据面每包现查，
	// 这样即使对端晚几秒上线，数据面也已经就绪，不会出现
	// 「等了 20 秒才建 TUN」这种别扭的启动行为。
	dp, planeErr := setupDataPlane(runCtx, log, c, env, nodeID, disc)

	// 性能剖析：独立于数据面 ✅✅
	//
	// 为什么放在这里 ✅✅：数据面是本项目的性能瓶颈 ✅⚠️（用户态协议栈 ✅✅），
	// 优化它就必须能剖析「数据面跑起来之后」的状态 ✅✅ —— 但数据面启动本身
	// 也可能失败/降级 ✅✅，那时恰恰最需要看剖析数据 ✅✅。
	// 所以它既不能依赖 startAPI ✅✅（那个在 if dp != nil 里面 ✅✅），
	// 也不能依赖数据面 ✅✅。
	if paddr, perr := startPprof(runCtx, c.EffPprof()); perr == nil && paddr != "" {
		log.Info("pprof 已启动", logx.Event("pprof.ready"),
			"addr", paddr, logx.Hint("仅供本机使用，绑回环地址"))
	} else if perr != nil {
		// 用 Info 而不是 Debug：剖析是排障手段，失败时必须看得见 ✅⚠️
		log.Info("pprof 未启动", logx.Event("pprof.failed"), logx.FieldErr, perr.Error())
	}
	if planeErr != nil {
		log.Error("数据面启动失败",
			logx.Event("plane.failed"),
			logx.FieldLayer, logx.LayerForward,
			logx.FieldErr, planeErr.Error())
		// 数据面失败不等于整机失败：发现能力仍有价值（可以当诊断观测点）。
		// 但必须明确告知流量不会被接管。
		log.Warn("降级为仅发现模式，流量不会被接管",
			logx.Event("plane.degraded"),
			logx.Hint("先解决上面的错误；slx doctor 可复查环境"))
	}
	if dp != nil {
		defer dp.cleanup()
		planeRef.Store(dp.plane)
		go func() {
			if err := dp.plane.Run(runCtx); err != nil && runCtx.Err() == nil {
				log.Error("数据面退出", logx.Event("plane.exited"), logx.FieldErr, err.Error())
			}
		}()
		// 分流看门狗：出口可用才接管，规则被清掉自动补回，出口没了自动退回直连
		go dp.dispatch.watch(runCtx, log, dp.plane.HasEgress)

		// ZT 环路保护规则的独立看护。
		//
		// 刻意跟分流看门狗分开：分流可以被暂停（出口没了就退回直连），
		// 但这条保护规则必须一直在 —— 它是隧道不把自己吸进隧道的最后一道防线。
		go watchZTGuard(runCtx, log, dp.sys, dp.ztGuard)

		// 把 SIGUSR1 转成「立刻自查」，避免防火墙重载后干等 10 秒
		go func() {
			for {
				select {
				case <-runCtx.Done():
					return
				case <-refreshCh:
					if dp.dispatch != nil && dp.dispatch.refresh != nil {
						select {
						case dp.dispatch.refresh <- struct{}{}:
						default: // 已有待处理请求，不堆叠
						}
					}
				}
			}
		}()

		// 探测：L1 每 2 秒测网格质量，L2 每 60 秒测出口到目标的实际速度。
		// 结果直接喂给选路器，后者每包按评分挑出口。
		var proberRef *prober
		if dp.picker != nil {
			pr := newProber(disc, dp.picker, log, nodeID)
			proberRef = pr
			// L2 的初始目标是配置里给的（不硬编码任何测速站）；
			// 后续会随实际访问的站点自动补充。
			for _, d := range c.ProbeTargets {
				pr.AddL2Target(d)
			}
			pr.Start(runCtx)
			// 定期把选路状态写到快照文件，供 slx routes / slx explain 读取
			go writeSnapshotLoop(runCtx, dp.picker, disc, nodeID)
			log.Info("探测已启动",
				logx.Event("probe.started"),
				"l1_interval", "2s", "l2_interval", "60s",
				"targets", len(c.ProbeTargets))
		}

		// 状态快照：供 slx status / slx trace 读取。
		// 5 秒一次，与上面的选路快照同步节奏。
		collect := func(now time.Time) StatusSnapshot {
			rolesCopy := append([]string(nil), roles...)
			return collectStatus(startedAt, nodeID, rolesCopy,
				dp.innerIP, dp.dev.Name(), dp.dev.MTU(),
				env, disc, dp, c, now)
		}
		go writeStatusLoop(runCtx, collect)

		// DNS 转发器：境外域名经出口解析，顺带建立域名↔出口亲和。
		if c.DNSListen != "" {
			startDNS(runCtx, log, c, disc, dp, nodeID, proberRef)
		}

		// 本地只读 API：给 CLI 一个能主动触发动作的通道。
		api := &apiServer{
			addr:   apiAddr(),
			pprof:  c.EffPprof(),
			status: func() StatusSnapshot { return collect(time.Now()) },
			routes: func() any {
				var ids []string
				for _, p := range disc.Table().EgressCandidates(time.Now()) {
					if p.NodeID != nodeID {
						ids = append(ids, p.NodeID)
					}
				}
				return dp.picker.SnapshotState(ids, time.Now())
			},
			probe: func(domain string) error {
				if proberRef == nil {
					return fmt.Errorf("探测未启用")
				}
				proberRef.AddL2Target(domain)
				return nil
			},
		}
		if addr, aerr := startAPI(runCtx, api); aerr != nil {
			// 用 Warn 而不是 Debug：这个失败必须看得见 ——
			// 「API 一直没监听」这类问题最费时间的就是「它到底有没有起来」。
			log.Warn("本地 API 未启动", logx.Event("api.start_failed"),
				logx.FieldErr, aerr.Error(),
				logx.Hint("端口被占用或数据面没起来（API 只在数据面就绪后才启动）"))
		} else {
			log.Info("本地 API 已启动", logx.Event("api.ready"), "addr", addr,
				logx.Hint("仅供本机 CLI 使用，默认只监听回环地址"))
		}
	}

	reported := false
	firstReport := time.NewTimer(5 * time.Second)
	defer firstReport.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("收到退出信号，正在停止", logx.Event("runtime.shutdown"))
			saveState()
			if dp != nil {
				s := dp.plane.Stats.Snapshot()
				log.Info("数据面统计", logx.Event("plane.stats"), "summary", summarizeStats(s))
			}
			return 0

		case err := <-discErr:
			if err != nil {
				log.Error("发现通道异常退出",
					logx.Event("discovery.crashed"),
					logx.FieldLayer, logx.LayerMesh, logx.FieldErr, err.Error())
				return 1
			}
			return 0

		case <-firstReport.C:
			reportPeers(log, disc.Table())
			reported = true

		case <-save.C:
			saveState()
			if !reported && disc.Table().Len() > 0 {
				reportPeers(log, disc.Table())
				reported = true
			}

		case <-statsTick.C:
			if dp == nil {
				continue
			}
			cur := dp.plane.Stats.Snapshot()
			if cur == lastStats {
				continue // 无变化不打印
			}
			// 增量更有用：一眼看出这几秒里哪一步在涨
			log.Info("数据面计数",
				logx.Event("plane.stats"),
				"tun_read", cur.TunRead-lastStats.TunRead,
				"sent", cur.Sent-lastStats.Sent,
				"tun_write", cur.TunWrite-lastStats.TunWrite,
				"no_route", cur.NoRoute-lastStats.NoRoute,
				"send_err", cur.SendErr-lastStats.SendErr,
				"oversize", cur.Oversize-lastStats.Oversize,
				"bad_pkt", cur.BadPacket-lastStats.BadPacket,
				"total_read", cur.TunRead,
				"total_written", cur.TunWrite)
			lastStats = cur
		}
	}
}

// reportPeers 输出一次拓扑汇总。
func reportPeers(log *slog.Logger, t *mesh.PeerTable) {
	now := time.Now()
	peers := t.Snapshot()
	cands := t.EgressCandidates(now)

	if len(peers) == 0 {
		log.Warn("尚未发现任何节点",
			logx.Event("discovery.none"),
			logx.FieldLayer, logx.LayerMesh,
			logx.Hint("确认其它节点也运行了 slx；若 ZT 关闭广播，子网扫描会自动兜底"))
		return
	}

	log.Info("拓扑汇总",
		logx.Event("discovery.summary"),
		"peers", len(peers), "egress_candidates", len(cands))

	for _, p := range peers {
		zt := "-"
		if p.ZTAddr != nil {
			zt = p.ZTAddr.String()
		}
		log.Info("  节点",
			logx.FieldNodeID, p.NodeID, "zt_addr", zt,
			"advertise", p.AdvertiseAddr, "caps", p.Capabilities,
			"state", p.State(now).String())
	}
}

// detectRoles 根据环境推断本节点能力。
func detectRoles(env *envdetect.Env) []string {
	roles := []string{"egress"}
	if env.HasTUN && env.LANIface != "" {
		roles = append(roles, "gateway")
	}
	if env.HasTUN && len(env.ZTIfaces) > 0 {
		roles = append(roles, "probe")
	}
	return roles
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }
