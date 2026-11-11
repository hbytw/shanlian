package relay

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transport 是一条 relay 传输通道的抽象。
//
// # 为什么要有这一层
//
// 现状只有一条路：agent 反向拨网关的 ZeroTier 地址，TCP 被包在 ZT 的 UDP 里。
// 跨境 UDP 有 2% 丢包（实测）且有被限流/阻断的风险，
// 而 TCP+TLS 是最难被针对性限流的形态。
//
// 新方向：**网关主动拨 agent 的公网 IP**（网关在 CGNAT 后没有入站，
// 只出不进；agent 有公网 IPv4），ZeroTier 反向连接保留为兜底通道。
//
// 两种传输的差别只在「怎么把字节送过去」，
// 上层（握手 → HMAC 认证 → yamux → 业务流）完全一致，
// 所以抽象点放在 net.Conn 这一层最省事。
type Transport interface {
	// Name 返回通道名："zt" / "tls"。
	Name() string

	// Dial 主动建立一条到对端的连接。
	//
	// 直连通道（tls）在这里完成 TLS 握手 + 指纹钉扎校验。
	Dial(ctx context.Context, addr string) (net.Conn, error)

	// Listen 监听并接受连接。
	//
	// 直连通道（tls）在这里是 TLS 服务端（agent 侧）。
	Listen(ctx context.Context, addr string) (net.Listener, error)
}

// ZTTransport 是 ZeroTier 私网通道：TCP + TLS，保持现状行为。
//
// 现状（agent → 网关）：agent 用 TLSConfigForClient 拨，
// 网关用 TLSConfigForServer 听。身份由 network_key 的 HMAC 握手保证，
// 私网通道不做指纹钉扎。
type ZTTransport struct {
	// Server 为 true 时 Listen 使用 TLS 服务端配置（网关侧）。
	Server bool
}

// Name 实现 Transport。
func (t ZTTransport) Name() string { return "zt" }

// Dial 实现 Transport。
func (t ZTTransport) Dial(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second}
	if t.Server {
		// 网关主动拨 agent 的 ZeroTier 地址（备用路径）。
		return d.DialContext(ctx, "tcp", addr)
	}
	return tls.DialWithDialer(d, "tcp", addr, TLSConfigForClient())
}

// Listen 实现 Transport。
func (t ZTTransport) Listen(ctx context.Context, addr string) (net.Listener, error) {
	if !t.Server {
		return nil, errors.New("relay: ZT 通道的 agent 侧不监听")
	}
	cfg, err := TLSConfigForServer()
	if err != nil {
		return nil, err
	}
	return tls.Listen("tcp", addr, cfg)
}

// TLSTransport 是公网直连通道：TCP + TLS + 指纹钉扎。
//
// 角色：**网关是客户端**（拨 agent 的公网 IP），**agent 是服务端**（听 :8443）。
// 与 ZT 通道相反 —— 因为网关在 CGNAT 后没有入站，只能出站。
//
// 端口默认 8443：agent 的公网 IP 上跑着 nginx，443 已被占。
// 端口号在 DPI 眼里没有区分度，抗识别靠 SNI + 证书 + 流量特征，不是端口。
type TLSTransport struct {
	// Server 为 true 时 Listen 使用 TLS 服务端配置（agent 侧）。
	Server bool

	// Pin 是期望的服务端证书指纹（SHA-256，hex，可带冒号）。
	//
	// 只在客户端侧生效。为空时 Dial 直接失败 ——
	// 没有钉扎的公网 TLS 等于明文，宁可不用这条通道。
	Pin string

	// CertFile 是服务端证书路径（agent 侧）。
	//
	// 必须是**持久化**的：现状 selfSignedCert() 每次启动重新生成，
	// notAfter 只有 24 小时（实测路由器上那张早已过期），
	// 指纹每次都变 → 钉扎必然失效。
	CertFile string

	// CertDays 是服务端证书有效期（天）。
	CertDays int
}

// Name 实现 Transport。
func (t TLSTransport) Name() string { return "tls" }

// Dial 实现 Transport（网关侧：拨 agent 的公网 IP）。
func (t TLSTransport) Dial(ctx context.Context, addr string) (net.Conn, error) {
	cfg, err := TLSConfigForPinnedClient(t.Pin)
	if err != nil {
		return nil, err
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: cfg}
	return d.DialContext(ctx, "tcp", addr)
}

// Listen 实现 Transport（agent 侧：监听公网 :8443）。
func (t TLSTransport) Listen(ctx context.Context, addr string) (net.Listener, error) {
	if !t.Server {
		return nil, errors.New("relay: TLS 通道的网关侧不监听")
	}
	cert, err := LoadOrCreateCert(t.CertFile, t.CertDays)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	return tls.Listen("tcp", addr, cfg)
}

// ErrNoPin 表示没有提供证书指纹，拒绝建立公网直连。
//
// 钉扎是强制的：没有指纹就不开通道 A，宁可继续用 ZeroTier 兜底通道。
var ErrNoPin = errors.New("relay: 没有证书指纹，拒绝建立公网直连通道")

// TLSConfigForPinnedClient 生成带**指纹钉扎**的客户端 TLS 配置。
//
// 与 TLSConfigForClient（InsecureSkipVerify）的区别：
// 现状那条用于私网反向通道 —— 私网 + HMAC 握手已经够了。
// 公网直连必须钉扎：中间人拿不到 network_key 就过不了 relay 握手，
// 但如果 TLS 层不校验证书，中间人至少能**看到并转发**流量（流量劫持/窃听）。
//
// 校验方式：VerifyPeerCertificate 里比对叶证书 DER 的 SHA-256。
// 不用 CA、不用主机名 —— 自签证书没有 CA 可言。
func TLSConfigForPinnedClient(pin string) (*tls.Config, error) {
	want, err := NormalizeFingerprint(pin)
	if err != nil {
		return nil, err
	}
	if want == "" {
		return nil, ErrNoPin
	}
	return &tls.Config{
		// 自签证书无法做链校验，但**必须**由 VerifyPeerCertificate 兜住。
		InsecureSkipVerify: true, //nolint:gosec // 校验在下面的回调里做
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("relay: 对端没有提供证书")
			}
			sum := sha256.Sum256(rawCerts[0])
			got := hex.EncodeToString(sum[:])
			if !strings.EqualFold(got, want) {
				// 只回前缀，避免把完整指纹写进日志。
				return fmt.Errorf("relay: 证书指纹不匹配（期望 %s…，实际 %s…）",
					shortFingerprint(want), shortFingerprint(got))
			}
			return nil
		},
	}, nil
}

// NormalizeFingerprint 把指纹统一成小写无冒号的 hex。
//
// 允许的输入形式："ab:cd:ef"、"abcdef"、"AB-CD"、"sha256=ab:cd"。
// 空串返回空串（表示没配）。
func NormalizeFingerprint(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	s = strings.TrimPrefix(strings.ToLower(s), "sha256=")
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	if len(s) != 64 {
		return "", fmt.Errorf("relay: 证书指纹长度不对（%d，应为 64 个 hex 字符）", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("relay: 证书指纹不是合法的 hex: %w", err)
	}
	return s, nil
}

// CertFingerprint 返回证书的 SHA-256 指纹（小写 hex，无冒号）。
func CertFingerprint(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// shortFingerprint 把指纹缩成前 8 位，供日志使用（不泄露完整指纹）。
func shortFingerprint(fp string) string {
	if len(fp) <= 8 {
		return fp
	}
	return fp[:8]
}

// DefaultCertFile 是 relay 证书的默认存放路径。
const DefaultCertFile = "/etc/slx/relay-cert.pem"

// DefaultCertDays 是 relay 自签证书的默认有效期（天）。
//
// 现状是 24 小时（每次进程启动重新生成），实测路由器上那张早已过期 ——
// 直连通道要钉扎指纹，指纹必须稳定，所以改成持久化 + 长有效期。
const DefaultCertDays = 3650

// certRenewBefore 是证书剩余有效期低于这个值时的续期阈值。
const certRenewBefore = 30 * 24 * time.Hour

// LoadOrCreateCert 读取持久化证书；不存在或剩余有效期不足 30 天就重新生成。
//
// 私钥与证书写在同一个 PEM 文件里，权限 0600。
func LoadOrCreateCert(path string, days int) (tls.Certificate, error) {
	if path == "" {
		path = DefaultCertFile
	}
	if days <= 0 {
		days = DefaultCertDays
	}
	if cert, ok := loadCertIfValid(path); ok {
		return cert, nil
	}
	return createAndSaveCert(path, days)
}

// loadCertIfValid 读证书；剩余有效期不足 30 天时返回 false。
func loadCertIfValid(path string) (tls.Certificate, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, false
	}
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return tls.Certificate{}, false
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, false
	}
	if time.Until(leaf.NotAfter) < certRenewBefore {
		return tls.Certificate{}, false
	}
	return cert, true
}

// createAndSaveCert 生成证书并原子写入 path。
func createAndSaveCert(path string, days int) (tls.Certificate, error) {
	cert, err := selfSignedCert(days)
	if err != nil {
		return tls.Certificate{}, err
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	kb, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	b = append(b, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})...)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return tls.Certificate{}, err
	}
	return cert, nil
}

// TLSConfigForServerWithCert 生成网关侧 TLS 配置，证书持久化到 certFile。
//
// certFile 为空时退回运行期生成的临时证书（= 现状行为）。
func TLSConfigForServerWithCert(certFile string) (*tls.Config, error) {
	if certFile == "" {
		return TLSConfigForServer()
	}
	cert, err := LoadOrCreateCert(certFile, DefaultCertDays)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
