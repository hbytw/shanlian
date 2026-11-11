package cli

import (
	"encoding/binary"
	"net"
	"testing"
)

// buildICMPEchoRequest 构造一个合法的 IPv4 ICMP echo 请求包（测试用）。
func buildICMPEchoRequest(src, dst net.IP, payload []byte) []byte {
	ihl := 20
	total := ihl + icmpHeaderLen + len(payload)
	pkt := make([]byte, total)

	// —— IP 头 ——
	pkt[0] = 0x45 // version 4, IHL 5
	pkt[1] = 0
	binary.BigEndian.PutUint16(pkt[2:], uint16(total))
	binary.BigEndian.PutUint16(pkt[4:], 0x1234) // ID
	binary.BigEndian.PutUint16(pkt[6:], 0)
	pkt[8] = 64 // TTL
	pkt[9] = icmpProtoNum
	copy(pkt[12:16], src.To4())
	copy(pkt[16:20], dst.To4())
	binary.BigEndian.PutUint16(pkt[10:], checksumSum(pkt[:ihl]))

	// —— ICMP echo 请求 ——
	pkt[ihl] = icmpEchoReq
	pkt[ihl+1] = 0
	binary.BigEndian.PutUint16(pkt[ihl+4:], 0xABCD) // id
	binary.BigEndian.PutUint16(pkt[ihl+6:], 1)      // seq
	copy(pkt[ihl+icmpHeaderLen:], payload)
	binary.BigEndian.PutUint16(pkt[ihl+2:], checksumSum(pkt[ihl:]))

	return pkt
}

// TestICMPEchoReply 验证合成回复的正确性。
//
// 重点在校验和：ICMP 校验和只覆盖 ICMP 报文、不含伪首部
// （与 TCP/UDP 不同，M3 阶段在这里踩过坑）。
func TestICMPEchoReply(t *testing.T) {
	src := net.IPv4(192, 168, 1, 100) // 客户端
	dst := net.IPv4(8, 8, 8, 8)       // 目标
	payload := []byte("abcdefgh")
	req := buildICMPEchoRequest(src, dst, payload)

	reply := icmpEchoReply(req)
	if reply == nil {
		t.Fatal("合成回复失败")
	}
	if len(reply) != len(req) {
		t.Fatalf("长度不一致: req=%d reply=%d", len(req), len(reply))
	}

	// 源/目的应互换
	gotSrc := net.IP(reply[12:16])
	gotDst := net.IP(reply[16:20])
	if !gotSrc.Equal(dst) {
		t.Errorf("回复源地址 = %v，期望 %v", gotSrc, dst)
	}
	if !gotDst.Equal(src) {
		t.Errorf("回复目的地址 = %v，期望 %v", gotDst, src)
	}

	ihl := int(reply[0]&0x0f) * 4

	// 必须是 echo reply
	if reply[ihl] != icmpEchoResp {
		t.Errorf("ICMP 类型 = %d，期望 %d", reply[ihl], icmpEchoResp)
	}
	if reply[ihl+1] != 0 {
		t.Errorf("ICMP code = %d，期望 0", reply[ihl+1])
	}

	// 载荷（id/seq/数据）必须原样保留
	if !bytesEqual(reply[ihl+4:], req[ihl+4:]) {
		t.Error("ICMP 载荷（id/seq/data）未原样保留")
	}

	// —— 校验和验证：对整个字段重算，结果应为 0 ——
	// （RFC 1071：包含校验和字段本身时，校验和计算结果为 0 表示正确）
	if got := checksumSum(reply[ihl:]); got != 0 {
		t.Errorf("ICMP 校验和错误（重算得 0x%04x，应为 0）", got)
	}
	if got := checksumSum(reply[:ihl]); got != 0 {
		t.Errorf("IP 头校验和错误（重算得 0x%04x，应为 0）", got)
	}

	// 原包不能被改动
	if req[ihl] != icmpEchoReq {
		t.Error("原请求包被就地修改了（应该只改副本）")
	}
}

// TestICMPEchoRequestTarget 验证识别逻辑：只接管 echo request。
func TestICMPEchoRequestTarget(t *testing.T) {
	src := net.IPv4(10, 0, 0, 2)
	dst := net.IPv4(1, 1, 1, 1)

	// 正常请求应被识别
	req := buildICMPEchoRequest(src, dst, []byte("x"))
	got, ok := icmpEchoRequestTarget(req)
	if !ok {
		t.Fatal("正常 echo 请求未被识别")
	}
	if !got.Equal(dst) {
		t.Errorf("识别出的目标 = %v，期望 %v", got, dst)
	}

	cases := []struct {
		name string
		pkt  []byte
	}{
		{"空包", nil},
		{"太短的包", make([]byte, 10)},
		{"非 IPv4", func() []byte { p := make([]byte, 40); p[0] = 0x60; return p }()},
		{"非 ICMP 协议", func() []byte {
			p := buildICMPEchoRequest(src, dst, nil)
			p[9] = 6 // TCP
			return p
		}()},
		{"echo reply 不接管", func() []byte {
			p := buildICMPEchoRequest(src, dst, nil)
			p[20] = icmpEchoResp
			return p
		}()},
	}
	for _, c := range cases {
		if _, ok := icmpEchoRequestTarget(c.pkt); ok {
			t.Errorf("%s：不应被识别为 echo 请求", c.name)
		}
	}
}

// TestChecksumSum 用已知向量验证校验和实现。
func TestChecksumSum(t *testing.T) {
	// 一个校验和正确的 ICMP echo 请求：把校验和字段一起算应为 0
	req := buildICMPEchoRequest(net.IPv4(1, 2, 3, 4), net.IPv4(5, 6, 7, 8), []byte("hello"))
	if got := checksumSum(req[20:]); got != 0 {
		t.Errorf("构造的请求包自身校验和不正确: 0x%04x", got)
	}
	if got := checksumSum(req[:20]); got != 0 {
		t.Errorf("构造的 IP 头校验和不正确: 0x%04x", got)
	}
}

// bytesEqual 是比较小工具（避免为一个函数引入 bytes 依赖差异）。
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
