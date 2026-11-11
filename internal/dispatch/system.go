package dispatch

import "slx/internal/netcfg"

// System 是分流与环路保护用到的系统操作集合。
//
// # 为什么要抽成接口
//
//  1. 让「ZT 环路保护规则不会随分流暂停而被撤掉」这种**行为**可以被单元测试验证。
//     之前 dispatch 直接调 *netcfg.System（内部 exec 真实命令），
//     只能在真机上验证，于是「暂停分流把环路保护规则一起撤了」这种致命 bug
//     一路逃到了线上（表现为：出口 agent 再也连不回网关 → 没有出口 →
//     分流永远被暂停 → 死锁）。
//  2. 让 dispatch 包不再绑死在 netcfg 的具体实现上。
//
// 生产代码照旧传 *netcfg.System —— 它满足这个接口（有编译期断言兜底）。
type System interface {
	// —— ipset ——

	// IPSetCreate 创建集合（幂等）。
	IPSetCreate(name string, maxElem int) error
	// IPSetSwapReplace 用新内容原子替换集合。
	IPSetSwapReplace(name string, cidrs []string) (int, error)
	// IPSetFlush 清空集合但保留它。
	IPSetFlush(name string)
	// IPSetAddBatch 批量添加，返回成功与失败条数。
	IPSetAddBatch(name string, cidrs []string) (ok, fail int)
	// IPSetCount 返回条目数，集合不存在时返回 -1。
	IPSetCount(name string) int
	// IPSetDestroy 删除集合。
	IPSetDestroy(name string)

	// —— 策略路由 ——

	// EnsureIPRule 确保存在一条 ip rule，返回是否新增。
	EnsureIPRule(priority int, selector, table string) (bool, error)
	// RemoveIPRule 删除一条 ip rule（不存在则忽略）。
	RemoveIPRule(priority int, selector, table string)
	// IPRuleExists 判断某条 ip rule 是否存在。
	IPRuleExists(priority int, selector, table string) bool
	// EnsureRuleInTable 在指定路由表里确保一条路由。
	EnsureRuleInTable(table int, dst, dev string) (bool, error)
	// RouteInTableExists 判断指定路由表里是否有某条路由。
	RouteInTableExists(table int, dst, dev string) bool
	// ClearTable 清空一个自定义路由表。
	ClearTable(table int)

	// —— mangle 标记 ——

	// EnsureMarkRule 确保存在一条打标记的 mangle 规则。
	EnsureMarkRule(chain, inIface string, mark uint32, cnSet, exemptSet, comment string) (bool, error)
	// RemoveMarkRule 删除带指定注释的 mangle 规则。
	RemoveMarkRule(chain, comment string)
	// HasMarkRule 判断某条链里是否已有带指定注释的规则。
	HasMarkRule(chain, comment string) bool
}

// 编译期断言：生产用的 netcfg.System 必须满足 System。
//
// 少一个方法都会在这里直接编译失败，而不是等到线上才发现规则装不上。
var _ System = (*netcfg.System)(nil)
