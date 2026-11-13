package relay

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// TestConnectErrorUnwrap 确认 ConnectError 能被 errors.Is 穿透到 cause。
//
// 2026-11-13 12:11 假熔断的根因之一：Open 把 lastErr 拼进 Reason 字符串，
// 错误链断裂 ⇒ 下游（reportConnectFailure）的哨兵与超时判断全部失明
// ⇒ 拥塞超时被当成「出口自己坏了」喂进熔断（香港/日本同时假熔断）。
func TestConnectErrorUnwrap(t *testing.T) {
	inner := fmt.Errorf("i/o deadline reached: %w", os.ErrDeadlineExceeded)
	ce := &ConnectError{
		Reset:  true,
		Reason: "打开复用流失败: " + inner.Error(),
		cause:  inner,
	}

	if !errors.Is(ce, os.ErrDeadlineExceeded) {
		t.Fatal("ConnectError 应当能被 errors.Is 穿透到 cause 里的超时")
	}
	if ce.Error() == "" {
		t.Fatal("Error() 不该为空")
	}
}
