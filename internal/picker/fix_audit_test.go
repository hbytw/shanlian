package picker

import (
	"testing"
	"time"
)

// —— 2026-11-12 审计的两条严重缺陷的验收测试 ——
//
// 缺陷 1：熔断的冷却与指数退避，会被 L1 探测的成功绕过
//         ⇒ 熔断后约 30 秒就复活，退避永远升不上去
//
// 缺陷 2：链路探测与业务结果混在同一个成功率窗口
//         ⇒ L1 每 2 秒成功一次，业务失败被稀释
//         ⇒ 出口真的不能用也判不出不健康

// TestFix1_LinkFailureDoesNotTripBreaker 验证链路失败不再熔断出口
//
// ★ 这是缺陷 2 的核心验收
//
// 熔断一个出口的**唯一**正当理由是「它的业务持续失败」
// 链路通 ≠ 出口可用
func TestFix1_LinkFailureDoesNotTripBreaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 连着 60 次链路失败（2 秒一次 = 2 分钟）
	for i := 0; i < 60; i++ {
		s.ReportLinkHealth("egA", false, "L1 出口链路超时",
			now.Add(time.Duration(i)*2*time.Second))
	}

	b := s.BreakerOf("egA")
	if b == nil {
		// ★ 熔断器压根不该被创建 —— 链路信号完全不碰熔断
		return
	}
	if st, _ := b.State(now.Add(2 * time.Minute)); st == "熔断" {
		t.Fatal("链路失败不该熔断出口（链路通 ≠ 出口可用）")
	}
}

// TestFix1_LinkFailureDoesNotClearBusinessFailure 验证链路成功不清业务失败计数
//
// ★ 这是缺陷 2 的另一半
//
// 原来的实现里，L1 每 2 秒成功一次 ⇒ 每次都清零熔断计数
// ⇒ 业务失败永远攒不够
func TestFix1_LinkFailureDoesNotClearBusinessFailure(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 业务失败两次（跨过判定点 ⇒ 会报给熔断器 ）
	s.ReportHealthWithReason("egA", false, "出口链路失败：超时", now)
	s.ReportHealthWithReason("egA", false, "出口链路失败：超时", now.Add(16*time.Second))
	s.ReportHealthWithReason("egA", false, "出口链路失败：超时", now.Add(32*time.Second))

	b := s.BreakerOf("egA")
	if b == nil {
		t.Fatal("业务失败应当创建熔断器")
	}
	// ★ 链路成功再来一批，不该把失败计数清掉
	for i := 0; i < 30; i++ {
		s.ReportLinkHealth("egA", true, "", now.Add(time.Duration(40+i)*2*time.Second))
	}
	// ★ 继续业务失败 ⇒ 应该走到熔断
	//
	// ★ 业务流量要**连续** ：判定只在有样本到达时才会发生
	//   ⇒ 中间留空档的话，后面的判定就不会触发
	//   （真实场景里 agent 坏了 ⇒ 请求持续失败 ⇒ 判定持续发生 ）
	for i := 0; i < 30; i++ {
		s.ReportHealthWithReason("egA", false, "出口链路失败：超时",
			now.Add(time.Duration(70+i)*2*time.Second))
	}
	st, _ := b.State(now.Add(4 * time.Minute))
	if st != "熔断" {
		t.Fatalf("业务持续失败应当熔断，实际 %q", st)
	}
}

// TestFix2_SuccessInOpenStateDoesNotCloseBreaker 验证熔断态的一次成功不再直接关闭熔断
//
// ★ 这是缺陷 1 的核心验收
func TestFix2_SuccessInOpenStateDoesNotCloseBreaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 先让它熔断
	tripBreaker(t, s, "egA", now)
	b := s.BreakerOf("egA")
	if st, _ := b.State(now); st != "熔断" {
		t.Fatalf("前置条件不满足：egA 应处于熔断，实际 %q", st)
	}

	// ★ 熔断期间来一批成功
	// （模拟 L1 探测继续成功 —— 链路本来就是通的 ）
	for i := 0; i < 20; i++ {
		s.ReportHealthWithReason("egA", true, "", now.Add(time.Duration(i+1)*2*time.Second))
	}

	// ★ 仍应处于熔断
	// （Allow() 尚未被调用 ⇒ 状态还是 open ）
	if st, _ := b.State(now.Add(50 * time.Second)); st != "熔断" {
		t.Fatalf("熔断中的一次成功不该关闭熔断，实际 %q", st)
	}
}

// TestFix2_HalfOpenSuccessClosesBreaker 验证半开态的成功才关闭熔断
//
// ★ 冷却期走完 ⇒ Allow() 转半开 ⇒ 试探成功 ⇒ 才算恢复
func TestFix2_HalfOpenSuccessClosesBreaker(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	tripBreaker(t, s, "egA", now)
	b := s.BreakerOf("egA")

	// 冷却期走完 ⇒ Allow() 把它转成半开
	//
	// ★ 熔断时刻不是 now：判定要攒够 3 次（30 秒 × 3）
	//   ⇒ 熔断发生在 now+105 秒左右
	//   ⇒ 冷却期（60 秒）要到 now+165 秒之后才走完
	later := now.Add(4 * time.Minute)
	if !b.Allow(later) {
		t.Fatal("冷却期过后应当允许试探")
	}
	if st, _ := b.State(later); st != "试探" {
		t.Fatalf("冷却期过后应处于试探态，实际 %q", st)
	}

	// 试探成功 ⇒ 关闭
	b.RecordWithReason(true, "", later)
	if st, _ := b.State(later); st != "正常" {
		t.Fatalf("半开态的成功应当关闭熔断，实际 %q", st)
	}
}

// TestFix2_ExponentialBackoffEscalates 验证指数退避能一级级升上去
//
// ★ 这是缺陷 1 的另一半
//
// 原来的实现里，任何一次成功都把 trips 清零 ⇒ 退避永远是 60 秒
func TestFix2_ExponentialBackoffEscalates(t *testing.T) {
	b := NewBreakerWithThreshold(1)
	now := time.Now()

	// 第 1 次熔断 ⇒ 冷却 60 秒
	b.RecordWithReason(false, "x", now)
	if got := b.cooldown; got != 60*time.Second {
		t.Fatalf("首次熔断冷却应为 60s，实际 %s", got)
	}
	if st, _ := b.State(now); st != "熔断" {
		t.Fatalf("应处于熔断，实际 %q", st)
	}

	// ★ open 态的成功不该清退避
	b.RecordWithReason(true, "", now.Add(time.Second))
	if got := b.cooldown; got != 60*time.Second {
		t.Fatalf("open 态的成功不该改冷却时间，实际 %s", got)
	}
	if b.trips != 1 {
		t.Fatalf("open 态的成功不该清退避计数，实际 %d", b.trips)
	}

	// 冷却走完 → 半开
	after := now.Add(2 * time.Minute)
	if !b.Allow(after) {
		t.Fatal("冷却过后应当允许试探")
	}
	// 试探失败 ⇒ 立刻重新熔断，冷却翻倍
	b.RecordWithReason(false, "x", after)
	if got := b.cooldown; got != 120*time.Second {
		t.Fatalf("第二次熔断冷却应为 120s，实际 %s", got)
	}

	// 继续升
	after2 := after.Add(3 * time.Minute)
	if !b.Allow(after2) {
		t.Fatal("应当允许试探")
	}
	b.RecordWithReason(false, "x", after2)
	if got := b.cooldown; got != 240*time.Second {
		t.Fatalf("第三次熔断冷却应为 240s，实际 %s", got)
	}
}

// TestFix2_BackoffResetsOnlyAfterRealRecovery 验证真正恢复后退避才清零
func TestFix2_BackoffResetsOnlyAfterRealRecovery(t *testing.T) {
	b := NewBreakerWithThreshold(1)
	now := time.Now()

	b.RecordWithReason(false, "x", now)
	after := now.Add(2 * time.Minute)
	b.Allow(after)
	b.RecordWithReason(false, "x", after) // 第二次熔断，冷却 120s

	// 冷却走完 → 半开 → 试探成功 ⇒ 真正恢复 ⇒ 清退避
	after2 := after.Add(3 * time.Minute)
	b.Allow(after2)
	b.RecordWithReason(true, "", after2)

	if b.trips != 0 {
		t.Fatalf("真正恢复后退避计数应清零，实际 %d", b.trips)
	}
	if b.cooldown != 0 {
		t.Fatalf("真正恢复后冷却应清零，实际 %s", b.cooldown)
	}
}

// TestFix_BusinessFailureStillTrips 确认功能没被改没
//
// ★ 熔断能力必须还在
func TestFix_BusinessFailureStillTrips(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	tripBreaker(t, s, "egA", now)
	if st, _ := s.BreakerOf("egA").State(now); st != "熔断" {
		t.Fatal("业务持续失败应当熔断")
	}
}

// TestFix_LinkHealthIsObservable 验证链路状态仍能被观测
//
// 链路失败不熔断，但状态页要看得到
func TestFix_LinkHealthIsObservable(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	for i := 0; i < 5; i++ {
		s.ReportLinkHealth("egA", false, "L1 超时", now.Add(time.Duration(i)*time.Second))
	}
	if got := s.LinkFailures("egA"); got != 5 {
		t.Fatalf("链路失败计数应为 5，实际 %d", got)
	}
	s.ReportLinkHealth("egA", true, "", now.Add(10*time.Second))
	if got := s.LinkFailures("egA"); got != 0 {
		t.Fatalf("链路成功后计数应清零，实际 %d", got)
	}
}

// TestFix_SoleEgressTrippedStillHasFallback 验证「唯一出口被熔断」不会完全不通
//
// ★ 这是本次修改最重要的安全保证
//
// 熔断变严了（只认业务失败 ）⇒ 万一误伤唯一那台出口
// ⇒ 必须是「还是能走，只是不走它」—— 而不是「全家断网」
func TestFix_SoleEgressTrippedStillHasFallback(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 只有一台出口，且它业务持续失败 ⇒ 被熔断
	s.UpdateL1("egA", 20, 0, 100, now)
	tripBreaker(t, s, "egA", now)

	if st, _ := s.BreakerOf("egA").State(now); st != "熔断" {
		t.Fatalf("前置条件不满足：egA 应处于熔断，实际 %q", st)
	}

	// ★ 选路必须仍返回一个出口 （兜底路径 ）
	d := s.Pick(nil, "", []string{"egA"}, now.Add(2*time.Minute))
	if d.Egress == "" {
		t.Fatal("唯一出口熔断时也必须返回一个兜底出口，不能完全不通")
	}
	if d.Egress != "egA" {
		t.Fatalf("兜底应当仍选 egA，实际 %q", d.Egress)
	}
	if d.Source != "兜底" {
		t.Fatalf("来源应为兜底，实际 %q", d.Source)
	}
}

// TestFix_TargetUnreachableNeverTrips 验证「用户目标不可达」永远不熔断出口
//
// ★ 这是防止「全家断网」的第二道保险
//
// 用户访问几个打不开的网站 ⇒ 绝不能把（往往也是唯一那台）出口熔断掉
func TestFix_TargetUnreachableNeverTrips(t *testing.T) {
	s := NewSelector(DefaultWeights())
	now := time.Now()

	// 模拟 200 次「目标不可达」（在 picker 侧就是不上报健康失败）
	for i := 0; i < 200; i++ {
		s.ReportTargetFailure("egA")
	}
	if b := s.BreakerOf("egA"); b != nil {
		if st, _ := b.State(now); st == "熔断" {
			t.Fatal("目标不可达不该熔断出口")
		}
	}
	if n := s.TargetFailures("egA"); n != 200 {
		t.Fatalf("目标不可达计数应为 200，实际 %d", n)
	}
}
