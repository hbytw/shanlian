package cli

import (
	"errors"
	"testing"
	"time"

	"slx/internal/picker"
	"slx/internal/relay"
)

// TestReportConnectFailure_TargetUnreachableDoesNotTrip 覆盖验收项 a：
// 「用户目标不可达」不推进熔断计数（连失败 10 次也不熔断）。
func TestReportConnectFailure_TargetUnreachableDoesNotTrip(t *testing.T) {
	pick := picker.NewSelector(picker.DefaultWeights())

	for i := 0; i < 10; i++ {
		reportConnectFailure(pick, nil, "egA", &relay.ConnectError{
			Reset:             true,
			Reason:            "dial tcp 1.2.3.4:443: connect: connection refused",
			TargetUnreachable: true,
		})
	}

	if b := pick.BreakerOf("egA"); b != nil {
		t.Fatal("目标不可达不该创建熔断器")
	}
	if n := pick.TargetFailures("egA"); n != 10 {
		t.Fatalf("目标不可达次数应为 10，实际 %d", n)
	}
}

// TestReportConnectFailure_EgressFaultTrips 覆盖验收项 b：
// 「连 agent 本身失败」推进熔断计数，连续失败会熔断。
func TestReportConnectFailure_EgressFaultTrips(t *testing.T) {
	pick := picker.NewSelector(picker.DefaultWeights())
	now := time.Now()

	// relay 层失败（agent 会话断 / 开不了流）—— 不是 ConnectError。
	for i := 0; i < 10; i++ {
		reportConnectFailure(pick, nil, "egA", errors.New("打开复用流失败: EOF"))
	}

	b := pick.BreakerOf("egA")
	if b == nil {
		t.Fatal("出口链路失败应创建熔断器")
	}
	if st, _ := b.State(now); st != "熔断" {
		t.Fatalf("出口链路连续失败应熔断，实际 %q", st)
	}
}

// TestReportConnectFailure_TargetUnreachableStillCountsAsHealthy 确认
// 目标不可达既不熔断，也不会被当成「出口正常」而影响其它判定。
func TestReportConnectFailure_TargetUnreachableStillCountsAsHealthy(t *testing.T) {
	pick := picker.NewSelector(picker.DefaultWeights())
	now := time.Now()
	pick.UpdateL1("egA", 20, 0, 100, now)

	reportConnectFailure(pick, nil, "egA", &relay.ConnectError{
		Reason:            "network is unreachable",
		TargetUnreachable: true,
	})

	d := pick.Pick(nil, "", []string{"egA"}, now)
	if d.Egress != "egA" {
		t.Fatalf("目标不可达不应影响选路，实际 %q（%s）", d.Egress, d.Reason)
	}
}

// TestDNSClientBudgetFitsClientTimeout 确认面向客户端的解析预算
// 不会超过客户端的耐心（一般 5 秒就超时了）。
//
// 出口解析最多 2 秒 + 本地回落最多 2 秒 = 4 秒，
// 客户端才有机会拿到地址（哪怕是被污染的）。
func TestDNSClientBudgetFitsClientTimeout(t *testing.T) {
	const clientPatience = 5 * time.Second
	if dnsClientTimeout*2 > clientPatience {
		t.Fatalf("出口 %s + 本地 %s 的总预算 %s 超过客户端的 %s 耐心",
			dnsClientTimeout, dnsClientTimeout, dnsClientTimeout*2, clientPatience)
	}
	if dnsClientTimeout >= dnsResolveTimeout {
		t.Fatalf("面向客户端的预算 %s 应小于通用超时 %s", dnsClientTimeout, dnsResolveTimeout)
	}
}
