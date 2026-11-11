package cli

import (
	"errors"
	"testing"
)

// TestProbeErrPhrase 验证探测失败原因被归成固定的几类短语。
//
// 状态页上要显示「熔断（连不上 1.1.1.1（超时））」这类可读信息，
// 归类错了就等于没归类。
func TestProbeErrPhrase(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("dial tcp 1.1.1.1:443: i/o timeout"), "超时"},
		{errors.New("context deadline exceeded"), "超时"},
		{errors.New("dial tcp 8.8.8.8:443: connect: connection refused"), "连接被拒绝"},
		{errors.New("dial tcp: no route to host"), "目标不可达"},
		{errors.New("network is unreachable"), "目标不可达"},
		{errors.New("dial tcp: lookup foo: no such host"), "域名解析失败"},
		{errors.New("something odd happened"), "失败"},
		{nil, "失败"},
	}
	for _, c := range cases {
		if got := probeErrPhrase(c.err); got != c.want {
			t.Errorf("probeErrPhrase(%v) = %q，期望 %q", c.err, got, c.want)
		}
	}
}

// TestProbeFailureReason 验证 L2 失败原因的完整措辞。
func TestProbeFailureReason(t *testing.T) {
	got := probeFailureReason("1.1.1.1", errors.New("i/o timeout"))
	if got != "L2 连不上 1.1.1.1（超时）" {
		t.Fatalf("原因措辞不对：%q", got)
	}
	if got := probeFailureReason("1.1.1.1", nil); got != "L2 目标 1.1.1.1 不可达" {
		t.Fatalf("无错误时措辞不对：%q", got)
	}
}
