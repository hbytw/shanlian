package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"slx/internal/logx"
	"slx/internal/mesh"
	"slx/internal/picker"
	"slx/internal/proto"
)

// 探测参数。
const (
	// l1Interval 是网格层探测频率。
	//
	// 2 秒足够快：出口抖动后 2 秒内就能反映到评分上，
	// 又不至于把几台 VPS 之间都打成高频流量。
	l1Interval = 2 * time.Second

	// l1Timeout 是单次网格探测的等待上限。
	l1Timeout = 2 * time.Second

	// l2Interval 是出口层探测频率。L2 要真的下载数据，不能太频繁。
	l2Interval = 60 * time.Second

	// l2Bytes 是 L2 吞吐测量读取的字节数。
	l2Bytes = 2 << 20 // 2 MB

	// l2Timeout 是单次 L2 探测的总超时。
	l2Timeout = 20 * time.Second
)

// prober 负责两层测量并把结果喂给选路器。
//
// # 为什么分两层
//
//	L1（网关↔出口）：每 2 秒，轻量，只测 RTT 与丢包
//	L2（出口→目标）：每 60 秒，要真下载，测 RTT 与吞吐
//
// 只测 L1 不知道 VPS 到网站快不快；只测 L2 不知道家到 VPS 的链路好坏。
type prober struct {
	disc   *mesh.Discovery
	pick   *picker.Selector
	log    *slog.Logger
	selfID string

	// pending 记录发出但还没回应的 L1 探测（出口 node_id → 发出时刻）
	pending sync.Map

	// l2Last 记录每个 (出口, 目标) 上次探测的时刻，避免重复探测
	l2Mu   sync.Mutex
	l2Last map[string]time.Time

	// l2Targets 是当前希望测量的目标（由 L1 层或用户触发）
	l2Targets map[string]bool
}

// newProber 创建探测器。
func newProber(disc *mesh.Discovery, pick *picker.Selector, log *slog.Logger, selfID string) *prober {
	if log == nil {
		log = logx.Discard()
	}
	return &prober{
		disc:      disc,
		pick:      pick,
		log:       log,
		selfID:    selfID,
		l2Last:    make(map[string]time.Time),
		l2Targets: make(map[string]bool),
	}
}

// AddL2Target 把某个域名加入出口层探测目标。
//
// 设计上 L2 应该探测「用户当前实际在访问的域名」，而不是写死测速站。
// 这里用注册域（如 googlevideo.com）而不是完整域名，
// 因为 CDN 会把同一个站点的内容分散到大量子域，逐个测没有意义。
func (p *prober) AddL2Target(domain string) {
	d := registrableDomain(domain)
	if d == "" {
		return
	}
	p.l2Mu.Lock()
	added := !p.l2Targets[d]
	if added && len(p.l2Targets) < 32 { // 上限，防止目标无限膨胀
		p.l2Targets[d] = true
	}
	p.l2Mu.Unlock()
	if added {
		p.log.Info("新增出口层探测目标",
			logx.Event("probe.target_added"), "domain", d,
			logx.Hint("按实际访问的站点测量，比写死测速站更贴近真实体验"))
	}
}

// Start 启动探测。
//
// 同时承担两个角色，因为同一套代码既可能是网关也可能是出口：
//   - 探测发起方（网关）：定期向各出口发 ping、收结果、更新评分
//   - 探测响应方（出口）：收到请求就替网关测一次并回结果
func (p *prober) Start(ctx context.Context) {
	p.disc.SetProbeHandlers(p.onProbeReq, p.onProbeResp)
	go p.l1Loop(ctx)
	go p.l2Loop(ctx)
}

// l1Loop 每 2 秒向所有候选出口发一次轻量探测。
func (p *prober) l1Loop(ctx context.Context) {
	t := time.NewTicker(l1Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sendL1()
		}
	}
}

// sendL1 向所有候选出口发探测。
func (p *prober) sendL1() {
	tbl := p.disc.Table()
	cands := tbl.EgressCandidates(time.Now())
	if len(cands) == 0 {
		return
	}
	req := proto.ProbeReq{Reason: "l1"} // Target 为空 = 只测链路
	for _, c := range cands {
		if c.NodeID == p.selfID {
			continue
		}
		addr := tbl.AddrOf(c.NodeID)
		if addr == nil {
			continue
		}
		p.pending.Store(c.NodeID, time.Now())
		if err := p.disc.SendControl(addr, proto.MsgProbeReq, req); err != nil {
			p.pending.Delete(c.NodeID)
		}
	}
}

// onProbeResp 处理收到的探测结果。
func (p *prober) onProbeResp(resp proto.ProbeResp, from *net.UDPAddr) {
	now := time.Now()

	// 找到这个地址对应的节点
	nodeID := ""
	tbl := p.disc.Table()
	for _, c := range tbl.Snapshot() {
		if a := tbl.AddrOf(c.NodeID); a != nil && a.String() == from.String() {
			nodeID = c.NodeID
			break
		}
	}
	if nodeID == "" {
		return
	}

	if resp.Target == "" {
		// L1 结果：用我们自己测的往返时间，比对方填的字段更可信
		v, ok := p.pending.LoadAndDelete(nodeID)
		if !ok {
			return
		}
		sent := v.(time.Time)
		rttMS := float64(now.Sub(sent).Milliseconds())
		if rttMS <= 0 {
			rttMS = 0.1
		}
		p.pick.UpdateL1(nodeID, rttMS, 0, 0, now)
		// ★ 链路信号 ⇒ 不推进熔断计数 ✅✅（见 picker.ReportLinkHealth ✅✅）
		p.pick.ReportLinkHealth(nodeID, true, "", now)
		return
	}

	// L2 结果：对方已经测好，直接采纳
	if resp.Err != "" {
		p.pick.UpdateL2(nodeID, registrableDomain(resp.Target), 0, 1, 0, false, now)
		return
	}
	bwMbps := resp.BytesPerSec * 8 / 1e6
	p.pick.UpdateL2(nodeID, registrableDomain(resp.Target),
		resp.RTTMS, resp.LossRate, bwMbps, false, now)
}

// onProbeReq 处理对端发来的探测请求（本节点作为出口时）。
func (p *prober) onProbeReq(req proto.ProbeReq, from *net.UDPAddr) {
	if req.Target == "" {
		// L1 探测：立刻原样回，让对方自己算往返时间。
		// 用对方的时钟算比用我们的更准，也不需要时钟同步。
		_ = p.disc.SendControl(from, proto.MsgProbeResp, proto.ProbeResp{})
		return
	}
	// L2 探测：真的去连一下。放在 goroutine 里，别堵住读循环。
	go func(target string) {
		resp := probeL2(target, l2Bytes, l2Timeout)
		_ = p.disc.SendControl(from, proto.MsgProbeResp, resp)
	}(req.Target)
}

// l2Loop 定期对所有登记的目标做出口层实测。
func (p *prober) l2Loop(ctx context.Context) {
	t := time.NewTicker(l2Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.runL2Round()
		}
	}
}

// runL2Round 让每个出口都测一遍当前关注的目标。
func (p *prober) runL2Round() {
	p.l2Mu.Lock()
	targets := make([]string, 0, len(p.l2Targets))
	for d := range p.l2Targets {
		targets = append(targets, d)
	}
	p.l2Mu.Unlock()
	if len(targets) == 0 {
		return
	}

	tbl := p.disc.Table()
	cands := tbl.EgressCandidates(time.Now())
	for _, c := range cands {
		if c.NodeID == p.selfID {
			continue
		}
		addr := tbl.AddrOf(c.NodeID)
		if addr == nil {
			continue
		}
		for _, target := range targets {
			_ = p.disc.SendControl(addr, proto.MsgProbeReq, proto.ProbeReq{
				Target: target, Bytes: l2Bytes, Reason: "l2",
			})
		}
	}
}

// —— 出口侧的实测实现 ——

// probeL2 从本机（出口）实测一个目标：解析 → 建连 → 下载 → 算吞吐。
//
// 关键点：**用本机 DNS 解析**。
// 这样才能拿到贴合本机所在地的 CDN 地址；如果用网关那边的解析结果，
// 测出来的是「跨区 CDN」的速度，跟用户实际体验对不上。
func probeL2(target string, wantBytes int, timeout time.Duration) proto.ProbeResp {
	resp := proto.ProbeResp{Target: target}
	deadline := time.Now().Add(timeout)

	host, port := splitHostPortDefault(target, "443")
	if host == "" {
		resp.Err = "目标为空"
		return resp
	}

	// 1. 解析（用本机 DNS）
	resolveStart := time.Now()
	ips, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil || len(ips) == 0 {
		resp.Err = "解析失败: " + errStr(err)
		return resp
	}
	resp.ResolvedIPs = ips
	_ = resolveStart

	addr := net.JoinHostPort(ips[0], port)

	// 2. TCP 建连（测 RTT）
	connStart := time.Now()
	d := net.Dialer{Timeout: time.Until(deadline)}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		resp.Err = "建连失败: " + errStr(err)
		resp.RTTMS = float64(time.Since(connStart).Milliseconds())
		return resp
	}
	defer raw.Close()
	resp.RTTConnectMS = float64(time.Since(connStart).Milliseconds())
	resp.RTTMS = resp.RTTConnectMS

	// 3. 走 TLS（443）或明文（80 等），再测下载吞吐
	var r io.Reader = raw
	if port == "443" {
		tc := tls.Client(raw, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, // 只测速，不校验身份
			MinVersion:         tls.VersionTLS12,
		})
		if err := tc.Handshake(); err != nil {
			// TLS 失败不算致命：至少 TCP 的 RTT 已经测到了
			resp.Err = ""
			return resp
		}
		defer tc.Close()
		r = tc
	}

	// 4. 读一批数据测吞吐
	_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 32<<10)
	var got int
	readStart := time.Now()
	for got < wantBytes {
		n, rerr := r.Read(buf)
		got += n
		if rerr != nil {
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	elapsed := time.Since(readStart).Seconds()
	if elapsed > 0 && got > 0 {
		resp.BytesPerSec = float64(got) / elapsed
	}
	return resp
}

// splitHostPortDefault 拆 host:port，没有端口时用默认值。
func splitHostPortDefault(target, defPort string) (host, port string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ""
	}
	if h, p, err := net.SplitHostPort(target); err == nil {
		return h, p
	}
	return target, defPort
}

// registrableDomain 粗略取注册域（最后两段）。
//
// 不做完整的公共后缀解析（那要维护一张 PSL 表，对路由判断没必要），
// 目的只是把 a.googlevideo.com 和 b.googlevideo.com 归成同一组。
func registrableDomain(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// 去掉末尾的点
	host = strings.TrimSuffix(host, ".")
	if net.ParseIP(host) != nil {
		return host
	}
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	// 处理常见双段后缀（co.uk / com.cn 等）
	second := parts[len(parts)-2]
	if len(second) <= 3 {
		if _, err := strconv.Atoi(second); err != nil {
			return strings.Join(parts[len(parts)-3:], ".")
		}
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func errStr(err error) string {
	if err == nil {
		return "未知错误"
	}
	return err.Error()
}

var _ = fmt.Sprintf
