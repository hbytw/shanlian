// Package relay 实现 proxy 模式的传输层：出口 agent 反向拨号到网关，
// 在一条 TLS 连接上用 yamux 多路复用，网关按需开流请求 agent 建立连接。
//
// 与 packet 模式的关系：两者是并行的数据面实现，由配置 transport_mode 选择。
// packet 模式转发裸 IP 包（需要出口 root+TUN+NAT）；relay 模式终结连接后中继
// （出口零权限、无需公网 IP）。
package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

// ProtoVersion 是 relay 握手协议版本。
const ProtoVersion uint8 = 1

// nonceLen 是握手随机数长度。
const nonceLen = 16

// macLen 是 HMAC-SHA256 输出长度。
const macLen = 32

// maxNodeIDLen 是 node_id 长度上限（节点 ID 是定长 hex，这里放宽不重要）。
const maxNodeIDLen = 128

// clockSkew 是允许的对端时钟偏差。
//
// 为什么需要它：握手带时间戳防重放，但两端时钟不可能完全一致。
// 60 秒足以覆盖常见的 NTP 漂移，又不会给重放留太大窗口。
//
// 实测（2026-11-11）：路由器与各 agent 的时钟差 ≤ 8 秒，
// 且**时钟差与传输路径无关**（ZeroTier 直连一样），
// 所以公网直连通道不会让偏差变大 —— 默认值保持 60 秒，只做成可配置。
//
// 用 SetClockSkew 调整（配置项 relay_clock_skew）。
var clockSkew = 60 * time.Second

// SetClockSkew 设置握手允许的时钟偏差。
//
// 非正值忽略（设 0 会让所有握手失败）。
func SetClockSkew(d time.Duration) {
	if d > 0 {
		clockSkew = d
	}
}

// ClockSkew 返回当前生效的时钟偏差上限。
func ClockSkew() time.Duration { return clockSkew }

// ClockSkewWarn 是时钟差的告警阈值。
//
// 超过它意味着「握手还过得去，但已经值得提醒运维检查 NTP」——
// 时钟飘了会让证书校验、日志排序、轮换判断全都不准。
const ClockSkewWarn = 30 * time.Second

// ErrAuthFailed 表示密钥不匹配。
var ErrAuthFailed = errors.New("relay: 认证失败（network_key 不一致）")

// ErrClockSkew 表示对端时间戳偏差过大。
var ErrClockSkew = errors.New("relay: 对端时间戳偏差过大（检查两端系统时钟）")

// ErrBadHandshake 表示握手数据格式非法。
var ErrBadHandshake = errors.New("relay: 握手数据非法")

// mac 计算握手 MAC：HMAC-SHA256(key, nonce || ts_be64 || nodeID)。
//
// 把 nodeID 纳入 MAC 是为了防止「用 A 的握手冒充 B」——
// 否则任何持有密钥的节点都能自称是任意 node_id，选路会被劫持。
func mac(key, nonce []byte, ts int64, nodeID string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(nonce)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(ts))
	h.Write(b[:])
	h.Write([]byte(nodeID))
	return h.Sum(nil)
}

// ClientHandshake 是 agent 侧的握手：把自己介绍给网关。
func ClientHandshake(conn net.Conn, key []byte, nodeID string) error {
	if len(nodeID) > maxNodeIDLen {
		return fmt.Errorf("%w: node_id 过长", ErrBadHandshake)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("relay: 生成随机数失败: %w", err)
	}
	ts := time.Now().Unix()
	tag := mac(key, nonce, ts, nodeID)

	// 帧：[ver 1][nonce 16][ts 8][mac 32][idLen 2][nodeID]
	var hdr [1 + nonceLen + 8 + macLen + 2]byte
	hdr[0] = ProtoVersion
	p := 1
	copy(hdr[p:], nonce)
	p += nonceLen
	binary.BigEndian.PutUint64(hdr[p:], uint64(ts))
	p += 8
	copy(hdr[p:], tag)
	p += macLen
	binary.BigEndian.PutUint16(hdr[p:], uint16(len(nodeID)))

	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(nodeID)); err != nil {
		return err
	}

	// 读网关的回应：1 字节结果 + 可选原因
	var resp [1]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("relay: 读取握手结果失败: %w", err)
	}
	if resp[0] != 1 {
		reason := make([]byte, 256)
		n, _ := conn.Read(reason)
		return fmt.Errorf("%w: %s", ErrAuthFailed, string(reason[:n]))
	}
	return nil
}

// ServerHandshake 是网关侧的握手：校验 agent 身份，返回其 node_id。
func ServerHandshake(conn net.Conn, key []byte) (string, error) {
	nodeID, _, err := ServerHandshakeWithSkew(conn, key)
	return nodeID, err
}

// ServerHandshakeWithSkew 与 ServerHandshake 相同，但额外返回观察到的对端时钟偏差。
//
// 时钟偏差用于监控：偏差超过 ClockSkewWarn（30 秒）时握手虽然还能过，
// 但已经值得提醒运维检查 NTP —— 时钟飘了会让证书校验、日志排序、
// 证书轮换判断全都不准。
func ServerHandshakeWithSkew(conn net.Conn, key []byte) (string, time.Duration, error) {
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetDeadline(time.Time{})

	var hdr [1 + nonceLen + 8 + macLen + 2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrBadHandshake, err)
	}
	if hdr[0] != ProtoVersion {
		reject(conn, fmt.Sprintf("不支持的协议版本 %d（本端 %d）", hdr[0], ProtoVersion))
		return "", 0, fmt.Errorf("%w: 版本 %d", ErrBadHandshake, hdr[0])
	}
	p := 1
	nonce := hdr[p : p+nonceLen]
	p += nonceLen
	ts := int64(binary.BigEndian.Uint64(hdr[p:]))
	p += 8
	gotMAC := hdr[p : p+macLen]
	p += macLen
	idLen := int(binary.BigEndian.Uint16(hdr[p:]))

	if idLen > maxNodeIDLen {
		reject(conn, "node_id 过长")
		return "", 0, fmt.Errorf("%w: node_id 长度 %d", ErrBadHandshake, idLen)
	}
	idBuf := make([]byte, idLen)
	if _, err := io.ReadFull(conn, idBuf); err != nil {
		return "", 0, fmt.Errorf("%w: 读取 node_id 失败: %v", ErrBadHandshake, err)
	}
	nodeID := string(idBuf)

	// 时间戳检查先做：时钟偏差大时 MAC 必然也对不上，
	// 先报「时钟偏差」比报「密钥错误」更能指明真正的原因。
	skew := time.Since(time.Unix(ts, 0))
	if skew > clockSkew || skew < -clockSkew {
		reject(conn, "系统时钟偏差过大")
		return "", skew, fmt.Errorf("%w: 偏差 %s", ErrClockSkew, skew.Round(time.Second))
	}

	wantMAC := mac(key, nonce, ts, nodeID)
	if !hmac.Equal(gotMAC, wantMAC) {
		reject(conn, "密钥不匹配")
		return "", skew, ErrAuthFailed
	}

	if _, err := conn.Write([]byte{1}); err != nil {
		return "", skew, err
	}
	return nodeID, skew, nil
}

// reject 回一个带原因的拒绝，让对端日志能直接说明白。
func reject(conn net.Conn, reason string) {
	if len(reason) > 255 {
		reason = reason[:255]
	}
	_, _ = conn.Write(append([]byte{0}, reason...))
}

// TLSConfigForServer 生成网关侧 TLS 配置。
//
// 用运行期生成的自签名证书：TLS 在这里只负责加密与防窃听，
// 身份认证由上面的 HMAC 握手完成（network_key 即凭证）。
// 这样不需要 CA、不需要证书分发，符合「零配置」目标。
func TLSConfigForServer() (*tls.Config, error) {
	cert, err := selfSignedCert(1)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// TLSConfigForClient 是 agent 侧配置：不校验证书链（靠 HMAC 认证身份）。
func TLSConfigForClient() *tls.Config {
	return &tls.Config{
		// 证书是自签且每次启动重新生成，无法做链校验；
		// 真正的身份认证在 HMAC 握手里，密钥不对连 yamux 都进不去。
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	}
}

// selfSignedCert 生成一张自签名证书。
//
// days 是有效期（天）。现状写死 24 小时 —— 每次进程启动重新生成，
// 指纹每次都变、证书一天就过期（实测路由器上那张 notAfter 已是 2025-10-12）。
// 持久化 + 长有效期是直连通道「指纹钉扎」的前提，见 transport.go。
func selfSignedCert(days int) (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("relay: 生成密钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "slx-relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, days),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("relay: 签发自签证书失败: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}
