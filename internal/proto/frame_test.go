package proto

import (
	"bytes"
	"testing"
)

func TestFrameRoundtrip(t *testing.T) {
	payload := []byte{0x45, 0x00, 0x00, 0x3c, 0xde, 0xad, 0xbe, 0xef}
	f := Frame{Type: TypeData, Flags: FlagFragment, Payload: payload}

	buf := make([]byte, f.EncodedLen())
	n, err := f.Encode(buf)
	if err != nil {
		t.Fatalf("Encode 失败: %v", err)
	}
	if n != HeaderLen+len(payload) {
		t.Fatalf("写入长度不符: 得到 %d 期望 %d", n, HeaderLen+len(payload))
	}

	got, err := Decode(buf)
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	if got.Type != TypeData {
		t.Errorf("Type 不符: %v", got.Type)
	}
	if got.Flags != FlagFragment {
		t.Errorf("Flags 不符: %v", got.Flags)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload 不符: %x != %x", got.Payload, payload)
	}
	// 版本缺失时应回落到当前线协议版本
	if got.Version != WireVersion {
		t.Errorf("Version 不符: %d", got.Version)
	}
}

func TestFrameShortBuffer(t *testing.T) {
	if _, err := Decode([]byte{0x10, 0x00}); err != ErrShortBuffer {
		t.Errorf("期望 ErrShortBuffer，得到 %v", err)
	}
}

func TestFrameBadLength(t *testing.T) {
	// 头部声明 100 字节载荷，实际只有 2 字节
	b := []byte{0x10, 0x00, 0x00, 100, 0xaa, 0xbb}
	if _, err := Decode(b); err != ErrBadLength {
		t.Errorf("期望 ErrBadLength，得到 %v", err)
	}
}

func TestFrameEncodeTooShort(t *testing.T) {
	f := Frame{Type: TypeData, Payload: []byte{1, 2, 3, 4}}
	if _, err := f.Encode(make([]byte, 3)); err != ErrShortBuffer {
		t.Errorf("期望 ErrShortBuffer，得到 %v", err)
	}
}

func TestFrameUnknownTypeTolerated(t *testing.T) {
	// 未知类型（如未来版本新增的 Type=9）必须能被解析，而不是报错。
	// 兼容性要求：不认识的消息要容忍，由上层决定处理还是回 NACK。
	b := []byte{0x10 | 0x09, 0x00, 0x00, 0x01, 0x42}
	f, err := Decode(b)
	if err != nil {
		t.Fatalf("未知类型不应导致解析失败: %v", err)
	}
	if uint8(f.Type) != 9 {
		t.Errorf("Type 应为 9，得到 %d", uint8(f.Type))
	}
}

func TestFrameSplit(t *testing.T) {
	f := Frame{Type: TypeKeepalive, Payload: nil}
	buf := make([]byte, f.EncodedLen()+8) // 尾部留噪声，模拟一个报文里有多帧
	n, err := f.Encode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != HeaderLen {
		t.Fatalf("空载荷帧长度应为 %d，得到 %d", HeaderLen, n)
	}
	got, used, err := Split(buf)
	if err != nil {
		t.Fatalf("Split 失败: %v", err)
	}
	if used != HeaderLen {
		t.Errorf("消耗字节应为 %d，得到 %d", HeaderLen, used)
	}
	if got.Type != TypeKeepalive {
		t.Errorf("Type 不符: %v", got.Type)
	}
}

func TestTypeString(t *testing.T) {
	cases := map[Type]string{
		TypeData:      "DATA",
		TypeCtrl:      "CTRL",
		TypeKeepalive: "KEEPALIVE",
		TypeProbe:     "PROBE",
		Type(15):      "UNKNOWN(15)",
	}
	for typ, want := range cases {
		if got := typ.String(); got != want {
			t.Errorf("Type(%d).String() = %q，期望 %q", uint8(typ), got, want)
		}
	}
}
