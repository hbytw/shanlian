package mesh

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestBroadcastAddr(t *testing.T) {
	cases := []struct{ cidr, want string }{
		{"192.168.99.1/24", "192.168.99.255"},
		{"10.0.0.5/8", "10.255.255.255"},
		{"172.16.5.9/16", "172.16.255.255"},
		{"192.168.1.254/24", "192.168.1.255"},
	}
	for _, c := range cases {
		got, err := broadcastAddr(c.cidr)
		if err != nil {
			t.Errorf("%s: 意外错误 %v", c.cidr, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("%s: 广播地址 = %s，期望 %s", c.cidr, got, c.want)
		}
	}
}

func TestBroadcastAddrRejectsIPv6(t *testing.T) {
	if _, err := broadcastAddr("fd00::1/64"); err == nil {
		t.Error("IPv6 应被拒绝（当前发现只做 IPv4）")
	}
}

func TestSubnetHosts(t *testing.T) {
	hosts, err := subnetHosts("192.168.99.1/24", 254)
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	// /24 有 254 个可用主机地址（去掉网络号与广播地址）
	if len(hosts) != 254 {
		t.Errorf("主机数 = %d，期望 254", len(hosts))
	}
	if hosts[0].String() != "192.168.99.1" {
		t.Errorf("首个主机 = %s，期望 192.168.99.1", hosts[0])
	}
	if hosts[len(hosts)-1].String() != "192.168.99.254" {
		t.Errorf("末个主机 = %s，期望 192.168.99.254", hosts[len(hosts)-1])
	}
}

func TestSubnetHostsRespectsLimit(t *testing.T) {
	hosts, err := subnetHosts("192.168.99.1/24", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 10 {
		t.Errorf("应被限制到 10 个，得到 %d", len(hosts))
	}
}

func TestSubnetHostsRejectsHugeSubnet(t *testing.T) {
	// /8 有 1600 万地址，扫描它没有意义且会拖垮网络
	if _, err := subnetHosts("10.0.0.1/8", 254); err == nil {
		t.Error("超大子网应被拒绝")
	}
}

func TestPeerStateTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		ago  time.Duration
		want State
	}{
		{0, StateAlive},
		{AliveWithin - time.Second, StateAlive},
		{AliveWithin + time.Second, StateStale},
		{StaleWithin - time.Second, StateStale},
		{StaleWithin + time.Second, StateDown},
	}
	for _, c := range cases {
		p := &Peer{LastSeen: now.Add(-c.ago)}
		if got := p.State(now); got != c.want {
			t.Errorf("距上次消息 %s: 状态 = %v，期望 %v", c.ago, got, c.want)
		}
	}
}

func TestPeerTableUpsert(t *testing.T) {
	tb := NewPeerTable()

	isNew := tb.Upsert("node-a", func(p *Peer, isNew bool) { p.Region = "eu" })
	if !isNew {
		t.Error("首次插入应标记为新增")
	}
	isNew = tb.Upsert("node-a", func(p *Peer, isNew bool) { p.Region = "us" })
	if isNew {
		t.Error("二次插入不应标记为新增")
	}

	p, ok := tb.Get("node-a")
	if !ok {
		t.Fatal("节点应存在")
	}
	if p.Region != "us" {
		t.Errorf("Region 应被更新为 us，得到 %s", p.Region)
	}
	if tb.Len() != 1 {
		t.Errorf("表大小 = %d，期望 1", tb.Len())
	}
}

func TestPeerTableRejectsEmptyID(t *testing.T) {
	tb := NewPeerTable()
	if tb.Upsert("", func(p *Peer, isNew bool) {}) {
		t.Error("空 node_id 不应被插入")
	}
	if tb.Len() != 0 {
		t.Error("表应保持为空")
	}
}

func TestPeerTableSnapshotIsCopy(t *testing.T) {
	tb := NewPeerTable()
	tb.Upsert("a", func(p *Peer, _ bool) { p.Capabilities = []string{"egress"} })

	snap := tb.Snapshot()
	snap[0].Capabilities[0] = "tampered"
	snap[0].NodeID = "hacked"

	again := tb.Snapshot()
	if again[0].Capabilities[0] != "egress" {
		t.Error("快照应返回拷贝，内部状态不应被外部修改")
	}
	if again[0].NodeID != "a" {
		t.Error("NodeID 不应被外部修改")
	}
}

func TestPeerTablePrune(t *testing.T) {
	tb := NewPeerTable()
	now := time.Now()
	tb.upsertAt("old", now.Add(-time.Hour), func(p *Peer, _ bool) {})
	tb.upsertAt("fresh", now, func(p *Peer, _ bool) {})

	n := tb.Prune(now, 10*time.Minute)
	if n != 1 {
		t.Errorf("应清理 1 个，得到 %d", n)
	}
	if _, ok := tb.Get("old"); ok {
		t.Error("旧节点应被清理")
	}
	if _, ok := tb.Get("fresh"); !ok {
		t.Error("新节点应保留")
	}
}

func TestEgressCandidatesFilters(t *testing.T) {
	tb := NewPeerTable()
	now := time.Now()
	tb.upsertAt("egress-alive", now, func(p *Peer, _ bool) {
		p.Capabilities = []string{"egress"}
	})
	tb.upsertAt("egress-stale", now.Add(-AliveWithin-time.Second), func(p *Peer, _ bool) {
		p.Capabilities = []string{"egress"}
	})
	tb.upsertAt("no-egress-cap", now, func(p *Peer, _ bool) {
		p.Capabilities = []string{"gateway"}
	})

	cands := tb.EgressCandidates(now)
	if len(cands) != 1 {
		t.Fatalf("出口候选应为 1 个，得到 %d", len(cands))
	}
	if cands[0].NodeID != "egress-alive" {
		t.Errorf("候选 = %s，期望 egress-alive", cands[0].NodeID)
	}
}

func TestStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	tb := NewPeerTable()
	addr, _ := net.ResolveUDPAddr("udp", "192.168.99.24:51820")
	tb.Upsert("peer-1", func(p *Peer, _ bool) {
		p.ZTAddr = addr
		p.AdvertiseAddr = "129.225.146.84:51820"
		p.Capabilities = []string{"egress", "probe"}
		p.Transports = []string{"zt", "direct"}
		p.Region = "eu"
		p.AgreedProto = 1
		p.SWVersion = "1.0.0"
	})

	if err := tb.SaveState(path, "self-1"); err != nil {
		t.Fatalf("SaveState 失败: %v", err)
	}

	restored := NewPeerTable()
	n := restored.LoadState(path, "self-1", StateMaxAge)
	if n != 1 {
		t.Fatalf("恢复节点数 = %d，期望 1", n)
	}
	p, ok := restored.Get("peer-1")
	if !ok {
		t.Fatal("节点未恢复")
	}
	if p.AdvertiseAddr != "129.225.146.84:51820" {
		t.Errorf("AdvertiseAddr 丢失: %q", p.AdvertiseAddr)
	}
	if p.ZTAddr == nil || p.ZTAddr.String() != "192.168.99.24:51820" {
		t.Errorf("ZTAddr 丢失: %v", p.ZTAddr)
	}
	if len(p.Capabilities) != 2 {
		t.Errorf("Capabilities 丢失: %v", p.Capabilities)
	}

	// 关键：恢复出来的节点必须是「迟滞」而非「在线」，
	// 否则选路会选中一个其实已经没了的出口。
	if got := p.State(time.Now()); got == StateAlive {
		t.Errorf("恢复的节点不应被标为在线，得到 %v", got)
	}
}

func TestLoadStateRejectsWrongSelf(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	tb := NewPeerTable()
	tb.Upsert("peer-1", func(p *Peer, _ bool) {})
	if err := tb.SaveState(path, "self-1"); err != nil {
		t.Fatal(err)
	}

	// 另一个节点读到别人的缓存，必须拒绝（否则会恢复出错误拓扑）
	other := NewPeerTable()
	if n := other.LoadState(path, "self-2", StateMaxAge); n != 0 {
		t.Errorf("不应恢复他人缓存，得到 %d", n)
	}
}

func TestLoadStateHandlesCorruption(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	// 写一个坏文件：缓存损坏必须降级为「重新发现」，绝不能崩溃
	if err := writeFile(path, []byte("{ this is not json")); err != nil {
		t.Fatal(err)
	}
	tb := NewPeerTable()
	if n := tb.LoadState(path, "self-1", StateMaxAge); n != 0 {
		t.Errorf("坏缓存应返回 0，得到 %d", n)
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	tb := NewPeerTable()
	if n := tb.LoadState(t.TempDir()+"/nope.json", "self", StateMaxAge); n != 0 {
		t.Errorf("缺失文件应返回 0，得到 %d", n)
	}
}

func TestLoadStateExpired(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	tb := NewPeerTable()
	tb.Upsert("peer-1", func(p *Peer, _ bool) {})
	if err := tb.SaveState(path, "self-1"); err != nil {
		t.Fatal(err)
	}

	// 用一个「已经过期」的 maxAge 加载
	restored := NewPeerTable()
	if n := restored.LoadState(path, "self-1", time.Nanosecond); n != 0 {
		t.Errorf("过期缓存应返回 0，得到 %d", n)
	}
}

func TestClearState(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	tb := NewPeerTable()
	if err := tb.SaveState(path, "self"); err != nil {
		t.Fatal(err)
	}
	if err := ClearState(path); err != nil {
		t.Errorf("ClearState 失败: %v", err)
	}
	// 再删一次不应报错
	if err := ClearState(path); err != nil {
		t.Errorf("删除不存在的缓存不应报错: %v", err)
	}
}

// writeFile 是测试辅助，避免测试文件里到处出现 os.WriteFile 的样板。
func writeFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}

func TestLoadSnapshotKeepsRealState(t *testing.T) {
	// LoadState（守护进程用）应把恢复的节点标成迟滞；
	// LoadSnapshot（CLI 展示用）应保留真实时间，从而正确显示在线。
	dir := t.TempDir()
	path := dir + "/state.json"

	tb := NewPeerTable()
	tb.Upsert("peer-1", func(p *Peer, _ bool) {})
	if err := tb.SaveState(path, "self-1"); err != nil {
		t.Fatal(err)
	}

	daemon := NewPeerTable()
	if n := daemon.LoadState(path, "self-1", StateMaxAge); n != 1 {
		t.Fatalf("守护进程应恢复 1 个节点，得到 %d", n)
	}
	if p, _ := daemon.Get("peer-1"); p.State(time.Now()) == StateAlive {
		t.Error("守护进程恢复的节点不应标为在线")
	}

	view := NewPeerTable()
	if n := view.LoadSnapshot(path, "self-1", StateMaxAge); n != 1 {
		t.Fatalf("快照应读出 1 个节点，得到 %d", n)
	}
	if p, _ := view.Get("peer-1"); p.State(time.Now()) != StateAlive {
		t.Errorf("快照应保留真实状态（在线），得到 %v", p.State(time.Now()))
	}
}
