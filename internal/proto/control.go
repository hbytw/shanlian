package proto

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ControlVersion 是控制面 JSON 消息的版本。
//
// 兼容规则（DESIGN.md §9.1）：
//   - 字段只增不删；废弃字段保留一个版本周期；
//   - 未知 type → 回 NACK，不断连；
//   - 未知字段 → 忽略；
//   - 错误码只增不重编号。
const ControlVersion uint8 = 1

// MsgType 是控制消息的类型标识。
type MsgType string

const (
	// MsgHello 主动通告自身身份与能力。发现阶段的第一条消息。
	MsgHello MsgType = "hello"
	// MsgHelloAck 对 hello 的回应，携带协商结果。
	MsgHelloAck MsgType = "hello_ack"
	// MsgHeartbeat 周期保活，携带负载信息。
	MsgHeartbeat MsgType = "heartbeat"
	// MsgProbeReq 请求某出口探测一个目标（域名或 IP）。
	MsgProbeReq MsgType = "probe_req"
	// MsgProbeResp 探测结果。
	MsgProbeResp MsgType = "probe_resp"
	// MsgResolveReq 请求经某出口解析域名（用于 CDN 就近）。
	MsgResolveReq MsgType = "resolve_req"
	// MsgResolveResp 解析结果。
	MsgResolveResp MsgType = "resolve_resp"
	// MsgConnectReq 网关请求 agent 替它在目标上建立一条连接（proxy 模式）。
	MsgConnectReq MsgType = "connect_req"
	// MsgConnectResp agent 回报建连结果。
	MsgConnectResp MsgType = "connect_resp"
	// MsgNack 表示收到无法处理的消息。
	MsgNack MsgType = "nack"
)

// 能力标识。用字符串而非位图：新增能力时老节点天然忽略，无需版本升级。
const (
	CapEgress   = "egress"   // 能当出口（收隧道包 → SNAT → 上网）
	CapGateway  = "gateway"  // 能当网关（建 TUN、接管流量）
	CapProbe    = "probe"    // 能执行 L2 主动探测
	CapAffinity = "affinity" // 支持域名↔出口亲和
	// CapAgent 能以「agent」身份工作：反向拨出、按需 connect、字节中继。
	// 这是 proxy 模式的核心能力，与 CapEgress（转发裸包）互斥但可共存。
	CapAgent = "agent"
)

// 传输方式标识。
const (
	TransportZT     = "zt"     // 经 ZeroTier 虚拟网卡
	TransportDirect = "direct" // 直连 VPS 公网地址（SLX 自有 PSK 加密）
	// TransportRelay proxy 模式：agent 反向连接网关，流内按需 connect。
	TransportRelay = "relay"
)

// 连接请求里的传输层编号（与 IP 协议号无关，仅线格式约定）。
const (
	ConnectTCP uint8 = 0
	ConnectUDP uint8 = 1
)

// 错误码。只增不重编号。
const (
	CodeOK                 = 0
	CodeUnknownMessageType = 1001 // 不认识的 msg type
	CodeUnsupportedVersion = 1002 // 协议版本无交集
	CodeBadPayload         = 1003 // JSON 解析失败或必填字段缺失
	CodeCapabilityMissing  = 1004 // 对端不具备该能力
	CodeBusy               = 1005 // 对端过载，稍后重试
	CodeTargetInvalid      = 1006
)

// Envelope 是所有控制消息的统一外壳。
//
// 用 Body 承载具体内容，使「未知类型」可以只解析外壳、回 NACK 而无需理解内容。
type Envelope struct {
	V    uint8           `json:"v"`
	Type MsgType         `json:"type"`
	Body json.RawMessage `json:"body,omitempty"`
}

// MarshalEnvelope 把任意 body 打包成信封。
func MarshalEnvelope(t MsgType, body any) ([]byte, error) {
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Envelope{V: ControlVersion, Type: t, Body: raw})
}

// UnmarshalEnvelope 解析信封并检查版本。
//
// 返回的 Envelope 即使 Type 未知也应被视为「已成功解析」，
// 由调用方决定回 NACK 还是忽略。
func UnmarshalEnvelope(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("proto: 控制消息解析失败: %w", err)
	}
	if e.V == 0 {
		e.V = ControlVersion
	}
	return e, nil
}

// Hello 是发现阶段的能力通告。
type Hello struct {
	NodeID       string   `json:"node_id"`
	SWVersion    string   `json:"sw_version"`
	ProtoMin     int      `json:"proto_min"`
	ProtoMax     int      `json:"proto_max"`
	Capabilities []string `json:"capabilities,omitempty"`
	Transports   []string `json:"transports,omitempty"`
	Region       string   `json:"region,omitempty"`
	// AdvertiseAddr 是对端可用于直连的地址（如 VPS 公网 IP:端口）。
	// 为空表示只能经 ZeroTier 到达。这是 Oracle 1:1 NAT 场景的关键字段。
	AdvertiseAddr string `json:"advertise_addr,omitempty"`
}

// HelloAck 是 hello 的回应，携带协商后的协议版本与双方能力交集。
type HelloAck struct {
	NodeID          string   `json:"node_id"`
	SWVersion       string   `json:"sw_version"`
	ProtoMin        int      `json:"proto_min"`
	ProtoMax        int      `json:"proto_max"`
	AgreedProto     int      `json:"agreed_proto"`
	Capabilities    []string `json:"capabilities,omitempty"`
	CapabilityInter []string `json:"capability_inter,omitempty"`
	Transports      []string `json:"transports,omitempty"`
	Region          string   `json:"region,omitempty"`
	AdvertiseAddr   string   `json:"advertise_addr,omitempty"`
	Accepted        bool     `json:"accepted"`
	RejectReason    string   `json:"reject_reason,omitempty"`
	RejectCode      int      `json:"reject_code,omitempty"`
}

// Heartbeat 是周期保活，同时广播负载以支持选路时的容量惩罚。
type Heartbeat struct {
	NodeID      string  `json:"node_id"`
	Load        float64 `json:"load"`         // 0.0 ~ 1.0
	ActiveFlows int     `json:"active_flows"` // 当前活跃流数
	UpBps       int64   `json:"up_bps"`       // 上行速率估计
	DownBps     int64   `json:"down_bps"`
	UptimeSec   int64   `json:"uptime_sec"`
}

// ProbeReq 请求对端替我们探测一个目标。
type ProbeReq struct {
	Target    string `json:"target"`
	Bytes     int    `json:"bytes,omitempty"`      // 下载字节数，0 = 用默认
	TimeoutMS int    `json:"timeout_ms,omitempty"` // 超时，0 = 用默认
	Reason    string `json:"reason,omitempty"`     // 触发原因（日志用）
}

// ProbeResp 是探测结果。
type ProbeResp struct {
	Target       string   `json:"target"`
	ResolvedIPs  []string `json:"resolved_ips,omitempty"`
	RTTMS        float64  `json:"rtt_ms"`
	BytesPerSec  float64  `json:"bytes_per_sec"`
	LossRate     float64  `json:"loss_rate"`
	RTTConnectMS float64  `json:"rtt_connect_ms,omitempty"` // TCP 建连耗时
	Err          string   `json:"err,omitempty"`
}

// ResolveReq 请求经某出口解析域名，以拿到贴合该出口位置的 CDN 地址。
type ResolveReq struct {
	Domain string `json:"domain"`
}

// ResolveResp 是解析结果。
type ResolveResp struct {
	Domain      string   `json:"domain"`
	IPs         []string `json:"ips,omitempty"`
	TTLSec      int      `json:"ttl_sec,omitempty"`
	ResolvedVia string   `json:"resolved_via,omitempty"` // 实际使用的出口 node_id
	Resolver    string   `json:"resolver,omitempty"`     // 实际使用的上游
	Err         string   `json:"err,omitempty"`
}

// Nack 表示无法处理收到的消息。
type Nack struct {
	AboutType MsgType `json:"about_type"`
	Code      int     `json:"code"`
	Reason    string  `json:"reason,omitempty"`
	// SupportedTypes 让对端知道我们认识哪些类型，便于诊断。
	SupportedTypes []MsgType `json:"supported_types,omitempty"`
}

// ConnectReq 是网关发给 agent 的建连请求（proxy 模式）。
//
// 语义：请在目标 Addr:Port 上建立一条 Transport 类型的连接，
// 然后把这条 yamux 流当作字节管道（TCP）或长度帧管道（UDP）使用。
type ConnectReq struct {
	// StreamID 仅用于日志关联（yamux 自己有流号，这里冗余一份便于排查）。
	StreamID uint32 `json:"stream_id,omitempty"`
	// Net 0=IPv4 1=IPv6。
	Net uint8 `json:"net"`
	// Transport 见 ConnectTCP / ConnectUDP。
	Transport uint8  `json:"transport"`
	Addr      string `json:"addr"`
	Port      uint16 `json:"port"`
	// Framed 表示 UDP 是否带长度帧（流式传输必须带，否则丢包边界）。
	Framed bool `json:"framed,omitempty"`
}

// ConnectResp 是 agent 对建连请求的回应。
//
// 三种结果对应网关侧不同的 TCP 语义（这决定了客户端看到的行为）：
//
//	Established=true            → 回 SYN-ACK，连接建立
//	Reset=true                  → 回 RST，客户端立刻收到连接被拒（快速失败）
//	Established=false, Reset=false → 不回包，让客户端自己超时重试
type ConnectResp struct {
	StreamID    uint32 `json:"stream_id,omitempty"`
	Established bool   `json:"established"`
	Reset       bool   `json:"reset,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// KnownMsgTypes 返回本实现认识的类型，用于 NACK 与诊断。
func KnownMsgTypes() []MsgType {
	return []MsgType{
		MsgHello, MsgHelloAck, MsgHeartbeat,
		MsgProbeReq, MsgProbeResp,
		MsgResolveReq, MsgResolveResp,
		MsgConnectReq, MsgConnectResp,
		MsgNack,
	}
}

// IsKnownMsgType 判断类型是否被本实现认识。
func IsKnownMsgType(t MsgType) bool {
	for _, k := range KnownMsgTypes() {
		if k == t {
			return true
		}
	}
	return false
}

// Negotiate 计算双方协议版本交集中的最大可用版本。
//
// 规则（DESIGN.md §9.2）：
//
//	lo = max(aMin, bMin); hi = min(aMax, bMax)
//	若 lo > hi → 无交集，返回 error（调用方应记录清晰日志并拒绝连接）
//	否则取 hi
func Negotiate(aMin, aMax, bMin, bMax int) (int, error) {
	lo := aMin
	if bMin > lo {
		lo = bMin
	}
	hi := aMax
	if bMax < hi {
		hi = bMax
	}
	if lo > hi {
		return 0, fmt.Errorf("proto: 协议版本无交集 我方[%d,%d] 对端[%d,%d]", aMin, aMax, bMin, bMax)
	}
	return hi, nil
}

// IntersectCaps 求两个能力列表的交集（结果有序，便于比较与日志）。
//
// 交集驱动特性开关：新节点连老节点时，只启用双方都有的能力，而不是断连。
func IntersectCaps(a, b []string) []string {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	var out []string
	for _, x := range b {
		if _, ok := set[x]; ok {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// IntersectStrings 是 IntersectCaps 的通用版本，用于传输方式列表。
func IntersectStrings(a, b []string) []string { return IntersectCaps(a, b) }

// HasCap 判断能力列表中是否包含某能力。
func HasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// HostPingReq 是网关发给 agent 的「帮我 ping 一下这个地址」请求（proxy 模式的 ICMP）。
//
// 为什么不在网关上自己 ping：网关的 ICMP 出发地和出口 agent 不同，
// 测到的可达性对用户没有意义 —— 用户要的是「从出口那边能不能到」。
type HostPingReq struct {
	Addr string `json:"addr"`
}

// HostPingResp 是探测结果。
//
// 只有 Alive=true 时网关才回 ICMP echo reply；false 时静默（让客户端超时），
// 不能伪造回复 —— 那会让用户以为主机活着，而实际是网络不通。
type HostPingResp struct {
	Alive bool `json:"alive"`
}

// AddrReport 是 agent 上报给网关的「我可以直接连我」的地址信息。
//
// # 为什么是这个形状
//
//   - Host/Port：agent 的公网监听地址（默认端口 8443）。
//     网关据此主动拨过来 —— 网关在 CGNAT 后没有入站，只能出站。
//   - CertSHA256：agent 的 TLS 服务端证书指纹（SHA-256，hex，无冒号）。
//     网关拨过来时做**指纹钉扎**校验 —— 公网上的自签证书没有 CA 可信，
//     指纹就是唯一的身份凭据。
//   - AgentVer：agent 的版本串，只用于日志与排障，不参与任何判定。
//
// # 兼容性
//
// 老 agent 不发这个帧 → 网关收不到 → 该 agent 只能走反向通道（ZeroTier）。
// 这是**天然兼容**的：不需要版本协商，不需要开关；
// 老实现不认识这个 kind，会按「不认识的消息要容忍」忽略它。
type AddrReport struct {
	// Proto 是传输协议，目前只有 "tcp"。
	//
	// 留着它是为了将来加 QUIC/UDP 通道时**不用再改帧格式** ——
	// 这正是「不改握手二进制格式」的回报：可扩展的字段放 JSON 帧里。
	Proto string `json:"proto,omitempty"`

	Host       string `json:"host"`
	Port       uint16 `json:"port"`
	CertSHA256 string `json:"cert_sha256,omitempty"`
	AgentVer   string `json:"agent_ver,omitempty"`
}
