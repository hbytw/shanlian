package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"slx/internal/mesh"
)

// PeersCmd 展示节点拓扑。
//
// 数据来源是守护进程周期性写下的状态缓存，而不是与守护进程实时通信。
// 这是 M1 的取舍：先把拓扑看清，实时视图留给 M5 的本地 HTTP API。
// 为了不误导人，输出里会明确标注快照时间。
func PeersCmd(args []string) int {
	statePath := ""
	watch := false
	interval := 2 * time.Second
	for i, a := range args {
		switch {
		case a == "--state" && i+1 < len(args):
			statePath = args[i+1]
		case strings.HasPrefix(a, "--state="):
			statePath = strings.TrimPrefix(a, "--state=")
		case a == "-w" || a == "--watch":
			watch = true
		}
	}
	if statePath == "" {
		statePath = mesh.DefaultStatePath
	}

	for {
		if code := renderPeers(statePath); code != 0 {
			return code
		}
		if !watch {
			return 0
		}
		fmt.Printf("\n每 %s 刷新一次，Ctrl-C 退出\n", interval)
		time.Sleep(interval)
		fmt.Print("\x1b[H\x1b[2J") // 清屏
	}
}

func renderPeers(statePath string) int {
	t := mesh.NewPeerTable()

	info, statErr := os.Stat(statePath)
	if statErr != nil {
		fmt.Printf("尚未发现任何节点。\n\n")
		fmt.Printf("状态缓存: %s（不存在）\n", statePath)
		fmt.Printf("\n可能原因与处理:\n")
		fmt.Printf("  1. 守护进程没在跑        → slx run\n")
		fmt.Printf("  2. 刚启动还没写出缓存    → 等 5 秒再看\n")
		fmt.Printf("  3. ZT 其它节点没跑 slx   → 每台节点都要装并启动\n")
		return 0
	}

	age := time.Since(info.ModTime())
	// 用 LoadSnapshot 而非 LoadState：这里要展示真实状态，
	// 不能用「恢复即标脏」的逻辑，否则所有节点永远显示迟滞。
	n := t.LoadSnapshot(statePath, readCachedSelfID(statePath), mesh.StateMaxAge)

	fmt.Println("节点拓扑")
	fmt.Println(strings.Repeat("=", 100))

	if age > 15*time.Second {
		fmt.Printf("⚠ 快照已 %s 未更新，守护进程可能已停止\n", humanAge(age))
	} else {
		fmt.Printf("快照时间：%s 前\n", humanAge(age))
	}
	fmt.Println()

	if n == 0 {
		fmt.Println("缓存里没有可用的节点记录。")
		fmt.Println("提示：守护进程刚启动时缓存为空，等下一轮广播后重新查看。")
		return 0
	}

	now := time.Now()
	hdr := fmt.Sprintf("%-18s %-22s %-24s %-18s %-10s %s",
		"节点 ID", "ZT 地址", "直连地址", "能力", "传输", "状态")
	fmt.Println(hdr)
	fmt.Println(strings.Repeat("-", 100))

	for _, p := range t.Snapshot() {
		zt := "-"
		if p.ZTAddr != nil {
			zt = p.ZTAddr.String()
		}
		adv := p.AdvertiseAddr
		if adv == "" {
			adv = "-"
		}
		caps := strings.Join(p.Capabilities, ",")
		if caps == "" {
			caps = "-"
		}
		trans := strings.Join(p.Transports, ",")
		if trans == "" {
			trans = "-"
		}
		fmt.Printf("%-18s %-22s %-24s %-18s %-10s %s\n",
			p.NodeID, zt, adv, caps, trans, p.State(now).Chinese())
	}

	cands := t.EgressCandidates(now)
	fmt.Printf("\n共 %d 个节点，其中可用出口候选 %d 个\n", t.Len(), len(cands))

	if len(cands) == 0 {
		fmt.Println()
		fmt.Println("没有可用出口候选。检查：")
		fmt.Println("  · 各 VPS 上是否已运行 slx（角色应为 egress）")
		fmt.Println("  · 状态是否都是「迟滞」（说明心跳断了）—— 看 slx doctor 的 ZeroTier 项")
	}
	return 0
}

// readCachedSelfID 读出缓存文件里记录的 self node_id。
//
// 缓存恢复要校验「这份缓存属于自己」，所以需要先知道自己的 ID。
// 这里直接再读一次文件即可，代价可忽略。
func readCachedSelfID(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// 极简抽取，避免为此再定义一套结构
	const key = `"node_id":`
	s := string(b)
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	start := strings.Index(rest, `"`)
	if start < 0 {
		return ""
	}
	rest = rest[start+1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Second:
		return "不到 1 秒"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	}
}
