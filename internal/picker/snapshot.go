package picker

import (
	"time"
)

// RouteView 是路由表里一条记录的可展示形式。
type RouteView struct {
	Prefix string  `json:"prefix"`
	Egress string  `json:"egress"`
	AgeSec int     `json:"age_sec"`
	Score  float64 `json:"score"`
}

// Snapshot 是选路状态的可序列化快照，供 `slx status` / `slx routes` / `slx explain` 读取。
//
// 为什么用文件快照而不是进程内共享：
// CLI 是独立进程，而状态在守护进程内存里。用一份小 JSON 文件交换，
// 既避免了给 CLI 加 RPC 依赖，也让「事后翻看故障现场」成为可能。
type Snapshot struct {
	UpdatedAt  time.Time   `json:"updated_at"`
	Current    string      `json:"current,omitempty"`
	Candidates []Scored    `json:"candidates,omitempty"`
	Routes     []RouteView `json:"routes,omitempty"`
	// Breakers 记录处于熔断/试探状态的出口，便于快速判断"为什么没走那台"。
	Breakers map[string]string `json:"breakers,omitempty"`
}

// SnapshotState 生成当前选路状态快照。
//
// candidates 由调用方从对等表取（选路器不持有对等关系）。
func (s *Selector) SnapshotState(candidates []string, now time.Time) Snapshot {
	snap := Snapshot{UpdatedAt: now}

	s.mu.RLock()
	snap.Current = s.current
	weights := s.weights
	for _, id := range candidates {
		rtt, bw, loss, aff, has := s.metricsForLocked(id, "")
		if !has {
			snap.Candidates = append(snap.Candidates, Scored{
				NodeID: id, Eligible: true, Note: "尚无测量数据",
			})
			continue
		}
		sc := Score(rtt, bw, loss, aff, weights)
		st := Scored{
			NodeID: id, Score: round1(sc), RTTMS: round1(rtt),
			BWMbps: round1(bw), Loss: round3(loss), Affinity: aff, Eligible: true,
			TargetFailures: s.targetFailures[id],
			LinkFailures:   s.linkFailures[id],
			LinkReason:     s.linkReason[id],
		}
		// L1（网关↔出口）与 L2（出口↔公网）两套独立计数汇总成一个结论。
		view := s.breakerViewLocked(id, now)
		if view.name != "" {
			st.Breaker = view.name
		}
		if view.name != "正常" {
			st.BreakerReason = view.reason
			if view.name == "熔断" {
				st.Eligible = false
				st.Note = "熔断中，恢复时间 " + view.until.Format("15:04:05")
				if snap.Breakers == nil {
					snap.Breakers = make(map[string]string)
				}
				snap.Breakers[id] = view.until.Format("15:04:05")
			} else if view.name == "试探" {
				st.Note = "冷却结束，正在试探"
			}
		} else if view.reason != "" {
			// 没熔断，但最近一次探测失败过：写进备注，
			// 让出口表能看出「这台上次没连上公网」。
			st.Note = "上次探测失败：" + view.reason
		}
		snap.Candidates = append(snap.Candidates, st)
	}
	s.mu.RUnlock()

	t := s.table
	t.mu.RLock()
	for _, e := range t.entries {
		if now.Sub(e.added) > routeTTL {
			continue
		}
		snap.Routes = append(snap.Routes, RouteView{
			Prefix: e.prefix.String(), Egress: e.egress,
			AgeSec: int(now.Sub(e.added).Seconds()), Score: round1(e.score),
		})
	}
	t.mu.RUnlock()
	return snap
}

// Entries 返回路由表所有未过期条目（诊断用）。
func (t *RouteTable) Entries(now time.Time) []RouteView {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]RouteView, 0, len(t.entries))
	for _, e := range t.entries {
		if now.Sub(e.added) > routeTTL {
			continue
		}
		out = append(out, RouteView{
			Prefix: e.prefix.String(), Egress: e.egress,
			AgeSec: int(now.Sub(e.added).Seconds()), Score: round1(e.score),
		})
	}
	return out
}
