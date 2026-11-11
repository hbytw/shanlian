package netcfg

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeFW 是一个最小的 iptables / ip 命令模拟器。
//
// 目的：验证「规则装得上、删得掉、能在防火墙重载后补回来」，
// 而这些正是线上事故的根因（审计报告根因 M / S）。
type fakeFW struct {
	mu     sync.Mutex
	rules  map[string][]string // "table/chain" → 规则行
	routes map[int][]string    // 路由表号 → 路由行
}

func newFakeFW() *fakeFW {
	return &fakeFW{
		rules:  map[string][]string{},
		routes: map[int][]string{},
	}
}

func (f *fakeFW) key(table, chain string) string { return table + "/" + chain }

// run 是 netcfg.Runner 的实现。
func (f *fakeFW) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if name == "ip" {
		return f.runIP(args)
	}
	if name != "iptables" && name != "iptables-nft" && name != "iptables-legacy" {
		return "", fmt.Errorf("未知命令 %s", name)
	}
	return f.runIptables(args)
}

func (f *fakeFW) runIP(args []string) (string, error) {
	if len(args) >= 2 && args[0] == "route" {
		switch args[1] {
		case "flush":
			tbl := -1
			for i, a := range args {
				if a == "table" && i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%d", &tbl)
				}
			}
			if tbl >= 0 {
				f.routes[tbl] = nil
			}
			return "", nil
		case "del":
			tbl := -1
			dev := ""
			for i, a := range args {
				if a == "table" && i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%d", &tbl)
				}
				if a == "dev" && i+1 < len(args) {
					dev = args[i+1]
				}
			}
			if tbl >= 0 && dev != "" {
				kept := []string{}
				for _, r := range f.routes[tbl] {
					if strings.Contains(r, "dev "+dev) {
						continue
					}
					kept = append(kept, r)
				}
				f.routes[tbl] = kept
			}
			return "", nil
		}
	}
	return "", nil
}

func (f *fakeFW) runIptables(args []string) (string, error) {
	table := "filter"
	if len(args) >= 2 && args[0] == "-t" {
		table = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		return "", fmt.Errorf("参数不足")
	}
	op := args[0]
	rest := args[1:]

	switch op {
	case "-S":
		if len(rest) == 0 {
			return "", fmt.Errorf("参数不足")
		}
		chain := rest[0]
		lines := f.rules[f.key(table, chain)]
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			// 真实 iptables 的 -S 输出会把 comment 值加引号，
			// 代码正是靠这个引号来识别自己的规则（见 hasRuleWithComment）。
			l = quoteComment(l)
			out = append(out, "-A "+chain+" "+strings.TrimPrefix(l, "-A "+chain+" "))
		}
		return strings.Join(out, "\n"), nil

	case "-C", "-D":
		if len(rest) < 2 {
			return "", fmt.Errorf("参数不足")
		}
		chain := rest[0]
		spec := strings.Join(rest[1:], " ")
		key := f.key(table, chain)
		for i, l := range f.rules[key] {
			// 真实 iptables 的 -C/-D 要求**整条规则完全匹配**，
			// 不是「包含就算」。这一点很关键：规格差一点就删不掉。
			if norm(l) == "-A "+chain+" "+norm(spec) {
				if op == "-D" {
					f.rules[key] = append(f.rules[key][:i], f.rules[key][i+1:]...)
				}
				return "", nil
			}
		}
		return "", fmt.Errorf("iptables: Bad rule (does a matching rule exist in that chain?)")

	case "-I", "-A":
		if len(rest) < 2 {
			return "", fmt.Errorf("参数不足")
		}
		chain := rest[0]
		spec := rest[1:]
		if op == "-I" && len(spec) > 0 {
			// "-I CHAIN 1 ..."：位置参数 1
			spec = spec[1:]
		}
		line := "-A " + chain + " " + strings.Join(spec, " ")
		key := f.key(table, chain)
		if op == "-I" {
			f.rules[key] = append([]string{line}, f.rules[key]...)
		} else {
			f.rules[key] = append(f.rules[key], line)
		}
		return "", nil
	}
	return "", fmt.Errorf("不支持的操作 %s", op)
}

func (f *fakeFW) has(table, chain, comment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.rules[f.key(table, chain)] {
		if strings.Contains(l, `"`+comment+`"`) {
			return true
		}
	}
	return false
}

func (f *fakeFW) count(table, chain string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rules[f.key(table, chain)])
}

// newTestSystem 造一个走模拟器的 System。
func newTestSystem(f *fakeFW) *System {
	s := NewSystem(f.run)
	s.IPTables = "iptables"
	s.IP = "ip"
	return s
}

// TestRemoveMarkRule_DeletesInstalledRule 是「撤分流后打标规则删不掉」的回归测试。
//
// 旧实现自己拼了一个「只有 comment + MARK」的 -D 规格，
// 与真实规则（还带 -i 接口、-m set ! --match-set、--set-xmark）不匹配
// ⇒ iptables 拒绝删除 ⇒ 错误又被丢掉 ⇒ 规则永远留着。
func TestRemoveMarkRule_DeletesInstalledRule(t *testing.T) {
	f := newFakeFW()
	s := newTestSystem(f)

	if _, err := s.EnsureMarkRule("PREROUTING", "br-lan", 0x100, "slx_cn", "slx_exempt", "split-mark-br-lan"); err != nil {
		t.Fatalf("装打标规则失败: %v", err)
	}
	if !s.HasMarkRule("PREROUTING", "split-mark-br-lan") {
		t.Fatal("打标规则应当已安装")
	}

	s.RemoveMarkRule("PREROUTING", "split-mark-br-lan")

	if s.HasMarkRule("PREROUTING", "split-mark-br-lan") {
		t.Fatal("RemoveMarkRule 之后打标规则必须消失（旧实现删不掉）")
	}
}

// TestCleanupMarked_CoversManglePrerouting 确认 uninstall 能清掉
// mangle/PREROUTING 里的打标规则（之前只扫 nat/filter 的 POSTROUTING+FORWARD）。
func TestCleanupMarked_CoversManglePrerouting(t *testing.T) {
	f := newFakeFW()
	s := newTestSystem(f)

	if _, err := s.EnsureMarkRule("PREROUTING", "br-lan", 0x100, "slx_cn", "slx_exempt", "split-mark-br-lan"); err != nil {
		t.Fatalf("装打标规则失败: %v", err)
	}
	if f.count("mangle", "PREROUTING") == 0 {
		t.Fatal("模拟器状态不对：mangle/PREROUTING 应当有规则")
	}

	removed := s.CleanupMarked()
	if removed == 0 {
		t.Fatal("CleanupMarked 应当删掉 mangle/PREROUTING 里的打标规则")
	}
	if s.HasMarkRule("PREROUTING", "split-mark-br-lan") {
		t.Fatal("uninstall 后打标规则必须消失")
	}
}

// TestEnsureForwardAccept_RestoresAfterFirewallReload 是根因 M 的回归测试：
// 防火墙重载把 FORWARD 规则冲掉后，必须能补回来。
//
// 真机症状：状态页全绿（ipset/mangle/ip rule 都在），但境外流量永久断，
// 只能重启 SLX 才恢复。
func TestEnsureForwardAccept_RestoresAfterFirewallReload(t *testing.T) {
	f := newFakeFW()
	s := newTestSystem(f)

	for _, outgoing := range []bool{false, true} {
		if _, err := s.EnsureForwardAccept("slx0", outgoing); err != nil {
			t.Fatalf("装 FORWARD 规则失败: %v", err)
		}
	}
	if !s.HasForwardAccept("slx0", false) || !s.HasForwardAccept("slx0", true) {
		t.Fatal("FORWARD 规则应当已安装")
	}

	// 模拟防火墙重载：filter 表被清空。
	f.mu.Lock()
	f.rules["filter/FORWARD"] = nil
	f.mu.Unlock()

	if s.HasForwardAccept("slx0", false) || s.HasForwardAccept("slx0", true) {
		t.Fatal("模拟器状态不对：重载后 FORWARD 规则应当消失")
	}

	// 自愈：再装一次。
	for _, outgoing := range []bool{false, true} {
		if _, err := s.EnsureForwardAccept("slx0", outgoing); err != nil {
			t.Fatalf("补装 FORWARD 规则失败: %v", err)
		}
	}
	if !s.HasForwardAccept("slx0", false) || !s.HasForwardAccept("slx0", true) {
		t.Fatal("防火墙重载后 FORWARD 规则必须能被补回来")
	}
}

// TestEnsureForwardAccept_Idempotent 确认重复安装不会堆叠规则。
func TestEnsureForwardAccept_Idempotent(t *testing.T) {
	f := newFakeFW()
	s := newTestSystem(f)

	for i := 0; i < 3; i++ {
		if _, err := s.EnsureForwardAccept("slx0", true); err != nil {
			t.Fatalf("装 FORWARD 规则失败: %v", err)
		}
	}
	if n := f.count("filter", "FORWARD"); n != 1 {
		t.Fatalf("FORWARD 规则条数 = %d，期望 1（安装必须幂等）", n)
	}
}

// TestRemoveTableRoute_KeepsOtherRoutes 确认只删自己那条路由。
//
// 旧实现是整表 flush ⇒ 别人往表 100 里加的路由会被一并冲掉。
func TestRemoveTableRoute_KeepsOtherRoutes(t *testing.T) {
	f := newFakeFW()
	s := newTestSystem(f)

	f.mu.Lock()
	f.routes[100] = []string{"default dev slx0", "10.0.0.0/8 dev eth0"}
	f.mu.Unlock()

	s.RemoveTableRoute(100, "default", "slx0")

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.routes[100] {
		if strings.Contains(r, "slx0") {
			t.Fatalf("自己的路由应当被删掉，却还在: %v", f.routes[100])
		}
	}
	if len(f.routes[100]) != 1 || !strings.Contains(f.routes[100][0], "eth0") {
		t.Fatalf("别人的路由必须保留，实际 = %v", f.routes[100])
	}
}

// norm 去掉引号，让「带引号的 -S 输出」与「不带引号的命令参数」能比较。
func norm(s string) string { return strings.ReplaceAll(s, `"`, "") }

// quoteComment 给 comment 值加引号（模拟 iptables -S 的输出格式）。
func quoteComment(line string) string {
	fields := strings.Split(line, " ")
	for i, f := range fields {
		if f == "--comment" && i+1 < len(fields) {
			fields[i+1] = `"` + fields[i+1] + `"`
		}
	}
	return strings.Join(fields, " ")
}
