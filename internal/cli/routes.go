package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"

	"slx/internal/mesh"
	"slx/internal/picker"
)

// routeStatePath 是选路状态快照文件。
//
// 放 /tmp 而不是 /etc：这是瞬时状态，重启后由守护进程重新生成，
// 不该进持久化目录（也避免磨损闪存）。
func routeStatePath() string {
	if p := os.Getenv("SLX_ROUTE_STATE"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "slx-route.json")
}

// writeRouteSnapshot 由守护进程定期调用，把当前选路状态写到快照文件。
func writeRouteSnapshot(snap picker.Snapshot) error {
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	path := routeStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readRouteSnapshot 由 CLI 读取快照文件。
func readRouteSnapshot() (picker.Snapshot, error) {
	var snap picker.Snapshot
	b, err := os.ReadFile(routeStatePath())
	if err != nil {
		return snap, fmt.Errorf("读不到选路状态：%w\n提示：守护进程可能没在运行，或还没产出第一份快照", err)
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return snap, fmt.Errorf("选路状态文件损坏：%w", err)
	}
	return snap, nil
}

// writeSnapshotLoop 每 5 秒把选路状态落盘一次。
//
// 不写得更频繁：这些数据只用于人看和排障，5 秒足够新；
// 写得太勤会在闪存上产生无谓的擦写。
func writeSnapshotLoop(ctx context.Context, sel *picker.Selector, disc *mesh.Discovery, selfID string) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			var ids []string
			for _, c := range disc.Table().EgressCandidates(now) {
				if c.NodeID != selfID {
					ids = append(ids, c.NodeID)
				}
			}
			_ = writeRouteSnapshot(sel.SnapshotState(ids, now))
		}
	}
}

// Routes 实现 `slx routes`：展示路由表与当前候选评分。
func Routes() int {
	snap, err := readRouteSnapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	fmt.Printf("选路状态（更新于 %s，%s前）\n\n",
		snap.UpdatedAt.Format("15:04:05"),
		humanAge(time.Since(snap.UpdatedAt)))

	fmt.Println("候选出口：")
	if len(snap.Candidates) == 0 {
		fmt.Println("  （暂无其他节点）")
	} else {
		sort.SliceStable(snap.Candidates, func(i, j int) bool {
			return snap.Candidates[i].Score > snap.Candidates[j].Score
		})
		fmt.Printf("  %-18s %8s %10s %10s %8s  %s\n", "节点", "评分", "延迟", "带宽", "丢包", "状态")
		for _, c := range snap.Candidates {
			mark := "  "
			if c.NodeID == snap.Current {
				mark = "→ "
			}
			state := c.Breaker
			if state == "" {
				state = "正常"
			}
			if !c.Eligible {
				state = "不可用"
			}
			// 带上判定依据：只写「不可用」没人知道该查什么。
			if c.BreakerReason != "" {
				state += "（" + c.BreakerReason + "）"
			}
			if c.Note != "" && c.Note != "尚无测量数据" {
				state += "（" + c.Note + "）"
			}
			fmt.Printf("  %s%-16s %7.1f %8.1fms %8.1fM %7.1f%%  %s\n",
				mark, c.NodeID, c.Score, c.RTTMS, c.BWMbps, c.Loss*100, state)
		}
	}

	fmt.Printf("\n目的地缓存：%d 条\n", len(snap.Routes))
	if len(snap.Routes) > 0 {
		sort.SliceStable(snap.Routes, func(i, j int) bool {
			return snap.Routes[i].Prefix < snap.Routes[j].Prefix
		})
		fmt.Printf("  %-22s %-18s %8s %6s\n", "目的前缀", "出口", "评分", "存活")
		for _, r := range snap.Routes {
			fmt.Printf("  %-22s %-18s %7.1f %5ds\n", r.Prefix, r.Egress, r.Score, r.AgeSec)
		}
	}
	return 0
}

// Explain 实现 `slx explain <目标>`：解释某个目标为什么走这个出口。
//
// 读的是守护进程产出的快照 —— 不需要连上守护进程，也不会打扰它。
func Explain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法：slx explain <域名或IP>")
		return 2
	}
	target := args[0]

	snap, err := readRouteSnapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	fmt.Printf("目标：%s\n", target)

	// 1. 路由表精确命中？
	var ip net.IP
	if v := net.ParseIP(target); v != nil {
		ip = v
	} else {
		// 域名：先看有没有以该域名对应的 IP 记录过
		ips, rerr := net.LookupHost(target)
		if rerr == nil && len(ips) > 0 {
			ip = net.ParseIP(ips[0])
			fmt.Printf("解析：%s → %s（用本机 DNS，仅供对照）\n", target, ip)
		}
	}

	hit := false
	if ip != nil {
		for _, r := range snap.Routes {
			if _, ipnet, e := net.ParseCIDR(r.Prefix); e == nil && ipnet.Contains(ip) {
				fmt.Printf("\n路由表命中：%s → 出口 %s（%d 秒前记录，评分 %.1f）\n",
					r.Prefix, r.Egress, r.AgeSec, r.Score)
				hit = true
				break
			}
		}
	}
	if !hit {
		fmt.Println("\n路由表：未命中（下次选路会按实测评分决定，并记入缓存）")
	}

	// 2. 候选评分明细
	fmt.Println("\n候选出口评分：")
	if len(snap.Candidates) == 0 {
		fmt.Println("  （暂无其他节点）")
		return 0
	}
	sort.SliceStable(snap.Candidates, func(i, j int) bool {
		return snap.Candidates[i].Score > snap.Candidates[j].Score
	})
	fmt.Printf("  %-18s %8s %10s %10s %8s  %-8s %s\n",
		"节点", "评分", "延迟", "带宽", "丢包", "亲和", "可用性")
	for _, c := range snap.Candidates {
		aff := "否"
		if c.Affinity {
			aff = "是"
		}
		avail := "可用"
		if !c.Eligible {
			avail = "不可用"
		}
		if c.Breaker != "" {
			avail += "（" + c.Breaker + "）"
		}
		if c.BreakerReason != "" {
			avail += " " + c.BreakerReason
		}
		fmt.Printf("  %-18s %7.1f %8.1fms %8.1fM %7.1f%%  %-8s %s\n",
			c.NodeID, c.Score, c.RTTMS, c.BWMbps, c.Loss*100, aff, avail)
	}

	fmt.Printf("\n当前使用：%s\n", orDash(snap.Current))
	fmt.Println("\n评分公式：100 × (0.45×带宽归一 + 0.35×延迟归一 + 0.20×(1−丢包)) + 亲和加成")
	fmt.Println("  带宽归一 = bw/(bw+50M)   延迟归一 = 100/(100+rtt)   亲和加成 +15")
	fmt.Println("  切换门槛：新出口需高出 8 分以上，且不在 30 秒冷却期内")
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "（未选择）"
	}
	return s
}
