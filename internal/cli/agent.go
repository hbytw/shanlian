package cli

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"slx/internal/buildinfo"
	"slx/internal/envdetect"
	"slx/internal/identity"
	"slx/internal/logx"
	"slx/internal/relay"
)

// Agent 实现 `slx agent`：以「出口 agent」身份反向连接网关。
//
// 与 `slx run` 的区别：
//   - 不建 TUN、不碰 iptables/rp_filter、不需要 root；
//   - 主动拨出到网关，因此本机不需要公网 IP，也不需要 ZeroTier 可达；
//   - 工作内容只有 net.Dial + 字节中继。
//
// 用法：
//
//	slx agent --gateway 192.168.1.254:11901
//	slx agent --gateway gw.example.com:11901 --config /etc/slx/config.yaml
func Agent(args []string) int {
	cfgPath := configPath(args)
	c, res, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 1
	}

	// --gateway 优先于配置文件里的 gateway_addr
	gw := flagString(args, "--gateway", "-g")
	if gw == "" {
		gw = c.EffGatewayAddr()
	}
	if gw == "" {
		fmt.Fprintf(os.Stderr, "缺少网关地址。\n\n用法: slx agent --gateway <网关IP:端口>\n"+
			"也可以在配置里写 gateway_addr\n")
		return 2
	}
	// 允许只写主机名/IP，端口用默认值
	if !strings.Contains(gw, ":") {
		gw = gw + ":11901"
	}

	log := logx.New(logx.Options{Level: c.EffLogLevel(), Format: c.EffLogFormat()})

	nodeID, err := identity.DeriveNodeID(c.KeyBytes())
	if err != nil {
		log.Error("节点身份派生失败",
			logx.FieldEvent, "identity.derive_failed",
			logx.FieldErr, err.Error(),
			logx.FieldHint, "确认 /etc/machine-id 或至少一块物理网卡存在")
		return 1
	}

	if res.Created {
		log.Info("首次运行，已生成配置文件", logx.FieldEvent, "config.created",
			"path", c.Path,
			logx.FieldHint, "把 network_key 复制到其它节点: slx config --key")
	}

	log.Info("出口 agent 启动",
		logx.FieldEvent, "agent.startup",
		"version", buildinfo.String(),
		logx.FieldNodeID, nodeID,
		"gateway", gw,
		"detail", "本机不需要 root、不需要 TUN、不需要公网 IP")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// —— 公网直连通道（可选）——
	//
	// 监听地址留空 = 不启用（默认，行为与改造前完全一致，不扩大安全面）。
	// 启用后：agent 监听 TLS（默认端口 8443，因为 443 被 nginx 占着），
	// 并把「公网地址 + 证书指纹」经**已认证的反向连接**上报给网关，
	// 网关据此主动拨过来（网关在 CGNAT 后没有入站，只能出站）。
	tlsListen := c.EffRelayTLSListen()
	tlsPort := c.EffRelayTLSPort()

	// 上报的公网地址：配置优先，否则自动探测。
	advertise := ""
	if tlsListen != "" {
		advertise = c.EffRelayAdvertise()
		if advertise == "" {
			advertise = envdetect.PublicIPv4()
		}
		if advertise == "" {
			log.Warn("探测不到公网 IP，请显式配置 relay_advertise",
				logx.FieldEvent, "relay.public_ip_unknown",
				logx.FieldHint, "在 /etc/config/slx 里设置 relay_advertise 为 <公网IP>")
		}
	}

	// 证书先落盘：指纹要稳定（网关靠它钉扎），
	// 而且上报地址必须带上指纹，所以顺序不能反。
	certFingerprint := ""
	if tlsListen != "" {
		cert, cerr := relay.LoadOrCreateCert(c.EffRelayTLSCert(), c.EffRelayCertDays())
		if cerr != nil {
			log.Warn("准备 TLS 证书失败",
				logx.FieldEvent, "relay.cert_failed",
				logx.FieldErr, cerr.Error(),
				logx.FieldHint, "检查 /etc/slx 目录是否可写")
		} else if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
			certFingerprint = relay.CertFingerprint(leaf)
		}
	}

	a := relay.NewAgent(relay.AgentOptions{
		Key:             c.KeyBytes(),
		GatewayAddr:     gw,
		NodeID:          nodeID,
		Log:             log,
		TLSListen:       tlsListen,
		TLSCert:         c.EffRelayTLSCert(),
		TLSPort:         uint16(tlsPort),
		Advertise:       advertise,
		CertDays:        c.EffRelayCertDays(),
		Version:         buildinfo.String(),
		CertFingerprint: certFingerprint,
	})

	if tlsListen != "" {
		// 直连监听：网关会主动拨过来。
		go func() {
			if err := a.ListenDirect(ctx); err != nil {
				log.Error("公网直连监听失败",
					logx.FieldEvent, "relay.direct_listen_failed",
					logx.FieldErr, err.Error(),
					logx.FieldHint, "检查端口占用（nginx 常占 443）与防火墙")
			}
		}()
	}

	if err := a.Run(ctx); err != nil {
		log.Error("agent 退出", logx.FieldEvent, "agent.exit", logx.FieldErr, err.Error())
		return 1
	}
	log.Info("agent 已停止", logx.FieldEvent, "agent.stopped")
	return 0
}

// flagString 取出形如 `--name value` / `--name=value` / `-n value` 的参数。
func flagString(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, n+"=") {
				return strings.TrimPrefix(a, n+"=")
			}
		}
	}
	return ""
}
