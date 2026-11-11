package proto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNegotiate(t *testing.T) {
	cases := []struct {
		name                   string
		aMin, aMax, bMin, bMax int
		want                   int
		wantErr                bool
	}{
		{"完全相同", 1, 1, 1, 1, 1, false},
		{"我方范围更宽", 1, 3, 1, 1, 1, false},
		{"对方范围更宽", 1, 1, 1, 3, 1, false},
		{"交集取上界", 1, 5, 3, 9, 5, false},
		{"无交集", 1, 2, 3, 4, 0, true},
		{"老节点连新节点", 1, 1, 1, 5, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Negotiate(c.aMin, c.aMax, c.bMin, c.bMax)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到版本 %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if got != c.want {
				t.Errorf("协商版本 = %d，期望 %d", got, c.want)
			}
		})
	}
}

func TestIntersectCapsOrdered(t *testing.T) {
	a := []string{CapGateway, CapEgress, CapProbe}
	b := []string{CapProbe, CapEgress, CapAffinity}
	got := IntersectCaps(a, b)
	want := []string{CapEgress, CapProbe} // 有序输出
	if !reflect.DeepEqual(got, want) {
		t.Errorf("交集 = %v，期望 %v", got, want)
	}
}

func TestIntersectCapsEmpty(t *testing.T) {
	if got := IntersectCaps(nil, []string{CapEgress}); len(got) != 0 {
		t.Errorf("空集交集应为空，得到 %v", got)
	}
}

func TestHasCap(t *testing.T) {
	caps := []string{CapEgress, CapProbe}
	if !HasCap(caps, CapEgress) {
		t.Error("应包含 egress")
	}
	if HasCap(caps, CapGateway) {
		t.Error("不应包含 gateway")
	}
}

func TestEnvelopeRoundtrip(t *testing.T) {
	h := Hello{
		NodeID:       "a788ee05c0d156e7",
		SWVersion:    "1.0.0",
		ProtoMin:     1,
		ProtoMax:     1,
		Capabilities: []string{CapEgress},
		Transports:   []string{TransportZT},
		// Oracle 1:1 NAT 场景下，这里是对端唯一可达的直连地址
		AdvertiseAddr: "129.225.146.84:51820",
	}
	b, err := MarshalEnvelope(MsgHello, h)
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}

	env, err := UnmarshalEnvelope(b)
	if err != nil {
		t.Fatalf("解包失败: %v", err)
	}
	if env.Type != MsgHello {
		t.Errorf("Type = %q", env.Type)
	}
	if env.V != ControlVersion {
		t.Errorf("Version = %d", env.V)
	}

	var got Hello
	if err := json.Unmarshal(env.Body, &got); err != nil {
		t.Fatalf("body 解析失败: %v", err)
	}
	if got.NodeID != h.NodeID || got.AdvertiseAddr != h.AdvertiseAddr {
		t.Errorf("往返不一致: %+v != %+v", got, h)
	}
}

func TestEnvelopeUnknownTypeIsParseable(t *testing.T) {
	// 未来版本可能新增消息类型。老节点必须能解析外壳并回 NACK，
	// 而不是因为不认识就直接断连。
	raw := []byte(`{"v":1,"type":"future_thing","body":{"x":1}}`)
	env, err := UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatalf("未知类型不应解析失败: %v", err)
	}
	if IsKnownMsgType(env.Type) {
		t.Error("future_thing 不应被识别为已知类型")
	}
}

func TestEnvelopeUnknownFieldsIgnored(t *testing.T) {
	// 新节点发来的消息带有老节点不认识的字段，必须被忽略而不是报错。
	raw := []byte(`{"v":1,"type":"hello","body":{"node_id":"aa","brand_new_field":42}}`)
	env, err := UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatalf("多余字段不应导致失败: %v", err)
	}
	var h Hello
	if err := json.Unmarshal(env.Body, &h); err != nil {
		t.Fatalf("body 解析失败: %v", err)
	}
	if h.NodeID != "aa" {
		t.Errorf("NodeID = %q", h.NodeID)
	}
}

func TestEnvelopeMissingVersionDefaults(t *testing.T) {
	raw := []byte(`{"type":"hello","body":{}}`)
	env, err := UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatalf("缺少 v 字段不应失败: %v", err)
	}
	if env.V != ControlVersion {
		t.Errorf("版本应回落到 %d，得到 %d", ControlVersion, env.V)
	}
}

func TestKnownMsgTypesComplete(t *testing.T) {
	for _, mt := range KnownMsgTypes() {
		if !IsKnownMsgType(mt) {
			t.Errorf("%q 在 KnownMsgTypes 里但 IsKnownMsgType 不认", mt)
		}
	}
}
