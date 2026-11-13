package relay

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// memConn 是最小可用的内存连接：Read 从 Reader、Write 到 Writer。
type memConn struct {
	io.Reader
	io.Writer
}

func (m memConn) Close() error                     { return nil }
func (m memConn) LocalAddr() net.Addr              { return nil }
func (m memConn) RemoteAddr() net.Addr             { return nil }
func (m memConn) SetDeadline(time.Time) error      { return nil }
func (m memConn) SetReadDeadline(time.Time) error  { return nil }
func (m memConn) SetWriteDeadline(time.Time) error { return nil }

// TestChanConnTrafficFeedsEwma 验证实时流量的完整链路：
// chanConn.Read/Write → upBytes/downBytes → settleBW → bwUp/bwDown。
//
// 2026-11-13 的教训：addBytes 定义了却全仓无人调用，通道吞吐永远是
// 兜底/0 —— 伟哥开 YouTube 页面流量列还是空。这条链路必须有测试钉住。
func TestChanConnTrafficFeedsEwma(t *testing.T) {
	var sink bytes.Buffer
	c := newChannel(ChannelTLS, nil, nil, "1.2.3.4:8443")
	cc := &chanConn{
		Conn: memConn{Reader: bytes.NewReader(make([]byte, 65536)), Writer: &sink},
		ch:   c,
	}

	// 上行 64KB、下行 32KB
	payload := make([]byte, 1024)
	for i := 0; i < 64; i++ {
		if _, err := cc.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	for i := 0; i < 32; i++ {
		if _, err := cc.Read(payload); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	// 未结算前：pending 有值、速率还没有
	up, down := c.pendingBytes()
	if up != 64*1024 || down != 32*1024 {
		t.Fatalf("pending 不对: up=%d down=%d", up, down)
	}
	if v, ok := c.bwUp.peek(); ok && v > 0 {
		t.Fatalf("结算前不该有速率值: %v", v)
	}

	// 推进 11 秒结算（bytesAt 是创建时刻）
	c.settleBW(time.Now().Add(11 * time.Second))

	upRate, ok := c.bwUp.peek()
	if !ok || upRate <= 0 {
		t.Fatalf("结算后上行速率应>0, ok=%v v=%v", ok, upRate)
	}
	dnRate, ok := c.bwDown.peek()
	if !ok || dnRate <= 0 {
		t.Fatalf("结算后下行速率应>0, ok=%v v=%v", ok, dnRate)
	}
	// 64KB/11s ≈ 47.5 Kbps ≈ 0.047 Mbps（>0 即可，量级再验）
	if upRate > 1 {
		t.Fatalf("上行速率量级不对: %v Mbps", upRate)
	}

	// 结算后 pending 清零
	if up, down := c.pendingBytes(); up != 0 || down != 0 {
		t.Fatalf("结算后 pending 应清零: up=%d down=%d", up, down)
	}

	// 10 秒内的二次结算应被门槛拦住（不产生新样本）
	c.addUp(1024)
	before, _ := c.bwUp.peek()
	c.settleBW(time.Now().Add(11 * time.Second).Add(5 * time.Second)) // 距上次仅 5 秒
	after, _ := c.bwUp.peek()
	if after != before {
		t.Fatalf("10 秒门槛应拦住二次结算: %v -> %v", before, after)
	}
}
