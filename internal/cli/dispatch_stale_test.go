package cli

import (
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"

	"slx/internal/netcfg"
)

// fakeIP 只模拟 ip 命令（路由表部分），用来验证「启动时清理残留」。
type fakeIP struct {
	mu        sync.Mutex
	routes    map[int][]string
	deleted   []string
	removedIP []string
}

func (f *fakeIP) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if name == "ip" {
		if len(args) >= 3 && args[0] == "route" && args[1] == "show" && args[2] == "table" {
			tbl := 0
			for i, a := range args {
				if a == "table" && i+1 < len(args) {
					tbl, _ = strconv.Atoi(args[i+1])
				}
			}
			return strings.Join(f.routes[tbl], "\n"), nil
		}
		if len(args) >= 2 && args[0] == "route" && args[1] == "del" {
			f.deleted = append(f.deleted, strings.Join(args, " "))
			return "", nil
		}
		return "", nil
	}
	if name == "ip" || strings.Contains(name, "iptables") {
		return "", nil
	}
	return "", nil
}

// TestCleanupStaleRoutes_RemovesDanglingRoute 验证「上一轮残留」的清理。
//
// 场景：进程被 kill -9 ⇒ table 100 里的 "default dev slx0" 残留，
// 而 slx0 已随进程消失 ⇒ 被标记 0x100 的包被内核丢掉
// ⇒ **境外流量被黑洞**（不是回退直连）。
//
// 判据必须保守：只删**设备已经不存在**的路由，活路由一条不动。
func TestCleanupStaleRoutes_RemovesDanglingRoute(t *testing.T) {
	fake := &fakeIP{routes: map[int][]string{}}
	// slxstale0 是个不存在的网卡 ⇒ 一定是残留。
	fake.routes[100] = []string{"default dev slxstale0"}

	sys := netcfg.NewSystem(fake.run)
	sys.IP = "ip"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cleanupStaleRoutes(log, sys)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleted) != 1 {
		t.Fatalf("应当删掉那条失效路由，实际删除次数 = %d（%v）", len(fake.deleted), fake.deleted)
	}
	if !strings.Contains(fake.deleted[0], "slxstale0") {
		t.Fatalf("删的应当是失效路由，实际 = %v", fake.deleted)
	}
}

// TestCleanupStaleRoutes_KeepsLiveRoutes 确认活路由不会被误删。
//
// 这是「宁可不动，也不要误触发」的要求：
// 清理逻辑只删指向已消失设备的路由。
func TestCleanupStaleRoutes_KeepsLiveRoutes(t *testing.T) {
	// 找一个本机真实存在的网卡来代表「活路由」。
	live := ""
	for _, cand := range []string{"lo", "eth0", "en0"} {
		if netcfg.DeviceExists(cand) {
			live = cand
			break
		}
	}
	if live == "" {
		t.Skip("本机没有可用的网卡名（/sys/class/net 不可用），跳过")
	}

	fake := &fakeIP{routes: map[int][]string{}}
	fake.routes[100] = []string{"default dev " + live}

	sys := netcfg.NewSystem(fake.run)
	sys.IP = "ip"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cleanupStaleRoutes(log, sys)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleted) != 0 {
		t.Fatalf("活路由不该被删，实际删了 %v", fake.deleted)
	}
}
