// Package proto 定义 SLX 的线协议。
//
// 设计约束（见 DESIGN.md §9）：
//   - 数据面用紧凑二进制帧，每包开销最小；
//   - 控制面用 JSON，可读、易调试、天然容忍未知字段；
//   - 两个面都带版本，且必须向前/向后兼容：不认识的消息类型要容忍，不能直接断连。
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// WireVersion 是当前实现的线协议版本（4 bit，最大 15）。
const WireVersion uint8 = 1

// HeaderLen 是二进制帧的固定头部长度（字节）。
//
//	0: Ver(4bit) | Type(4bit)
//	1: Flags(8bit)
//	2-3: Payload Length（大端 uint16）
const HeaderLen = 4

// MaxPayload 是单帧可承载的最大载荷。实际受 MTU 限制，通常远小于此值。
const MaxPayload = 65535

// Type 是帧类型（4 bit，取值 0..15）。
type Type uint8

const (
	// TypeData 载荷为一个完整的 IP 包（IPv4 或 IPv6），出口节点据此做 NAT。
	TypeData Type = 1
	// TypeCtrl 载荷为一条 JSON 控制消息。
	TypeCtrl Type = 2
	// TypeKeepalive 空载荷，用于保活与 RTT 采样。
	TypeKeepalive Type = 3
	// TypeProbe 载荷为探测填充数据，用于 L1 链路吞吐测量。
	TypeProbe Type = 4
)

// String 便于日志输出。
func (t Type) String() string {
	switch t {
	case TypeData:
		return "DATA"
	case TypeCtrl:
		return "CTRL"
	case TypeKeepalive:
		return "KEEPALIVE"
	case TypeProbe:
		return "PROBE"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", uint8(t))
	}
}

// Flags 是帧标志位。
type Flags uint8

const (
	// FlagFragment 表示本帧是一个大包分片链的成员。
	FlagFragment Flags = 1 << 0
	// FlagLastFragment 表示分片链的最后一片。
	FlagLastFragment Flags = 1 << 1
)

// ErrShortBuffer 表示输入字节不足一个完整帧。
var ErrShortBuffer = errors.New("proto: 缓冲区不足以容纳完整帧")

// ErrBadVersion 表示收到不支持的协议版本。
var ErrBadVersion = errors.New("proto: 不支持的协议版本")

// ErrBadLength 表示头部声明的长度与实际不符。
var ErrBadLength = errors.New("proto: 载荷长度与实际不符")

// Frame 是一个解析后的帧。Payload 直接引用输入切片，不做拷贝。
type Frame struct {
	Version uint8
	Type    Type
	Flags   Flags
	Payload []byte
}

// EncodedLen 返回编码后的总字节数。
func (f *Frame) EncodedLen() int { return HeaderLen + len(f.Payload) }

// Encode 把帧编码进 dst，返回实际写入的字节数。
//
// dst 必须至少能容纳 f.EncodedLen() 字节，通常直接复用读缓冲。
func (f *Frame) Encode(dst []byte) (int, error) {
	n := f.EncodedLen()
	if len(dst) < n {
		return 0, ErrShortBuffer
	}
	if len(f.Payload) > MaxPayload {
		return 0, fmt.Errorf("proto: 载荷超长 %d", len(f.Payload))
	}
	v := f.Version
	if v == 0 {
		v = WireVersion
	}
	dst[0] = (v&0x0f)<<4 | (uint8(f.Type) & 0x0f)
	dst[1] = uint8(f.Flags)
	binary.BigEndian.PutUint16(dst[2:4], uint16(len(f.Payload)))
	copy(dst[HeaderLen:], f.Payload)
	return n, nil
}

// Decode 解析一个帧。Payload 是 b 的子切片，调用方需保证 b 在帧生命周期内有效。
//
// 对未知 Type 不做拒绝——兼容性要求「不认识的消息要容忍」。是否处理由上层决定。
func Decode(b []byte) (Frame, error) {
	if len(b) < HeaderLen {
		return Frame{}, ErrShortBuffer
	}
	ver := uint8(b[0] >> 4)
	if ver == 0 || ver > 15 {
		return Frame{}, ErrBadVersion
	}
	typ := Type(b[0] & 0x0f)
	flags := Flags(b[1])
	plen := int(binary.BigEndian.Uint16(b[2:4]))
	if len(b) < HeaderLen+plen {
		return Frame{}, ErrBadLength
	}
	return Frame{
		Version: ver,
		Type:    typ,
		Flags:   flags,
		Payload: b[HeaderLen : HeaderLen+plen],
	}, nil
}

// Split 从流式缓冲区中切出第一个帧，返回该帧占用的字节数。
//
// 用于从 UDP 报文里取帧（一个报文通常恰好包含一帧，但保留扩展余地）。
func Split(b []byte) (Frame, int, error) {
	if len(b) < HeaderLen {
		return Frame{}, 0, ErrShortBuffer
	}
	plen := int(binary.BigEndian.Uint16(b[2:4]))
	total := HeaderLen + plen
	if len(b) < total {
		return Frame{}, 0, ErrShortBuffer
	}
	f, err := Decode(b[:total])
	if err != nil {
		return Frame{}, 0, err
	}
	return f, total, nil
}
