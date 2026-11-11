package netcfg

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IPSet 封装 ipset 操作。
//
// 为什么用 ipset 而不是把几千条网段直接写进 iptables 规则：
// 4300+ 条规则会让每个包线性匹配几千次，路由器 CPU 直接跑满；
// ipset 是哈希表，O(1) 查表，且支持原子替换。

// IPSetExists 判断集合是否存在。
func (s *System) IPSetExists(name string) bool {
	_, err := s.runCmd("ipset", "list", "-n", name)
	return err == nil
}

// IPSetCreate 创建 hash:net 集合（幂等）。
func (s *System) IPSetCreate(name string, maxElem int) error {
	if s.IPSetExists(name) {
		return nil
	}
	if maxElem <= 0 {
		maxElem = 200000
	}
	args := []string{"create", name, "hash:net", "family", "inet",
		"maxelem", strconv.Itoa(maxElem)}
	if out, err := s.runCmd("ipset", args...); err != nil {
		return fmt.Errorf("创建 ipset %s 失败: %v (%s)\n"+
			"提示: 需要内核 ip_set 支持；OpenWrt 执行 opkg install ipset", name, err, out)
	}
	return nil
}

// IPSetAdd 向集合添加网段（幂等：已存在不报错）。
func (s *System) IPSetAdd(name, cidr string) error {
	if _, err := s.runCmd("ipset", "add", name, cidr, "-exist"); err != nil {
		return fmt.Errorf("向 %s 添加 %s 失败: %v", name, cidr, err)
	}
	return nil
}

// IPSetAddBatch 批量添加，失败只记数量不中断。
//
// 返回成功与失败条数。单条失败通常是格式问题，不应让整个分流失败。
func (s *System) IPSetAddBatch(name string, cidrs []string) (ok, fail int) {
	for _, c := range cidrs {
		if _, err := s.runCmd("ipset", "add", name, c, "-exist"); err != nil {
			fail++
			continue
		}
		ok++
	}
	return ok, fail
}

// IPSetSwapReplace 原子地用新内容替换集合。
//
// 做法：建一个临时集合装满 → ipset swap 两个集合 → 删掉临时集合。
// 这样在替换瞬间查表也永远是完整的，不会出现「清空了正在重填导致大量误判」
// 的窗口期 —— 那个窗口哪怕只有 100ms，也足以让上千个连接走错出口。
func (s *System) IPSetSwapReplace(name string, cidrs []string) (int, error) {
	tmp := name + "_new"
	if s.IPSetExists(tmp) {
		_, _ = s.runCmd("ipset", "destroy", tmp)
	}
	if err := s.IPSetCreate(tmp, len(cidrs)+1024); err != nil {
		return 0, err
	}
	ok, _ := s.IPSetAddBatch(tmp, cidrs)

	if !s.IPSetExists(name) {
		// 目标集合还不存在，直接把临时集合改名过来
		if _, err := s.runCmd("ipset", "rename", tmp, name); err != nil {
			return ok, fmt.Errorf("重命名 ipset 失败: %v", err)
		}
		return ok, nil
	}
	if _, err := s.runCmd("ipset", "swap", tmp, name); err != nil {
		return ok, fmt.Errorf("交换 ipset 失败: %v", err)
	}
	_, _ = s.runCmd("ipset", "destroy", tmp)
	return ok, nil
}

// IPSetCount 返回集合内的条目数。
func (s *System) IPSetCount(name string) int {
	out, err := s.runCmd("ipset", "list", "-t", name)
	if err != nil {
		return -1
	}
	// 输出里有一行 "Number of entries: N"
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Number of entries:") {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Number of entries:")))
			return n
		}
	}
	return -1
}

// IPSetDestroy 删除集合（不存在则忽略）。
func (s *System) IPSetDestroy(name string) {
	_, _ = s.runCmd("ipset", "destroy", name)
}

// IPSetFlush 清空集合但保留它。
func (s *System) IPSetFlush(name string) {
	_, _ = s.runCmd("ipset", "flush", name)
}

// —— 策略路由 ——

// EnsureIPRule 确保存在一条 ip rule（幂等）。
//
// priority 越小优先级越高。返回是否新增。
func (s *System) EnsureIPRule(priority int, selector, table string) (bool, error) {
	// 先查是否已存在完全相同的规则
	out, err := s.runCmd(s.IP, "rule", "show")
	if err == nil {
		want := strings.Join([]string{selector, "lookup", table}, " ")
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, strconv.Itoa(priority)+":") && strings.Contains(line, want) {
				return false, nil
			}
		}
	}
	// 注意：selector 可能是 "fwmark 0x100" 这样的多词串，
	// 必须拆成独立 argv。整串传进去 ip 会当成一个参数解析失败，
	// 现象是规则压根没装上、但也没报错（踩过）。
	args := []string{"rule", "add"}
	if sel := strings.Fields(selector); len(sel) > 0 {
		args = append(args, sel...)
	}
	args = append(args, "lookup", table, "priority", strconv.Itoa(priority))
	if out, err := s.runCmd(s.IP, args...); err != nil {
		return false, fmt.Errorf("添加 ip rule 失败: %v (%s)", err, out)
	}
	return true, nil
}

// RemoveIPRule 删除一条 ip rule（不存在则忽略）。
func (s *System) RemoveIPRule(priority int, selector, table string) {
	args := []string{"rule", "del"}
	if sel := strings.Fields(selector); len(sel) > 0 {
		args = append(args, sel...)
	}
	args = append(args, "lookup", table, "priority", strconv.Itoa(priority))
	_, _ = s.runCmd(s.IP, args...)
}

// IPRuleExists 判断某条 ip rule 是否存在。
func (s *System) IPRuleExists(priority int, selector, table string) bool {
	out, err := s.runCmd(s.IP, "rule", "show")
	if err != nil {
		return false
	}
	want := strings.Join([]string{selector, "lookup", table}, " ")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, strconv.Itoa(priority)+":") && strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// RouteInTableExists 判断指定路由表里是否有某条路由。
func (s *System) RouteInTableExists(table int, dst, dev string) bool {
	out, err := s.runCmd(s.IP, "route", "show", "table", strconv.Itoa(table))
	if err != nil {
		return false
	}
	if dst == "" {
		dst = "default"
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), dst) && strings.Contains(line, "dev "+dev) {
			return true
		}
	}
	return false
}

// EnsureRuleInTable 在指定路由表里确保一条路由（幂等）。
func (s *System) EnsureRuleInTable(table int, dst, dev string) (bool, error) {
	if s.RouteInTableExists(table, dst, dev) {
		return false, nil
	}
	d := dst
	if d == "" {
		d = "default"
	}
	if out, err := s.runCmd(s.IP, "route", "add", d, "dev", dev, "table", strconv.Itoa(table)); err != nil {
		if strings.Contains(out, "File exists") {
			return false, nil
		}
		return false, fmt.Errorf("添加路由 %s 到表 %d 失败: %v (%s)", d, table, err, out)
	}
	return true, nil
}

// ClearTable 清空一个自定义路由表。
//
// ⚠️ 整表 flush：会连带清掉别人往这张表里加的路由。
//
//	撤自己的规则请用 RemoveTableRoute（只删自己那一条）。
func (s *System) ClearTable(table int) {
	_, _ = s.runCmd(s.IP, "route", "flush", "table", strconv.Itoa(table))
}

// —— mangle 标记 ——

// EnsureMarkRule 在 mangle 表指定链里确保一条按「目标不在集合内」打标记的规则。
//
// 语义：目标地址既不在 cnSet 也不在 exemptSet 里 → 打 mark。
//
// 为什么两个集合都要排除：
//   - cnSet 是「中国大陆网段」，这些走原路（分流的核心）；
//   - exemptSet 是「绝不能进隧道的网段」（私有地址、隧道内层网段、ZT 网段）。
//     少了第二项就会出现 DESIGN.md §7.2 那个死循环：ZT 的私有地址不在国内网段里，
//     会被判成境外送进隧道，而隧道本身又要走 ZT 出去。
func (s *System) EnsureMarkRule(chain, inIface string, mark uint32, cnSet, exemptSet, comment string) (bool, error) {
	// 先检查已存在（按 comment 识别）
	cname := markOf(comment)
	if s.hasRuleWithComment("mangle", chain, cname) {
		return false, nil
	}

	args := []string{"-t", "mangle", "-I", chain, "1"}
	if inIface != "" {
		args = append(args, "-i", inIface)
	}
	args = append(args,
		"-m", "set", "!", "--match-set", cnSet, "dst",
		"-m", "set", "!", "--match-set", exemptSet, "dst",
		"-m", "comment", "--comment", cname,
		"-j", "MARK", "--set-xmark", fmt.Sprintf("0x%x", mark))

	if out, err := s.runCmd(s.IPTables, args...); err != nil {
		return false, fmt.Errorf("添加分流标记规则失败: %v (%s)", err, out)
	}
	return true, nil
}

// RemoveMarkRule 删除带指定 comment 的 mangle 规则。
//
// # 为什么不能自己拼 -D 规格
//
// 真实装上去的规则长这样：
//
//	-A PREROUTING -i br-lan -m set ! --match-set slx_cn dst
//	  -m set ! --match-set slx_exempt dst -m comment --comment "slx:split-mark-br-lan"
//	  -j MARK --set-xmark 0x100/0xffffffff
//
// 自己拼一个「只有 comment + MARK」的 -D 规格，iptables 一定匹配不上
// ⇒ 删除被拒绝 ⇒ 而错误又被丢掉 ⇒ **规则永远删不掉**。
// 撤分流后 MARK 规则仍在，后续 ipset destroy 会因「集合仍被引用」而失败。
//
// 所以改成：**扫链（-S）拿到真实规则，再原样 -D** —— 与 CleanupMarked 同一套路。
func (s *System) RemoveMarkRule(chain, comment string) {
	s.removeRulesWithComment("mangle", chain, markOf(comment))
}

// removeRulesWithComment 扫链，把带指定 comment 的规则逐条删掉，返回删除条数。
func (s *System) removeRulesWithComment(table, chain, comment string) int {
	out, err := s.runCmd(s.IPTables, "-t", table, "-S", chain)
	if err != nil {
		return 0
	}
	removed := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"`+comment+`"`) {
			continue
		}
		// 把 "-A CHAIN ..." 变成 "-D CHAIN ..."，其余原样保留。
		del := strings.Replace(line, "-A "+chain, "-D "+chain, 1)
		args := append([]string{"-t", table}, splitArgs(del)...)
		if _, derr := s.runCmd(s.IPTables, args...); derr == nil {
			removed++
		}
	}
	return removed
}

// RemoveTableRoute 只删除路由表里**指定的那一条**路由，不动表里的其它条目。
//
// 为什么需要它：ClearTable 是整表 flush，会把别人往这张表里加的路由一并冲掉。
// 撤分流时应该只删自己那条（default dev slx0）。
func (s *System) RemoveTableRoute(table int, dst, dev string) bool {
	args := []string{"route", "del"}
	if dst != "" {
		args = append(args, dst)
	}
	if dev != "" {
		args = append(args, "dev", dev)
	}
	args = append(args, "table", strconv.Itoa(table))
	_, err := s.runCmd(s.IP, args...)
	return err == nil
}

// HasMarkRule 判断某条链里是否已有带指定注释的规则（供看门狗使用）。
func (s *System) HasMarkRule(chain, comment string) bool {
	return s.hasRuleWithComment("mangle", chain, markOf(comment))
}

// hasRuleWithComment 判断某条链里是否已有带指定注释的规则。
func (s *System) hasRuleWithComment(table, chain, comment string) bool {
	out, err := s.runCmd(s.IPTables, "-t", table, "-S", chain)
	if err != nil {
		return false
	}
	return strings.Contains(out, `"`+comment+`"`)
}

// IPSetLoadWait 等待 ipset 命令可用（OpenWrt 上 ipset 可能刚装好）。
func (s *System) IPSetLoadWait(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := s.runCmd("ipset", "version"); err == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
