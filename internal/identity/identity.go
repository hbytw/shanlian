// Package identity 从「网络密钥 + 本机指纹」确定性地派生节点身份与传输密钥。
//
// 为什么不用配置文件存 node_id：OpenWrt 的闪存只有 200MB 可用，且频繁写入会磨损。
// 确定性派生让节点身份在重启、重装后保持稳定，同时不写任何状态文件。
package identity

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// NodeIDLen 是 node_id 的字节长度。8 字节 = 64 bit，碰撞概率对本场景足够低。
const NodeIDLen = 8

// Fingerprint 采集一组能区分本机的稳定标识。
//
// 按可靠性排序，能取到多少用多少：machine-id 最稳，
// 网卡 MAC 次之，hostname 兜底。
func Fingerprint() string {
	var parts []string

	// 1) systemd machine-id（最稳定，重装才变）
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				parts = append(parts, "mid:"+s)
				break
			}
		}
	}

	// 2) OpenWrt 特有：/etc/config/system 里的主机名与密钥
	if b, err := os.ReadFile("/etc/config/system"); err == nil {
		h := sha256.Sum256(b)
		parts = append(parts, "owrt:"+hex.EncodeToString(h[:8]))
	}

	// 3) 主网卡 MAC（排除虚拟接口）
	if mac := primaryMAC(); mac != "" {
		parts = append(parts, "mac:"+mac)
	}

	// 4) 兜底
	if h, err := os.Hostname(); err == nil && h != "" {
		parts = append(parts, "host:"+h)
	}

	if len(parts) == 0 {
		// 极端情况：什么都取不到。返回空串让上层报错，而不是悄悄用随机值。
		return ""
	}
	return strings.Join(parts, "|")
}

// primaryMAC 取一个物理网卡的 MAC。
//
// 排除 zt*/tun*/tap*/docker*/br-*/veth*/tailscale* 等虚拟接口，
// 因为它们的 MAC 会随重启或重建而变。
func primaryMAC() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	virtual := []string{"zt", "tun", "tap", "docker", "br-", "veth", "tailscale", "lo", "sit", "gre", "ip6", "dummy"}
	for _, e := range entries {
		name := e.Name()
		skip := false
		for _, p := range virtual {
			if strings.HasPrefix(name, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		b, err := os.ReadFile("/sys/class/net/" + name + "/address")
		if err != nil {
			continue
		}
		mac := strings.TrimSpace(string(b))
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		return mac
	}
	return ""
}

// DeriveNodeID 从 network_key 与本机指纹确定性派生 node_id（16 位 hex）。
//
// 同机同密钥恒定；换密钥即换身份（符合直觉：换网络就是新节点）。
func DeriveNodeID(networkKey []byte) (string, error) {
	fp := Fingerprint()
	if fp == "" {
		return "", fmt.Errorf("无法采集本机指纹（machine-id / 网卡 MAC / hostname 都取不到），无法派生节点身份")
	}
	h, err := hkdf.Key(sha256.New, networkKey, []byte(fp), "slx-v1 node-id", NodeIDLen)
	if err != nil {
		return "", fmt.Errorf("派生 node_id 失败: %w", err)
	}
	return hex.EncodeToString(h), nil
}

// 传输密钥的方向标识。
const (
	DirC2S = "c2s" // 网关 → 出口
	DirS2C = "s2c" // 出口 → 网关
)

// DeriveSessionKeys 为「本节点 ↔ 对端」派生一对方向独立的 AEAD 密钥。
//
// 仅用于 direct 传输（不经 ZeroTier 时）。走 ZT 时由 ZT 负责加密，无需重复。
//
// 设计要点：
//   - 双向密钥不同，避免 nonce 复用导致的安全问题；
//   - salt 绑定双方 node_id，使每条链路密钥唯一；
//   - info 带协议版本，将来换版本可平滑共存。
func DeriveSessionKeys(networkKey []byte, selfID, peerID string) (c2s, s2c []byte, err error) {
	a, b := selfID, peerID
	// 保证 (A,B) 与 (B,A) 派生出同一组基础材料，再按方向拆分。
	if a > b {
		a, b = b, a
	}
	salt := []byte(a + "|" + b)

	base, err := hkdf.Key(sha256.New, networkKey, salt, "slx-v1 session", 64)
	if err != nil {
		return nil, nil, fmt.Errorf("派生会话密钥失败: %w", err)
	}
	// 身份较小的一端使用前 32 字节作为 c2s。
	if selfID == a {
		return base[:32], base[32:], nil
	}
	return base[32:], base[:32], nil
}

// Nonce 构造 12 字节 AEAD nonce（96 bit，ChaCha20-Poly1305 标准长度）。
//
// 高 4 字节放方向标识，低 8 字节放单调递增包序号。
// 同一方向内序号不复用，即可保证 nonce 永不重复。
func Nonce(direction uint32, seq uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint32(n[0:4], direction)
	binary.BigEndian.PutUint64(n[4:12], seq)
	return n
}
