// Package config 负责配置的加载、生成、校验与版本迁移。
//
// 设计原则（DESIGN.md §10）：
//
//   - 配置项只保留「无法推断」的信息：整个文件常态只有 2 项；
//   - 能推断的一律自动推断（网卡、端口、ZT 网段、iptables 变体…）；
//   - 其余可调项通过环境变量覆盖，不塞进配置文件，避免配置膨胀；
//   - 文件带 schema_version，启动时自动迁移，不因旧格式而崩溃。
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"time"
)

// CurrentSchema 是当前配置文件格式版本。
const CurrentSchema = 1

// KeyLen 是 network_key 解码后的字节长度（32 字节 = 256 bit）。
const KeyLen = 32

// DefaultPath 是默认配置路径。
const DefaultPath = "/etc/slx/config.yaml"

// File 是配置文件的内容。
//
// 注意：这里刻意保持极简。新增字段必须同时提供默认值，
// 否则会破坏「零配置启动」的承诺。
type File struct {
	// SchemaVersion 配置格式版本。缺失视为 1。
	SchemaVersion int `yaml:"schema_version"`

	// NetworkKey 节点间共享密钥（base64 编码的 32 字节）。
	// 首次启动自动生成并打印，用户复制到其它节点即可。
	NetworkKey string `yaml:"network_key"`

	// DNSListen 是本地 DNS 转发器的监听地址，如 "127.0.0.1:5335"。
	//
	// 不要用 5353：那是 mDNS 的保留端口，几乎所有 Linux（含 OpenWrt）上
	// 都被 avahi-daemon 占着，绑定会失败（真机踩到，表现为 dns.start_failed）。
	//
	// 留空表示不启用。启用后把家里路由器的 DNS 上游指向这个地址即可。
	//
	// 它做两件事：
	//   1. 境外域名经出口解析，拿到贴着出口的 CDN 地址（直接决定视频卡不卡）；
	//   2. 顺带建立「域名 ↔ 出口」亲和，避免解析在东京、流量却走法兰克福。
	//
	// 为什么不占 53 端口：那是 dnsmasq / systemd-resolved 的地盘，
	// 硬抢会破坏用户现有解析。默认 5353，由用户显式指过来。
	DNSListen string `yaml:"dns_listen,omitempty"`

	// ProbeTargets 可选探测目标。留空则从实际访问的境外域名自动推导。
	ProbeTargets []string `yaml:"probe_targets,omitempty"`

	// ---- 以下为可选覆盖项，留空即用自动推断/默认值 ----

	// LogLevel debug|info|warn|error，默认 info。
	LogLevel string `yaml:"log_level,omitempty"`
	// LogFormat json|text，默认 json。
	LogFormat string `yaml:"log_format,omitempty"`
	// ListenAddr 隧道监听地址，默认 0.0.0.0。
	ListenAddr string `yaml:"listen_addr,omitempty"`
	// ListenPort 数据面端口，0 表示自动挑选可用端口。
	ListenPort int `yaml:"listen_port,omitempty"`
	// CtrlPort 控制面端口，0 表示自动挑选。
	CtrlPort int `yaml:"ctrl_port,omitempty"`
	// CNListURL 中国大陆 IP 段来源，留空用内置默认源列表。
	CNListURL string `yaml:"cn_list_url,omitempty"`
	// ProbeBytes 单次 L2 探测下载字节数，0 表示默认。
	ProbeBytes int `yaml:"probe_bytes,omitempty"`
	// EgressCapacity 本节点作为出口时的并发流容量，0 表示按内存自动推算。
	EgressCapacity int `yaml:"egress_capacity,omitempty"`

	// PublicAddr 本节点对外可直连的公网地址（不含端口），如 "129.225.146.84"。
	//
	// 仅在使用 provider 级 NAT 的机器上需要显式填写（典型是 Oracle Cloud：
	// VNIC 只有内网地址，公网 IP 由平台 1:1 映射，本机查不到自己）。
	// 留空时程序会尝试从本机网卡自动识别公网地址；识别不到则只能经 ZeroTier 到达。
	PublicAddr string `yaml:"public_addr,omitempty"`

	// ---- 数据面相关（都有默认值，通常不用写）----

	// Egress 指定作为出口的节点 ID。留空则自动挑选第一个可用出口候选。
	//
	// M2 阶段选路还没实现，固定一个出口；M4 接入选路后此项仅作为"强制指定"用。
	Egress string `yaml:"egress,omitempty"`
	// InnerCIDR 隧道内层网段。默认 10.254.0.0/16。
	// 与现有网络冲突时改这里。
	InnerCIDR string `yaml:"inner_cidr,omitempty"`
	// TunName 隧道网卡名。默认 slx0。
	TunName string `yaml:"tun_name,omitempty"`

	// Dispatch 是否启用「境外流量走隧道、境内走原路」的分流。
	// 不写 = 自动（网关角色就启用）；显式 false 可临时关闭而不改其它配置。
	Dispatch *bool `yaml:"dispatch,omitempty"`
	// DispatchIfaces 要做分流的内网接口，如 ["br-lan"]。
	// 留空则自动识别内网侧接口。
	//
	// 刻意只在内网侧接口上打标记：出口节点从 WAN 收到的回程包
	// 目的地址是它自己的公网 IP，若不加限定会被误判成境外送进隧道。
	DispatchIfaces []string `yaml:"dispatch_ifaces,omitempty"`
	// CNListURLs 覆盖中国大陆网段的数据源。留空使用内置默认源列表
	// （实测国内可达的 ispip.clang.cn 与 APNIC）。
	CNListURLs []string `yaml:"cn_list_urls,omitempty"`

	// ---- proxy 模式（M7 架构转向，见 DESIGN.md §18）----

	// TransportMode 数据面模式：
	//   proxy  —— 反向连接 + 用户态协议栈（默认）；
	//             出口节点零权限、无需公网 IP、无需 ZeroTier 可达。
	//   packet —— 旧的裸包转发（出口需 root + TUN + NAT）。
	//             需要任意 IP 协议（IPsec 等）或源 IP 透明时用。
	TransportMode string `yaml:"transport_mode,omitempty"`

	// AgentListen 是网关侧接受 agent 反向连接的监听地址。
	// 只有网关需要；留空用默认 0.0.0.0:11901。
	AgentListen string `yaml:"agent_listen,omitempty"`

	// GatewayAddr 是出口侧 agent 要主动拨的网关地址（host:port）。
	// 只有代理模式下的出口需要；留空则该节点不启动 agent。
	GatewayAddr string `yaml:"gateway_addr,omitempty"`

	// ---- relay 双通道（见 docs/relay-dual-transport.md）----

	// RelayTransport 是 relay 的通道模式：
	//   zt   —— 只走 ZeroTier 反向通道（默认，行为与改造前完全一致）；
	//   tls  —— 只走公网直连通道（网关主动拨 agent 的公网 IP）；
	//   auto —— 按通道评分自动选（带 8 分滞回）。
	//
	// 两侧语义不同：网关侧 = 选哪条通道；agent 侧 = 要不要开 TLS 监听。
	RelayTransport string `yaml:"relay_transport,omitempty"`

	// RelayTLSPort 是 **agent 侧**的公网 TLS 监听端口，默认 8443。
	// agent 上跑着 nginx、443 已被占，所以默认不是 443。
	// 网关侧不读这一项：网关按 agent 上报的 host:port 连。
	RelayTLSPort int `yaml:"relay_tls_port,omitempty"`

	// RelayTLSListen 是 **agent 侧**的 TLS 监听地址（host:port）。
	// 留空 = 不监听（默认，不扩大 agent 的安全面）。
	RelayTLSListen string `yaml:"relay_tls_listen,omitempty"`

	// RelayTLSCert 是 TLS 证书的持久化路径（含私钥，0600）。
	// 留空用 /etc/slx/relay-cert.pem。
	RelayTLSCert string `yaml:"relay_tls_cert,omitempty"`

	// RelayAdvertise 是 **agent 侧**上报给网关的公网地址（host:port）。
	// 留空 = 自动探测公网 IP，端口取 RelayTLSPort。
	RelayAdvertise string `yaml:"relay_advertise,omitempty"`

	// RelayTLSConns 是网关侧每个 agent 的直连通道连接数，默认 4。
	// 多条独立 TCP 是缓解 TCP-in-TCP 头阻塞的关键。
	RelayTLSConns int `yaml:"relay_tls_conns,omitempty"`

	// RelayTLSConnectTimeout 是单次直连建连超时，默认 8s。
	RelayTLSConnectTimeout string `yaml:"relay_tls_connect_timeout,omitempty"`

	// RelayTLSProbeInterval 是通道探测周期，默认 30s。
	RelayTLSProbeInterval string `yaml:"relay_tls_probe_interval,omitempty"`

	// Pprof 为 true 时开启本地性能剖析端点（127.0.0.1:6060）。
	//
	// 为什么要有配置项而不只靠环境变量 SLX_PPROF：
	// OpenWrt 的 procd 对 `procd_set_param env` 是**整体替换**，
	// 同一个 instance 里写两次 env，**只有最后一次生效** ——
	// 现场就因为这个把 SLX_PPROF 丢了（/proc/<pid>/environ 里查不到）。
	// 配置项走的是配置文件，不受 procd 的 env 覆盖影响。
	Pprof bool `yaml:"slx_pprof,omitempty"`

	// RelayProbeTimeout 是单次通道探测的超时，默认 10s。
	//
	// 5 秒偏紧：实测直连 RTT 75~267ms，一次探测还要含 yamux 建流开销。
	// 宁可晚点发现坏通道，也别把好通道误杀。
	RelayProbeTimeout string `yaml:"relay_probe_timeout,omitempty"`

	// RelayChannelFailures 是通道级熔断阈值（连续失败次数），默认 5。
	RelayChannelFailures int `yaml:"relay_channel_failures,omitempty"`

	// RelayTLSPin 是手工指定的 agent 证书指纹（SHA-256 hex）。
	// 留空 = 用 agent 经已认证通道上报的指纹（TOFU）。
	RelayTLSPin string `yaml:"relay_tls_pin,omitempty"`

	// RelayClockSkew 是握手允许的时钟偏差，默认 60s。
	// 实测各节点时钟差 ≤ 8s，且与传输路径无关，默认值不用放宽。
	RelayClockSkew string `yaml:"relay_clock_skew,omitempty"`

	// RelayCertDays 是 agent 侧自签证书的有效期（天），默认 3650。
	RelayCertDays int `yaml:"relay_cert_days,omitempty"`
}

// Config 是加载后的配置，含来源路径。
type Config struct {
	File
	Path string
}

// LoadResult 描述加载过程中发生的值得告知用户的事情。
type LoadResult struct {
	// Created 表示配置文件是本次新建的。
	Created bool
	// GeneratedKey 非空表示本次新生成了密钥，需要提示用户分发。
	GeneratedKey string
	// MigratedFrom 非零表示从旧版本迁移而来。
	MigratedFrom int
}

// Load 从 path 加载配置。文件不存在时创建（含自动生成密钥）。
//
// 返回 LoadResult 让调用方可以给出恰当的提示（例如首次启动时打印密钥）。
func Load(path string) (*Config, LoadResult, error) {
	var res LoadResult
	if path == "" {
		path = DefaultPath
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		c := &Config{Path: path}
		c.SchemaVersion = CurrentSchema
		applyEnvOverrides(&c.File)
		key, err := GenerateKey()
		if err != nil {
			return nil, res, err
		}
		c.NetworkKey = key
		if err := c.Save(); err != nil {
			return nil, res, fmt.Errorf("写入新配置失败: %w", err)
		}
		res.Created = true
		res.GeneratedKey = key
		return c, res, nil
	}
	if err != nil {
		return nil, res, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}

	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, res, fmt.Errorf("解析配置 %s 失败: %w\n提示: 检查 YAML 缩进，或用 slx config --print 生成一份参考配置", path, err)
	}

	migratedFrom, err := migrate(&f)
	if err != nil {
		return nil, res, err
	}
	res.MigratedFrom = migratedFrom

	c := &Config{File: f, Path: path}
	applyEnvOverrides(&c.File)

	if c.NetworkKey == "" {
		key, err := GenerateKey()
		if err != nil {
			return nil, res, err
		}
		c.NetworkKey = key
		res.GeneratedKey = key
		if err := c.Save(); err != nil {
			return nil, res, fmt.Errorf("回写密钥失败: %w", err)
		}
	}

	if err := c.Validate(); err != nil {
		return nil, res, err
	}
	return c, res, nil
}

// applyDefaults 已废弃：默认值不再写回 File，而是由下面的 Eff* 访问器在运行时补齐。
//
// 原因：如果启动时把默认值填进 File 再保存，用户打开配置文件会看到一堆
// 他从未设置的项，直接违背「配置项只有 2 个」的初衷。
// 现在 File 严格等于「用户实际写了什么」，默认值只在运行时生效。

// 下列访问器返回「生效值」：用户设了就用用户的，没设就用默认。
// 这是「零配置启动 + 不硬编码」的实现方式：默认值集中在这里，可被配置或环境变量覆盖。

// EffLogLevel 返回生效的日志级别。
func (c *Config) EffLogLevel() string {
	if c.LogLevel != "" {
		return c.LogLevel
	}
	return "info"
}

// EffLogFormat 返回生效的日志格式。
func (c *Config) EffLogFormat() string {
	if c.LogFormat != "" {
		return c.LogFormat
	}
	return "json"
}

// EffListenAddr 返回生效的监听地址。
func (c *Config) EffListenAddr() string {
	if c.ListenAddr != "" {
		return c.ListenAddr
	}
	return "0.0.0.0"
}

// EffProbeBytes 返回生效的单次探测下载字节数。
func (c *Config) EffProbeBytes() int {
	if c.ProbeBytes > 0 {
		return c.ProbeBytes
	}
	return 2 << 20 // 2 MiB
}

// EffEgressCapacity 返回生效的出口并发流容量；0 表示由内存自动推算。
func (c *Config) EffEgressCapacity(memAvailKB int64) int {
	if c.EgressCapacity > 0 {
		return c.EgressCapacity
	}
	// 按可用内存的 1/4 估算：每条流约 4KB 缓冲。
	// 低内存设备自动缩容，避免 OOM（DESIGN.md §12.4）。
	if memAvailKB <= 0 {
		return 1024
	}
	n := int(memAvailKB * 1024 / 4 / 4096)
	if n < 64 {
		n = 64
	}
	if n > 65536 {
		n = 65536
	}
	return n
}

// DefaultInnerCIDR 是隧道内层默认网段。
//
// 取 /16 而不是 /24：每个节点按 node_id 哈希派生唯一内层地址（见 forward.NodeInnerIP），
// /24 只有 253 个可用地址，几个节点撞车概率就有十几个百分点；
// /16 有 6.5 万个，十来个节点几乎不可能碰撞。
// 选 10.254.x 是因为在私有地址里比较靠后，与常见的 192.168.x / 10.0.x / 172.16-31.x 撞车概率低。
const DefaultInnerCIDR = "10.254.0.0/16"

// DefaultTunName 是隧道网卡默认名。
const DefaultTunName = "slx0"

// EffInnerCIDR 返回生效的内层网段。
func (c *Config) EffInnerCIDR() string {
	if c.InnerCIDR != "" {
		return c.InnerCIDR
	}
	return DefaultInnerCIDR
}

// EffTunName 返回生效的隧道网卡名。
func (c *Config) EffTunName() string {
	if c.TunName != "" {
		return c.TunName
	}
	return DefaultTunName
}

// EffDispatch 返回是否启用分流。
func (c *Config) EffDispatch() bool {
	if c.Dispatch != nil {
		return *c.Dispatch
	}
	return true // 默认启用：这是本产品的核心功能
}

// 数据面模式取值。
const (
	// ModeProxy 反向连接 + 用户态协议栈：出口零权限、不需要公网 IP。
	// 这是 M7 起的默认模式（DESIGN.md §18）。
	ModeProxy = "proxy"
	// ModePacket 旧的裸包转发：出口需 root + TUN + NAT。
	// 需要任意 IP 协议或源 IP 透明时切回它。
	ModePacket = "packet"
)

// DefaultAgentListen 是 agent 反向连接的默认监听地址。
//
// 选 11901 而非 11900：11900 常被其它隧道工具占用，
// 固定用一个不冲突的高位端口，避免与现有服务抢。
const DefaultAgentListen = "0.0.0.0:11901"

// EffTransportMode 返回生效的数据面模式。
func (c *Config) EffTransportMode() string {
	m := strings.ToLower(strings.TrimSpace(c.TransportMode))
	switch m {
	case ModeProxy, ModePacket:
		return m
	case "":
		return ModeProxy // 默认走新架构
	default:
		return ModeProxy // 非法值按默认处理，日志里会提示
	}
}

// EffAgentListen 返回生效的 agent 监听地址。
func (c *Config) EffAgentListen() string {
	if strings.TrimSpace(c.AgentListen) != "" {
		return c.AgentListen
	}
	return DefaultAgentListen
}

// EffGatewayAddr 返回生效的网关地址（可能为空 = 不启动 agent）。
func (c *Config) EffGatewayAddr() string {
	return strings.TrimSpace(c.GatewayAddr)
}

// applyEnvOverrides 用 SLX_* 环境变量覆盖配置。
//
// 这样高级调参不必污染配置文件，符合「减少配置项」的要求。
func applyEnvOverrides(f *File) {
	if v := os.Getenv("SLX_LOG_LEVEL"); v != "" {
		f.LogLevel = v
	}
	if v := os.Getenv("SLX_LOG_FORMAT"); v != "" {
		f.LogFormat = v
	}
	if v := os.Getenv("SLX_LISTEN_ADDR"); v != "" {
		f.ListenAddr = v
	}
	if v := os.Getenv("SLX_LISTEN_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.ListenPort = n
		}
	}
	if v := os.Getenv("SLX_CTRL_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.CtrlPort = n
		}
	}
	if v := os.Getenv("SLX_CN_LIST_URL"); v != "" {
		f.CNListURL = v
	}
	if v := os.Getenv("SLX_PROBE_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.ProbeBytes = n
		}
	}
	if v := os.Getenv("SLX_NETWORK_KEY"); v != "" {
		f.NetworkKey = v
	}
	if v := os.Getenv("SLX_TRANSPORT_MODE"); v != "" {
		f.TransportMode = v
	}
	if v := os.Getenv("SLX_AGENT_LISTEN"); v != "" {
		f.AgentListen = v
	}
	if v := os.Getenv("SLX_GATEWAY_ADDR"); v != "" {
		f.GatewayAddr = v
	}
}

// Validate 校验配置有效性。
func (c *Config) Validate() error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.NetworkKey))
	if err != nil {
		return fmt.Errorf("network_key 不是合法 base64: %w\n提示: 它由 slx 首次启动时自动生成，形如 32 字节 base64 串", err)
	}
	if len(raw) != KeyLen {
		return fmt.Errorf("network_key 长度错误: 得到 %d 字节，需要 %d 字节", len(raw), KeyLen)
	}
	if c.ListenPort < 0 || c.ListenPort > 65535 {
		return fmt.Errorf("listen_port 超出范围: %d", c.ListenPort)
	}
	return nil
}

// KeyBytes 返回解码后的密钥字节。
func (c *Config) KeyBytes() []byte {
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(c.NetworkKey))
	return raw
}

// Save 原子地写回配置文件（先写临时文件再 rename，避免写一半掉电损坏）。
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(&c.File)
	if err != nil {
		return err
	}
	header := []byte("# SLX 配置 —— 全部必需配置项只有 2 个\n" +
		"# 其它能力（网卡、端口、ZT 网段、iptables 变体、DNS 上游）均由程序自动推断\n\n")
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, append(header, out...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

// GenerateKey 生成一个随机的 32 字节密钥并 base64 编码。
func GenerateKey() (string, error) {
	b := make([]byte, KeyLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成密钥失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// migrate 把旧格式配置升级到当前格式。
//
// 返回值 migratedFrom 非零表示发生了迁移。
// 迁移器应当只增字段、不丢字段，并且可重复执行（幂等）。
func migrate(f *File) (int, error) {
	from := f.SchemaVersion
	switch {
	case from == 0:
		// 早期文件没有 schema_version，视为 1。
		f.SchemaVersion = 1
		return 0, nil
	case from == CurrentSchema:
		return 0, nil
	case from > CurrentSchema:
		return 0, fmt.Errorf(
			"配置格式版本 %d 高于本程序支持的 %d\n提示: 该配置由更新版本的 slx 写入，请升级程序而不是降级配置",
			from, CurrentSchema)
	default:
		// 未来在此处加入逐版本迁移：
		//   if f.SchemaVersion < 2 { ...; f.SchemaVersion = 2 }
		f.SchemaVersion = CurrentSchema
		return from, nil
	}
}

// —— relay 双通道配置的生效值 ——

// 通道模式取值。
const (
	// RelayTransportAuto 按通道评分自动选（带 8 分滞回）。
	RelayTransportAuto = "auto"
	// RelayTransportTLS 只走公网直连通道。
	RelayTransportTLS = "tls"
	// RelayTransportZT 只走 ZeroTier 反向通道（= 改造前的行为）。
	RelayTransportZT = "zt"
)

// 环境变量（临时覆盖，优先级最高）。
const (
	EnvRelayTransport        = "SLX_RELAY_TRANSPORT"
	EnvRelayTLSPort          = "SLX_RELAY_TLS_PORT"
	EnvRelayTLSListen        = "SLX_RELAY_TLS_LISTEN"
	EnvRelayTLSConns         = "SLX_RELAY_TLS_CONNS"
	EnvRelayTLSConnectTO     = "SLX_RELAY_TLS_CONNECT_TIMEOUT"
	EnvRelayTLSProbeInterval = "SLX_RELAY_TLS_PROBE_INTERVAL"
	EnvRelayTLSPin           = "SLX_RELAY_TLS_PIN"
	EnvRelayAdvertise        = "SLX_RELAY_ADVERTISE"
	EnvRelayClockSkew        = "SLX_RELAY_CLOCK_SKEW"
)

// EffRelayTransport 返回生效的通道模式。
//
// 优先级：环境变量 > YAML > 默认 zt。
//
// 默认 zt：P0 阶段行为必须与改造前完全一致，任何时候能一键回退。
// 非法取值一律回落到 zt（宁可保守，也别因为一个错字把网络搞挂）。
func (c *Config) EffRelayTransport() string {
	v := strings.TrimSpace(os.Getenv(EnvRelayTransport))
	if v == "" {
		v = strings.TrimSpace(c.RelayTransport)
	}
	switch strings.ToLower(v) {
	case RelayTransportAuto:
		return RelayTransportAuto
	case RelayTransportTLS:
		return RelayTransportTLS
	case RelayTransportZT:
		return RelayTransportZT
	default:
		return RelayTransportZT
	}
}

// EffRelayTLSPort 返回生效的 agent 侧 TLS 监听端口（默认 8443）。
func (c *Config) EffRelayTLSPort() int {
	if v := strings.TrimSpace(os.Getenv(EnvRelayTLSPort)); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	if c.RelayTLSPort > 0 && c.RelayTLSPort < 65536 {
		return c.RelayTLSPort
	}
	return 8443
}

// EffRelayTLSListen 返回生效的 agent 侧 TLS 监听地址（空 = 不监听）。
func (c *Config) EffRelayTLSListen() string {
	if v := strings.TrimSpace(os.Getenv(EnvRelayTLSListen)); v != "" {
		return v
	}
	return strings.TrimSpace(c.RelayTLSListen)
}

// EffRelayAdvertise 返回生效的 agent 上报公网地址（空 = 自动探测）。
func (c *Config) EffRelayAdvertise() string {
	if v := strings.TrimSpace(os.Getenv(EnvRelayAdvertise)); v != "" {
		return v
	}
	return strings.TrimSpace(c.RelayAdvertise)
}

// EffRelayTLSConns 返回生效的每 agent 直连通道连接数（默认 4）。
func (c *Config) EffRelayTLSConns() int {
	if v := strings.TrimSpace(os.Getenv(EnvRelayTLSConns)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 16 {
			return n
		}
	}
	if c.RelayTLSConns > 0 && c.RelayTLSConns <= 16 {
		return c.RelayTLSConns
	}
	return 4
}

// EffRelayTLSPin 返回生效的证书指纹（可空 = 走 TOFU）。
func (c *Config) EffRelayTLSPin() string {
	if v := strings.TrimSpace(os.Getenv(EnvRelayTLSPin)); v != "" {
		return v
	}
	return strings.TrimSpace(c.RelayTLSPin)
}

// EffRelayCertDays 返回生效的 agent 侧证书有效期（天，默认 3650）。
func (c *Config) EffRelayCertDays() int {
	if c.RelayCertDays > 0 {
		return c.RelayCertDays
	}
	return 3650
}

// EffRelayTLSConnectTimeout 返回生效的直连建连超时（默认 8 秒）。
func (c *Config) EffRelayTLSConnectTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv(EnvRelayTLSConnectTO)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if v := strings.TrimSpace(c.RelayTLSConnectTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 8 * time.Second
}

// EffRelayTLSProbeInterval 返回生效的通道探测周期（默认 15 秒）。
//
// 注意：决策定的阈值是「连续 3 次失败、探测周期 15 秒」⇒ 45 秒内回退兜底通道，
// 所以这里的默认值是 15s 而不是 30s。
func (c *Config) EffRelayTLSProbeInterval() time.Duration {
	if v := strings.TrimSpace(os.Getenv(EnvRelayTLSProbeInterval)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if v := strings.TrimSpace(c.RelayTLSProbeInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Second
}

// EffPprof 返回是否开启本地性能剖析端点。
//
// 环境变量 SLX_PPROF 非空即开（保持老习惯），
// 配置文件 slx_pprof: true 也开（不受 procd env 覆盖影响）。
func (c *Config) EffPprof() bool {
	if v := strings.TrimSpace(os.Getenv("SLX_PPROF")); v != "" && v != "0" {
		return true
	}
	return c.Pprof
}

// EffRelayProbeTimeout 返回生效的单次通道探测超时（默认 10 秒）。
func (c *Config) EffRelayProbeTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("SLX_RELAY_PROBE_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if v := strings.TrimSpace(c.RelayProbeTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Second
}

// EffRelayChannelFailures 返回生效的通道级熔断阈值（默认 5 次）。
func (c *Config) EffRelayChannelFailures() int {
	if v := strings.TrimSpace(os.Getenv("SLX_RELAY_CHANNEL_FAILURES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			return n
		}
	}
	if c.RelayChannelFailures > 0 && c.RelayChannelFailures <= 20 {
		return c.RelayChannelFailures
	}
	return 5
}

// EffRelayClockSkew 返回生效的握手时钟偏差上限（默认 60 秒）。
//
// 实测各节点时钟差 ≤ 8 秒，且与传输路径无关 ⇒ 默认值不必放宽，
// 但做成可配置，万一将来有机器没开 NTP 可以临时调。
func (c *Config) EffRelayClockSkew() time.Duration {
	if v := strings.TrimSpace(os.Getenv(EnvRelayClockSkew)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if v := strings.TrimSpace(c.RelayClockSkew); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// EffRelayTLSCert 返回生效的证书路径（空 = 默认路径）。
func (c *Config) EffRelayTLSCert() string {
	if v := strings.TrimSpace(c.RelayTLSCert); v != "" {
		return v
	}
	return "/etc/slx/relay-cert.pem"
}
