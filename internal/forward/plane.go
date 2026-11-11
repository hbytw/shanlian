// Package forward 实现数据面。
//
// 数据流（双向，两端各做一次 SNAT）：
//
//	去程：PC → 网关内核策略路由 → TUN → 加帧 → UDP(ZeroTier) → 出口
//	      → 出口 TUN → 出口内核转发 + MASQUERADE → 互联网
//	回程：互联网 → 出口内核反 NAT → 出口 TUN → 加帧 → UDP → 网关 TUN
//	      → 网关内核反 NAT → PC
//
// 两次地址改写，谁来做：
//   - 网关侧：**用户态 NAT**（见 nat.go）。早期用内核 SNAT + conntrack 自动 DNAT，
//     但回程包在真实环境里会在 netfilter 的 raw/mangle 之后就消失，极难规避；
//     改由我们自己记表与还原，内核只做普通转发。
//   - 出口侧：仍用内核 MASQUERADE（这条路径验证稳定，没必要改）。
//
// 角色判定不靠配置，只看目的地址：
//   - 目的地址落在内层网段 → 这是回程包，送回对应网关；
//   - 否则 → 这是要去互联网的包，交给出口。
//
// 因此同一个 Plane 既能当网关也能当出口，一台机器可同时承担两种角色。
package forward

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"slx/internal/logx"
	"slx/internal/mesh"
	"slx/internal/picker"
	"slx/internal/tun"
)

// Plane 是本节点的数据面。
type Plane struct {
	dev  *tun.Device
	disc *mesh.Discovery
	log  *slog.Logger

	// innerNet 是隧道内层网段，用于区分「回程包」与「去程包」。
	innerNet *net.IPNet

	// nat 是网关侧的用户态地址转换表。为 nil 表示本节点不承担网关职责，
	// 数据面按纯出口处理。
	nat *NAT

	// 网关角色：出口选择。
	//
	// fixedEgress 非空表示用户在配置里写死了出口，直接用，不参与自动选路。
	// 否则交给 pick 按实测评分选（含滞回、熔断、路由表粘性）。
	//
	// 地址不缓存而是每包现查：peer 地址会随重连变化，
	// 查表成本只是一次读锁 + map 查找，比维护缓存失效逻辑便宜得多。
	egressID    string
	fixedEgress string
	selfID      string
	pick        *picker.Selector

	// candsBuf 复用候选列表切片，避免每包分配。
	// 只在 Run 这一个 goroutine 里使用，无需加锁。
	candsBuf []string

	// 出口角色：内层源地址 → 网关隧道地址。
	innerToPeer map[string]*net.UDPAddr
	mu          sync.RWMutex

	Stats Stats
}

// Options 构造 Plane。
type Options struct {
	Dev       *tun.Device
	Discovery *mesh.Discovery
	Log       *slog.Logger
	// InnerCIDR 是隧道内层网段，如 "10.254.0.0/24"。
	InnerCIDR string
	// EgressID 非空时固定用这个出口；为空则每包自动挑第一个可用出口。
	EgressID string
	// SelfID 是本节点 ID，用于自动挑选出口时排除自己。
	SelfID string
	// NAT 是网关侧的用户态地址转换表；nil 表示不启用（纯出口节点）。
	NAT *NAT
	// Picker 是选路器；nil 表示不自动选路，退化为「取第一个可用出口」。
	Picker *picker.Selector
	// FixedEgress 非空表示用户写死了出口（配置里的 egress 字段）。
	FixedEgress string
}

// New 创建数据面。
func New(o Options) (*Plane, error) {
	if o.Dev == nil {
		return nil, fmt.Errorf("数据面需要 TUN 设备")
	}
	if o.Discovery == nil {
		return nil, fmt.Errorf("数据面需要已建立的发现通道")
	}
	if o.Log == nil {
		o.Log = logx.Discard()
	}
	_, ipnet, err := net.ParseCIDR(o.InnerCIDR)
	if err != nil {
		return nil, fmt.Errorf("内层网段 %q 非法: %w", o.InnerCIDR, err)
	}

	p := &Plane{
		dev:         o.Dev,
		disc:        o.Discovery,
		log:         o.Log,
		innerNet:    ipnet,
		egressID:    o.EgressID,
		selfID:      o.SelfID,
		nat:         o.NAT,
		pick:        o.Picker,
		fixedEgress: o.FixedEgress,
		innerToPeer: make(map[string]*net.UDPAddr),
	}

	return p, nil
}

// IsGateway 表示本节点是否具备网关能力（有指定出口）。
func (p *Plane) IsGateway() bool { return p.egressID != "" }

// egressAddr 现查出口地址。
//
// 优先级：
//  1. 用户在配置里写死的出口；
//  2. 选路器按实测评分选出的出口（含滞回 / 熔断 / 路由表粘性）；
//  3. 兜底：取第一个可用候选。
//
// 每包现查而不是缓存：出口可能随时变化（重连、切换、上线），
// 一次读锁 + map 扫描的成本远低于维护缓存失效逻辑的复杂度与出错风险。
func (p *Plane) egressAddr() *net.UDPAddr {
	return p.egressAddrFor(nil, time.Now())
}

// egressAddrFor 为指定目的地选出口。dst 可为 nil（表示不看目的地）。
func (p *Plane) egressAddrFor(dst net.IP, now time.Time) *net.UDPAddr {
	t := p.disc.Table()
	if p.fixedEgress != "" {
		return t.AddrOf(p.fixedEgress)
	}
	if p.pick == nil {
		return t.FirstEgressAddr(p.selfID, now)
	}

	// 复用切片，避免每包分配
	p.candsBuf = p.candsBuf[:0]
	for _, c := range t.EgressCandidates(now) {
		if c.NodeID != p.selfID {
			p.candsBuf = append(p.candsBuf, c.NodeID)
		}
	}
	if len(p.candsBuf) == 0 {
		return nil
	}
	d := p.pick.Pick(dst, "", p.candsBuf, now)
	if d.Egress == "" {
		return nil
	}
	return t.AddrOf(d.Egress)
}

// HasEgress 报告当前是否有可用出口。
func (p *Plane) HasEgress() bool { return p.egressAddr() != nil }

// isInner 判断地址是否在内层网段。
func (p *Plane) isInner(ip net.IP) bool { return p.innerNet.Contains(ip) }

// OnTunnelData 处理从隧道收到的包。
//
// 在发现模块的读 goroutine 里调用，必须够快：只做一次 TUN 写入。
func (p *Plane) OnTunnelData(pkt []byte, from *net.UDPAddr) {
	if isIPv6(pkt) {
		p.Stats.BadPacket.Add(1)
		return
	}
	src, dst, ok := ipv4Info(pkt)
	if !ok {
		p.Stats.BadPacket.Add(1)
		return
	}

	// 目的地址不在内层网段 → 这是要转发到互联网的包，记下来源网关，
	// 否则回程时不知道送回哪里。
	if !p.isInner(dst) {
		p.remember(src.String(), from)
	} else if p.nat != nil {
		// 回程包：目的地址本来是本节点的内层地址，这里还原成客户端的真实地址。
		// 还原之后内核只需要做一次普通转发（slx0 → LAN），不涉及 NAT。
		if !p.nat.MapInbound(pkt, time.Now()) {
			p.Stats.NoRoute.Add(1)
			return
		}
	}

	if _, err := p.dev.Write(pkt); err != nil {
		p.Stats.SendErr.Add(1)
		return
	}
	p.Stats.TunWrite.Add(1)
	p.Stats.TunBytesWrite.Add(uint64(len(pkt)))
}

// remember 记录内层地址 → 网关隧道地址。
func (p *Plane) remember(innerIP string, addr *net.UDPAddr) {
	p.mu.RLock()
	cur, ok := p.innerToPeer[innerIP]
	p.mu.RUnlock()
	if ok && cur.String() == addr.String() {
		return
	}
	p.mu.Lock()
	p.innerToPeer[innerIP] = addr
	p.mu.Unlock()
	p.log.Info("登记网关",
		logx.Event("forward.gateway_registered"),
		"inner_ip", innerIP, "tunnel_addr", addr.String(),
		logx.Hint("该地址的回程流量将被送回到这里"))
}

// peerFor 根据内层目的地址找网关隧道地址。
func (p *Plane) peerFor(innerIP string) (*net.UDPAddr, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a, ok := p.innerToPeer[innerIP]
	return a, ok
}

// Run 启动 TUN → 隧道方向，阻塞到 ctx 结束。
func (p *Plane) Run(ctx context.Context) error {
	buf := make([]byte, tun.MaxPacket)
	var local localCounter

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	go func() {
		gcTick := time.NewTicker(30 * time.Second)
		defer gcTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				local.publish(&p.Stats.Sent, &p.Stats.TunBytesRead)
			case now := <-gcTick.C:
				// 回收空闲表项：低端路由器内存有限，必须定期清理。
				if p.nat != nil {
					if n := p.nat.GC(now); n > 0 {
						p.log.Debug("回收 NAT 表项", logx.Event("forward.nat_gc"), "count", n)
					}
				}
			}
		}
	}()

	var last logThrottle
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := p.dev.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("读取 TUN %s 失败: %w", p.dev.Name(), err)
		}
		if n == 0 {
			continue
		}
		pkt := buf[:n]

		if n > tun.DefaultMTU+64 {
			p.Stats.Oversize.Add(1)
			last.warn(p.log, "丢弃超大包", "size", n)
			continue
		}
		if isIPv6(pkt) {
			// 内核会在新建的 TUN 上自发 IPv6 链路本地流量（NDP/DAD），
			// 属于正常现象，只计数不刷日志，否则日志会被淹掉。
			p.Stats.BadPacket.Add(1)
			continue
		}
		_, dst, ok := ipv4Info(pkt)
		if !ok {
			p.Stats.BadPacket.Add(1)
			continue
		}

		var target *net.UDPAddr
		if p.isInner(dst) {
			// 回程包：出自出口内核的反 NAT，要送回发起它的网关
			peer, found := p.peerFor(dst.String())
			if !found {
				p.Stats.NoRoute.Add(1)
				last.warn(p.log, "找不到回程网关", "inner_dst", dst.String(),
					logx.Hint("该网关还没发过包，或状态已过期"))
				continue
			}
			target = peer
		} else {
			// 去程包：要送去互联网
			eg := p.egressAddrFor(dst, time.Now())
			if eg == nil {
				p.Stats.NoRoute.Add(1)
				last.warn(p.log, "出口不可用，丢弃去程包", "dst", dst.String(),
					logx.FieldEgress, p.egressID)
				continue
			}
			target = eg
			// 用户态 NAT：把源地址/端口改写成内层地址/自有端口，
			// 出口才能把它 NAT 出去，回程也才有东西可以还原。
			if p.nat != nil && !p.nat.MapOutbound(pkt, time.Now()) {
				p.Stats.BadPacket.Add(1)
				continue
			}
		}

		if err := p.disc.SendData(target, pkt); err != nil {
			p.Stats.SendErr.Add(1)
			last.warn(p.log, "发送失败", "dst", target.String(), logx.FieldErr, err.Error())
			continue
		}
		local.packets++
		local.bytes += uint64(n)
		p.Stats.TunRead.Add(1)
	}
}

// KnownGateways 返回已登记的网关数量（诊断用）。
func (p *Plane) KnownGateways() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.innerToPeer)
}

// logThrottle 限制同一条日志的输出频率，避免故障时刷爆日志与闪存。
type logThrottle struct {
	lastAt time.Time
	count  int
}

func (t *logThrottle) warn(log *slog.Logger, msg string, kv ...any) {
	t.count++
	if !t.lastAt.IsZero() && time.Since(t.lastAt) < 10*time.Second {
		return
	}
	suppressed := t.count - 1
	args := append([]any{logx.Event("forward.warn"), "suppressed", suppressed}, kv...)
	log.Warn(msg, args...)
	t.lastAt = time.Now()
	t.count = 0
}
