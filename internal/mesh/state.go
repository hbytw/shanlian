package mesh

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// 状态缓存路径。
//
// 刻意放在 /tmp（tmpfs）而不是闪存：
//   - 你的旁路由 eMMC 只剩 200MB，且频繁小写入会磨损；
//   - 缓存的作用只是「重启后少等两秒」，丢了无害。
const (
	// DefaultStatePath 是默认状态缓存文件。
	DefaultStatePath = "/tmp/slx-state.json"
	// stateSchema 是缓存文件格式版本。
	stateSchema = 1
	// StateMaxAge 是缓存可被接受的最大年龄。
	StateMaxAge = 10 * time.Minute
)

// cachedPeer 是缓存里的一条节点记录（只保留重启后有价值的字段）。
type cachedPeer struct {
	NodeID        string    `json:"node_id"`
	ZTAddr        string    `json:"zt_addr,omitempty"`
	AdvertiseAddr string    `json:"advertise_addr,omitempty"`
	SWVersion     string    `json:"sw_version,omitempty"`
	Capabilities  []string  `json:"capabilities,omitempty"`
	Transports    []string  `json:"transports,omitempty"`
	Region        string    `json:"region,omitempty"`
	AgreedProto   int       `json:"agreed_proto,omitempty"`
	LastSeen      time.Time `json:"last_seen"`
}

// stateFile 是缓存文件结构。
type stateFile struct {
	SchemaVersion int          `json:"schema_version"`
	NodeID        string       `json:"node_id"`
	SavedAt       time.Time    `json:"saved_at"`
	Peers         []cachedPeer `json:"peers"`
}

// SaveState 把节点表写入缓存。
//
// 写入是「尽力而为」：失败只返回错误供调用方记日志，绝不影响主流程。
func (t *PeerTable) SaveState(path, selfNodeID string) error {
	if path == "" {
		path = DefaultStatePath
	}
	snap := t.Snapshot()

	sf := stateFile{
		SchemaVersion: stateSchema,
		NodeID:        selfNodeID,
		SavedAt:       time.Now(),
		Peers:         make([]cachedPeer, 0, len(snap)),
	}
	for _, p := range snap {
		cp := cachedPeer{
			NodeID:        p.NodeID,
			AdvertiseAddr: p.AdvertiseAddr,
			SWVersion:     p.SWVersion,
			Capabilities:  p.Capabilities,
			Transports:    p.Transports,
			Region:        p.Region,
			AgreedProto:   p.AgreedProto,
			LastSeen:      p.LastSeen,
		}
		if p.ZTAddr != nil {
			cp.ZTAddr = p.ZTAddr.String()
		}
		sf.Peers = append(sf.Peers, cp)
	}

	b, err := json.MarshalIndent(&sf, "", "  ")
	if err != nil {
		return err
	}

	// 原子写：先写临时文件再 rename，避免写一半掉电留下坏文件
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadState 从缓存恢复节点表（守护进程启动用），返回恢复的节点数。
//
// 恢复出来的节点被刻意标记为「迟滞」状态：
// 缓存只能证明「上次运行时它在线」，不能证明「现在还在线」。
// 若直接标成在线，选路可能选中一个其实早就没了的出口。
// 真实消息到达后会自动转回在线。
//
// 任何异常（文件不存在、格式损坏、版本不符、属于别的节点、已过期）
// 都退化为「空表 + 重新发现」，不返回错误。
// 缓存是加速手段，不是数据源——损坏了绝不能导致启动失败。
func (t *PeerTable) LoadState(path, selfNodeID string, maxAge time.Duration) int {
	return t.load(path, selfNodeID, maxAge, true)
}

// LoadSnapshot 从缓存读出一份「用于展示」的拓扑快照，返回节点数。
//
// 与 LoadState 的区别：保留缓存里记录的真实最后消息时间，
// 从而让 CLI 看到的状态（在线/迟滞/离线）反映实际情况。
// 用 LoadState 来渲染界面会导致所有节点永远显示「迟滞」——踩过。
func (t *PeerTable) LoadSnapshot(path, selfNodeID string, maxAge time.Duration) int {
	return t.load(path, selfNodeID, maxAge, false)
}

func (t *PeerTable) load(path, selfNodeID string, maxAge time.Duration, markStale bool) int {
	if path == "" {
		path = DefaultStatePath
	}
	if maxAge <= 0 {
		maxAge = StateMaxAge
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var sf stateFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return 0
	}
	if sf.SchemaVersion != stateSchema || sf.NodeID != selfNodeID {
		return 0
	}
	if time.Since(sf.SavedAt) > maxAge {
		return 0
	}

	n := 0
	for _, cp := range sf.Peers {
		if cp.NodeID == "" || cp.NodeID == selfNodeID {
			continue
		}
		if time.Since(cp.LastSeen) > maxAge {
			continue
		}
		addr := parseUDPAddr(cp.ZTAddr)

		seenAt := cp.LastSeen
		if markStale {
			seenAt = time.Now().Add(-StaleWithin / 2)
		}
		t.upsertAt(cp.NodeID, seenAt, func(p *Peer, _ bool) {
			p.ZTAddr = addr
			p.AdvertiseAddr = cp.AdvertiseAddr
			p.SWVersion = cp.SWVersion
			p.Capabilities = cp.Capabilities
			p.Transports = cp.Transports
			p.Region = cp.Region
			p.AgreedProto = cp.AgreedProto
		})
		n++
	}
	return n
}

func parseUDPAddr(s string) *net.UDPAddr {
	if s == "" {
		return nil
	}
	a, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		return nil
	}
	return a
}

// ClearState 删除缓存文件（uninstall 时调用）。
func ClearState(path string) error {
	if path == "" {
		path = DefaultStatePath
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除状态缓存失败: %w", err)
	}
	return nil
}
