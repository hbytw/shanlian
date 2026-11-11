package cli

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"slx/internal/netcfg"
)

// fakeIPTables 是只够用的 iptables 模拟器（只管 FORWARD 链）。
type fakeIPTables struct {
	mu    sync.Mutex
	rules []string // filter/FORWARD 的规则行
}

func (f *fakeIPTables) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if name != "iptables" && name != "iptables-nft" && name != "iptables-legacy" {
		return "", nil
	}
	if len(args) >= 2 && args[0] == "-t" {
		args = args[2:]
	}
	if len(args) == 0 {
		return "", nil
	}
	spec := ""
	switch args[0] {
	case "-S":
		out := make([]string, 0, len(f.rules))
		for _, r := range f.rules {
			out = append(out, "-A FORWARD "+strings.TrimPrefix(r, "-A FORWARD "))
		}
		return strings.Join(out, "\n"), nil
	case "-C", "-D", "-I", "-A":
		if len(args) < 2 {
			return "", nil
		}
		chain := args[1]
		rest := args[2:]
		if args[0] == "-I" && len(rest) > 0 {
			rest = rest[1:]
		}
		spec = "-A " + chain + " " + strings.Join(rest, " ")
		switch args[0] {
		case "-C":
			for _, r := range f.rules {
				if r == spec {
					return "", nil
				}
			}
			return "", errNoSuchRule
		case "-D":
			for i, r := range f.rules {
				if r == spec {
					f.rules = append(f.rules[:i], f.rules[i+1:]...)
					return "", nil
				}
			}
			return "", errNoSuchRule
		default:
			if args[0] == "-I" {
				f.rules = append([]string{spec}, f.rules...)
			} else {
				f.rules = append(f.rules, spec)
			}
			return "", nil
		}
	}
	return "", nil
}

// reload 模拟防火墙重载：整张规则表被清空重建。
func (f *fakeIPTables) reload() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = nil
}

func (f *fakeIPTables) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rules)
}

var errNoSuchRule = &ruleError{}

type ruleError struct{}

func (e *ruleError) Error() string { return "iptables: Bad rule" }

// TestEnsureForwardRules_RestoresAfterFirewallReload 是审计报告「根因 M」的回归测试。
//
// 真机症状：防火墙一重载（fw3 reload / UCI 提交 / 装新服务），
// `-A FORWARD -o slx0 -j ACCEPT` 这两条就没了，
// 而 ipset、mangle 打标、ip rule、路由表都还在
// ⇒ **状态页显示一切正常，但 LAN→slx0 被防火墙拒 ⇒ 境外流量永久断**，
// 只有重启 SLX 才能恢复。
//
// 修法：看门狗补回 FORWARD 规则（只补「本来就有」的）。
func TestEnsureForwardRules_RestoresAfterFirewallReload(t *testing.T) {
	fake := &fakeIPTables{}
	sys := netcfg.NewSystem(fake.run)
	sys.IPTables = "iptables"

	// 启动时这两条规则在位（proxy 模式的隧道网卡）。
	if _, err := sys.EnsureForwardAccept("slx0", false); err != nil {
		t.Fatalf("装 FORWARD 规则失败: %v", err)
	}
	if _, err := sys.EnsureForwardAccept("slx0", true); err != nil {
		t.Fatalf("装 FORWARD 规则失败: %v", err)
	}

	d := &dispatchState{
		sys: sys,
		forwardPresent: map[string]map[bool]bool{
			"slx0": {false: true, true: true},
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 防火墙重载：规则没了。
	fake.reload()
	if fake.count() != 0 {
		t.Fatal("模拟器状态不对：重载后规则应当消失")
	}

	// 看门狗跑一轮。
	d.ensureForwardRules(log)

	if fake.count() != 2 {
		t.Fatalf("防火墙重载后 FORWARD 规则条数 = %d，期望 2（必须补回来）", fake.count())
	}
	if !sys.HasForwardAccept("slx0", false) || !sys.HasForwardAccept("slx0", true) {
		t.Fatal("防火墙重载后 FORWARD 规则必须能被补回来")
	}
}

// TestEnsureForwardRules_OnlyRestoresWhatWasThere 确认看门狗
// **不会新增本来就没有的规则**。
//
// 这一点很重要：看门狗如果顺手把内网网卡之间的转发也放行，
// 那就是安全策略的变化（LAN→LAN 直通），不该由看门狗悄悄做。
func TestEnsureForwardRules_OnlyRestoresWhatWasThere(t *testing.T) {
	fake := &fakeIPTables{}
	sys := netcfg.NewSystem(fake.run)
	sys.IPTables = "iptables"

	d := &dispatchState{
		sys: sys,
		forwardPresent: map[string]map[bool]bool{
			// br-lan 的规则「本来就不在」⇒ 看门狗不该给它装。
			"br-lan": {false: false, true: false},
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	d.ensureForwardRules(log)

	if fake.count() != 0 {
		t.Fatalf("看门狗不该新增本来没有的规则，实际装了 %d 条", fake.count())
	}
}
