package dispatch

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeSystem 是内存版的 System，用来在单元测试里验证规则行为。
//
// 为什么需要它：真实的 *netcfg.System 内部 exec 真实命令，
// 只能在装了 iproute 的机器上跑 —— 于是「暂停分流会不会把 ZT 环路保护
// 规则一起撤掉」这种**行为**没法被自动验证，只能靠人肉上真机试。
// 线上那次死锁就是这么漏出去的。
type fakeSystem struct {
	mu    sync.Mutex
	rules map[string]bool     // key: priority|selector|table
	marks map[string]bool     // key: chain|comment
	sets  map[string][]string // ipset 名 → 内容
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		rules: map[string]bool{},
		marks: map[string]bool{},
		sets:  map[string][]string{},
	}
}

func ruleKey(priority int, selector, table string) string {
	return fmt.Sprintf("%d|%s|%s", priority, selector, table)
}

func markKey(chain, comment string) string {
	return chain + "|" + comment
}

func (f *fakeSystem) IPSetCreate(name string, maxElem int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sets[name]; !ok {
		f.sets[name] = nil
	}
	return nil
}

func (f *fakeSystem) IPSetSwapReplace(name string, cidrs []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets[name] = append([]string(nil), cidrs...)
	return len(cidrs), nil
}

func (f *fakeSystem) IPSetFlush(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets[name] = nil
}

func (f *fakeSystem) IPSetAddBatch(name string, cidrs []string) (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets[name] = append(f.sets[name], cidrs...)
	return len(cidrs), 0
}

func (f *fakeSystem) IPSetCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	items, ok := f.sets[name]
	if !ok {
		return -1
	}
	return len(items)
}

func (f *fakeSystem) IPSetDestroy(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sets, name)
}

func (f *fakeSystem) EnsureIPRule(priority int, selector, table string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ruleKey(priority, selector, table)
	if f.rules[k] {
		return false, nil
	}
	f.rules[k] = true
	return true, nil
}

func (f *fakeSystem) RemoveIPRule(priority int, selector, table string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rules, ruleKey(priority, selector, table))
}

func (f *fakeSystem) IPRuleExists(priority int, selector, table string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rules[ruleKey(priority, selector, table)]
}

func (f *fakeSystem) EnsureRuleInTable(table int, dst, dev string) (bool, error) {
	return true, nil
}

func (f *fakeSystem) RouteInTableExists(table int, dst, dev string) bool {
	return true
}

func (f *fakeSystem) ClearTable(table int) {}

func (f *fakeSystem) EnsureMarkRule(chain, inIface string, mark uint32,
	cnSet, exemptSet, comment string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := markKey(chain, comment)
	if f.marks[k] {
		return false, nil
	}
	f.marks[k] = true
	return true, nil
}

func (f *fakeSystem) RemoveMarkRule(chain, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.marks, markKey(chain, comment))
}

func (f *fakeSystem) HasMarkRule(chain, comment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marks[markKey(chain, comment)]
}

// ruleList 返回当前存在的 ip rule（按 key 排序，便于断言）。
func (f *fakeSystem) ruleList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.rules))
	for k := range f.rules {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// testSpec 造一份够用的分流配置（Setup 要求国内网段 ≥ 100 条）。
func testSpec() Spec {
	cidrs := make([]string, 0, 128)
	for i := 0; i < 128; i++ {
		cidrs = append(cidrs, fmt.Sprintf("10.%d.%d.0/24", i/256, i%256))
	}
	return Spec{
		TunnelDev: "slx0",
		InnerCIDR: "10.254.0.0/16",
		ZTSubnets: []string{"192.168.99.0/24"},
		LANIfaces: []string{"br-lan"},
		CNCIDRs:   cidrs,
	}
}

// TestZTProtectionSurvivesDispatchTeardown 是本次线上事故的回归测试。
//
// 场景（真实发生过）：
//
//	看门狗发现没有可用出口 → 调 Teardown 撤下分流规则
//	→ （旧代码）ZT 环路保护规则被一起撤掉
//	→ ZT 流量被吸进隧道 → 出口 agent 连不回网关 → 没有出口
//	→ 分流永远被撤 → 死锁
//
// 断言：暂停分流之后，ZT 保护规则必须还在。
func TestZTProtectionSurvivesDispatchTeardown(t *testing.T) {
	sys := newFakeSystem()
	spec := testSpec()

	// 1. 进程启动：ZT 保护规则装上（独立于分流）。
	guard := NewZTGuard(spec.ZTSubnets)
	if err := guard.Install(sys); err != nil {
		t.Fatalf("安装 ZT 保护规则失败: %v", err)
	}
	ztRule := ruleKey(RulePriorityZT, "to 192.168.99.0/24", TableMain)
	if _, ok := sys.rules[ztRule]; !ok {
		t.Fatalf("ZT 保护规则没有装上，当前规则: %v", sys.ruleList())
	}

	// 2. 分流启用。
	st, err := Setup(sys, spec)
	if err != nil {
		t.Fatalf("安装分流失败: %v", err)
	}

	// 3. 出口没了 → 看门狗撤下分流规则（= 暂停分流）。
	st.Teardown(sys)

	// 4. 断言：ZT 保护规则仍然在位。
	if !sys.IPRuleExists(RulePriorityZT, "to 192.168.99.0/24", TableMain) {
		t.Fatalf("暂停分流后 ZT 保护规则被撤掉了 —— 这就是线上那次死锁的根因；当前规则: %v",
			sys.ruleList())
	}
	if probs := guard.Verify(sys); len(probs) != 0 {
		t.Fatalf("暂停分流后 ZT 保护规则自检不通过: %v", probs)
	}

	// 5. 分流规则确实被撤了（否则这个测试就没测到东西）。
	markRule := ruleKey(RulePriorityMark, fmt.Sprintf("fwmark 0x%x", MarkRoute),
		fmt.Sprintf("%d", RouteTable))
	if sys.IPRuleExists(RulePriorityMark, fmt.Sprintf("fwmark 0x%x", MarkRoute),
		fmt.Sprintf("%d", RouteTable)) {
		t.Fatalf("暂停分流后 fwmark 规则还在，分流并没有被真正撤下")
	}
	if _, ok := sys.rules[markRule]; ok {
		t.Fatalf("fwmark 规则仍然存在")
	}
	if sys.IPSetCount(SetCN) >= 0 {
		t.Fatalf("暂停分流后国内网段集合仍然存在")
	}
}

// TestSetupDoesNotTouchZTRules 确认分流规则与 ZT 保护规则互不干涉。
func TestSetupDoesNotTouchZTRules(t *testing.T) {
	sys := newFakeSystem()
	spec := testSpec()

	if _, err := Setup(sys, spec); err != nil {
		t.Fatalf("安装分流失败: %v", err)
	}
	// 分流自己不该装 ZT 规则（那是 ZTGuard 的职责）。
	if sys.IPRuleExists(RulePriorityZT, "to 192.168.99.0/24", TableMain) {
		t.Fatalf("Setup 装了 ZT 保护规则：它的生命周期必须独立于分流")
	}
	// 但豁免集合里必须包含 ZT 网段（打标记那层的保护）。
	if !contains(sys.sets[SetExempt], "192.168.99.0/24") {
		t.Fatalf("豁免集合里没有 ZT 网段: %v", sys.sets[SetExempt])
	}
}

// TestZTGuardIdempotentAndSelfHeal 覆盖看门狗的自愈路径。
func TestZTGuardIdempotentAndSelfHeal(t *testing.T) {
	sys := newFakeSystem()
	guard := NewZTGuard([]string{"192.168.99.0/24", "10.147.17.0/24", "", "192.168.99.0/24"})

	if len(guard.Subnets()) != 2 {
		t.Fatalf("网段去重/过滤不对: %v", guard.Subnets())
	}

	for i := 0; i < 3; i++ {
		if err := guard.Install(sys); err != nil {
			t.Fatalf("第 %d 次安装失败: %v", i, err)
		}
	}
	if len(sys.ruleList()) != 2 {
		t.Fatalf("重复安装产生了重复规则: %v", sys.ruleList())
	}

	// 模拟 OpenWrt 防火墙重载把规则清掉。
	sys.RemoveIPRule(RulePriorityZT, "to 192.168.99.0/24", TableMain)
	probs := guard.Verify(sys)
	if len(probs) != 1 {
		t.Fatalf("规则丢了却没被自检发现: %v", probs)
	}
	if err := guard.Install(sys); err != nil {
		t.Fatalf("自愈安装失败: %v", err)
	}
	if len(guard.Verify(sys)) != 0 {
		t.Fatalf("自愈之后仍然报缺失")
	}

	// 只有显式 Remove 才撤掉它。
	guard.Remove(sys)
	if len(sys.ruleList()) != 0 {
		t.Fatalf("Remove 之后规则还在: %v", sys.ruleList())
	}
}

// TestZTGuardEmptySubnet 确认没有 ZT 时它是安全的空操作。
func TestZTGuardEmptySubnet(t *testing.T) {
	sys := newFakeSystem()
	guard := NewZTGuard(nil)
	if !guard.Empty() {
		t.Fatalf("空网段列表应该报 Empty")
	}
	if err := guard.Install(sys); err != nil {
		t.Fatalf("空守卫不该报错: %v", err)
	}
	if len(sys.ruleList()) != 0 {
		t.Fatalf("空守卫不该装任何规则: %v", sys.ruleList())
	}
	guard.Remove(sys)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.TrimSpace(x) == s {
			return true
		}
	}
	return false
}
