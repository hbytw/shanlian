package proto

// 黄金向量测试：把「线上字节格式」冻结下来。
//
// # 为什么需要它
//
// 协议兼容性最容易被无意破坏：改一个 JSON 字段名、调整一个结构体字段顺序、
// 换一种长度编码，本地测试可能全绿，但新旧版本节点之间就悄悄不通了。
// 这类问题在实网里极难定位 —— 现象是「能发现对方、就是传不了数据」。
//
// 做法：把一组代表性消息编码成固定字节序列，作为「黄金向量」写死在测试里。
// 任何人改动编码逻辑导致字节变化，测试立刻失败，必须显式更新向量并说明原因。
//
// 更新向量时请务必自问：**旧版本节点还读得懂吗？**
// 如果答案是"读不懂"，那就是破坏性变更，需要提协议版本。

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// goldenFrame 是一条黄金帧向量。
type goldenFrame struct {
	name string
	typ  Type
	_    uint8
	data []byte
	hex  string // 完整帧的十六进制
}

func TestGoldenFrameVectors(t *testing.T) {
	vectors := []goldenFrame{
		{name: "空数据帧", typ: TypeData, data: []byte{}, hex: "11000000"},
		{name: "单字节数据帧", typ: TypeData, data: []byte{0xAB}, hex: "11000001ab"},
		{name: "典型IP包数据帧", typ: TypeData, data: bytes.Repeat([]byte{0x45}, 40), hex: ""},
		{name: "保活帧", typ: TypeKeepalive, data: nil, hex: "13000000"},
		{name: "控制帧-短", typ: TypeCtrl, data: []byte{0x7B, 0x7D}, hex: "120000027b7d"},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			f := Frame{Type: v.typ, Payload: v.data}
			buf := make([]byte, f.EncodedLen())
			n, err := f.Encode(buf)
			if err != nil {
				t.Fatalf("编码失败: %v", err)
			}

			if v.hex == "" {
				// 没有固定向量的用例只校验头部结构
				if n < HeaderLen {
					t.Fatalf("编码长度异常: %d", n)
				}
				checkHeader(t, buf[:HeaderLen], v.typ, 0, len(v.data))
				return
			}

			want, _ := hex.DecodeString(v.hex)
			if !bytes.Equal(buf[:n], want) {
				t.Fatalf("编码字节与黄金向量不符\n  期望 %s\n  实际 %s",
					hex.EncodeToString(want), hex.EncodeToString(buf[:n]))
			}

			// 反向解码必须还原
			got, err := Decode(want)
			if err != nil {
				t.Fatalf("解码黄金向量失败: %v", err)
			}
			if got.Type != v.typ {
				t.Fatalf("类型不符: 期望 %v 实际 %v", v.typ, got.Type)
			}
			if !bytes.Equal(got.Payload, v.data) {
				t.Fatalf("载荷不符: 期望 %x 实际 %x", v.data, got.Payload)
			}
		})
	}
}

// checkHeader 校验 4 字节头部的字段布局：
// 第 1 字节 = 版本(高4位) | 类型(低4位)，第 2 字节 = 标志，第 3-4 字节 = 大端长度。
func checkHeader(t *testing.T, h []byte, typ Type, flags uint8, payloadLen int) {
	t.Helper()
	if got := h[0] >> 4; got != WireVersion {
		t.Fatalf("头部版本位不符: 期望 %d 实际 %d", WireVersion, got)
	}
	if got := Type(h[0] & 0x0F); got != typ {
		t.Fatalf("头部类型位不符: 期望 %v 实际 %v", typ, got)
	}
	if h[1] != flags {
		t.Fatalf("头部标志不符: 期望 %#x 实际 %#x", flags, h[1])
	}
	if got := int(h[2])<<8 | int(h[3]); got != payloadLen {
		t.Fatalf("头部长度不符: 期望 %d 实际 %d", payloadLen, got)
	}
}

// —— 控制消息黄金向量 ——

func TestGoldenControlVectors(t *testing.T) {
	// HELLO 是最关键的一条：它决定了节点之间能否互相认得。
	// 字段改名/删除都会破坏兼容。
	hello := Hello{
		NodeID:       "0123456789abcdef",
		SWVersion:    "1.2.3",
		ProtoMin:     1,
		ProtoMax:     1,
		Capabilities: []string{"egress", "gateway"},
		Transports:   []string{"zt"},
	}

	b, err := MarshalEnvelope(MsgHello, hello)
	if err != nil {
		t.Fatalf("编码 HELLO 失败: %v", err)
	}

	// 解出信封，逐字段核对 JSON 键名 —— 键名就是线上契约
	env, err := UnmarshalEnvelope(b)
	if err != nil {
		t.Fatalf("解码信封失败: %v", err)
	}
	if env.V != ControlVersion {
		t.Fatalf("控制版本不符: 期望 %d 实际 %d", ControlVersion, env.V)
	}
	if env.Type != MsgHello {
		t.Fatalf("消息类型不符: 期望 %v 实际 %v", MsgHello, env.Type)
	}

	var m map[string]any
	if err := json.Unmarshal(env.Body, &m); err != nil {
		t.Fatalf("body 不是合法 JSON: %v", err)
	}

	// 这些键名一旦改动就是破坏性变更
	required := []string{
		"node_id", "sw_version", "proto_min", "proto_max",
		"capabilities", "transports",
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Fatalf("HELLO 缺少必需字段 %q —— 这是破坏性变更，需要提协议版本。当前字段: %v",
				k, keysOf(m))
		}
	}

	// 解码回来必须一致
	var back Hello
	if err := json.Unmarshal(env.Body, &back); err != nil {
		t.Fatalf("回解 HELLO 失败: %v", err)
	}
	if back.NodeID != hello.NodeID {
		t.Fatalf("NodeID 往返不一致: %q vs %q", back.NodeID, hello.NodeID)
	}
	if back.ProtoMin != hello.ProtoMin || back.ProtoMax != hello.ProtoMax {
		t.Fatal("协议版本范围往返不一致")
	}
}

func TestGoldenControlFieldNames(t *testing.T) {
	// 每个消息类型的线上字段名都要冻结。
	cases := []struct {
		typ  MsgType
		body any
		want []string
	}{
		{MsgHelloAck, HelloAck{
			NodeID: "abc", SWVersion: "1", ProtoMin: 1, ProtoMax: 1, AgreedProto: 1,
		}, []string{"node_id", "sw_version", "proto_min", "proto_max", "agreed_proto", "accepted"}},
		{MsgHeartbeat, Heartbeat{NodeID: "abc"}, []string{"node_id"}},
		{MsgProbeReq, ProbeReq{Target: "example.com"}, []string{"target"}},
		{MsgProbeResp, ProbeResp{Target: "x", RTTMS: 1.5}, []string{"target", "rtt_ms"}},
		{MsgResolveReq, ResolveReq{Domain: "x"}, []string{"domain"}},
		{MsgResolveResp, ResolveResp{Domain: "x", IPs: []string{"1.2.3.4"}}, []string{"domain", "ips"}},
		{MsgNack, Nack{AboutType: MsgHello, Code: 1}, []string{"about_type", "code"}},
	}

	for _, c := range cases {
		t.Run(string(c.typ), func(t *testing.T) {
			b, err := MarshalEnvelope(c.typ, c.body)
			if err != nil {
				t.Fatalf("编码失败: %v", err)
			}
			env, err := UnmarshalEnvelope(b)
			if err != nil {
				t.Fatalf("解码失败: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(env.Body, &m); err != nil {
				t.Fatalf("body 不是合法 JSON: %v", err)
			}
			for _, k := range c.want {
				if _, ok := m[k]; !ok {
					t.Fatalf("%s 缺少字段 %q —— 破坏性变更。实际字段: %v",
						c.typ, k, keysOf(m))
				}
			}
		})
	}
}

// TestForwardCompatibility 校验「未知字段被忽略、缺失字段用零值」。
//
// 这是协议向前兼容的基础：新版本给旧版本发消息时多加字段，
// 旧版本必须能正常解析而不是报错。
func TestForwardCompatibility(t *testing.T) {
	// 模拟一个「未来版本」的 HELLO：多了几个当前不认识的字段
	future := `{"v":1,"type":"hello","body":{` +
		`"node_id":"ffff","sw_version":"9.9.9","proto_min":1,"proto_max":5,` +
		`"capabilities":["egress"],"transports":["zt"],` +
		`"future_field":123,"another_future":{"nested":true}}}`

	env, err := UnmarshalEnvelope([]byte(future))
	if err != nil {
		t.Fatalf("含未知字段的消息应能解析: %v", err)
	}
	if env.Type != MsgHello {
		t.Fatalf("类型解析错误: %v", env.Type)
	}
	var h Hello
	if err := json.Unmarshal(env.Body, &h); err != nil {
		t.Fatalf("未知字段应被忽略: %v", err)
	}
	if h.NodeID != "ffff" {
		t.Fatalf("已知字段应正常解析，实际 %q", h.NodeID)
	}

	// 反过来：旧版本消息缺少新字段，也要能解析（零值）
	old := `{"v":1,"type":"hello","body":{"node_id":"eeee","sw_version":"1.0.0"}}`
	env2, err := UnmarshalEnvelope([]byte(old))
	if err != nil {
		t.Fatalf("缺字段的消息应能解析: %v", err)
	}
	var h2 Hello
	if err := json.Unmarshal(env2.Body, &h2); err != nil {
		t.Fatalf("缺字段应回落到零值: %v", err)
	}
	if h2.NodeID != "eeee" || h2.ProtoMin != 0 {
		t.Fatalf("零值回落不正确: %+v", h2)
	}
}

// TestWireConstantsFrozen 冻结线上的魔数。
//
// 这些数字一旦改动，新旧节点直接无法互通。
func TestWireConstantsFrozen(t *testing.T) {
	if WireVersion != 1 {
		t.Fatalf("帧头版本号被改成了 %d —— 破坏性变更，需要同步升级所有节点", WireVersion)
	}
	if ControlVersion != 1 {
		t.Fatalf("控制协议版本被改成了 %d —— 破坏性变更", ControlVersion)
	}
	if HeaderLen != 4 {
		t.Fatalf("帧头长度被改成 %d —— 破坏性变更", HeaderLen)
	}
	if MaxPayload != 65535 {
		t.Fatalf("最大载荷被改成 %d", MaxPayload)
	}
	// 类型编码
	if TypeData != 1 || TypeCtrl != 2 || TypeKeepalive != 3 || TypeProbe != 4 {
		t.Fatalf("帧类型编码被改动: data=%d ctrl=%d keepalive=%d probe=%d",
			TypeData, TypeCtrl, TypeKeepalive, TypeProbe)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
