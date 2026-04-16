package verifycode

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// 验证码长度
	codeLength = 6
	// 验证码有效期（分钟）
	codeExpireMinutes = 5
	// 发送频率限制（秒）
	sendRateLimit = 60
	// 单个目标每天最大发送次数
	dailyMaxSends = 10
)

// VerifyCodeManager 验证码管理器
type VerifyCodeManager struct {
	rdb *redis.Client
}

// NewVerifyCodeManager 创建验证码管理器
func NewVerifyCodeManager(rdb *redis.Client) *VerifyCodeManager {
	return &VerifyCodeManager{rdb: rdb}
}

// Generate 生成6位数字验证码
func (m *VerifyCodeManager) Generate() string {
	const digits = "0123456789"
	code := make([]byte, codeLength)
	for i := range code {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		code[i] = digits[n.Int64()]
	}
	return string(code)
}

// SendRateLimit 检查发送频率限制
// 返回 true 表示可以发送，false 表示被限速，remainingSeconds 为剩余等待秒数
func (m *VerifyCodeManager) SendRateLimit(ctx context.Context, target string) (bool, int, error) {
	key := fmt.Sprintf("verify_code:rate:%s", target)

	// 获取当前计数
	count, err := m.rdb.Get(ctx, key).Int()
	if err != nil && err != redis.Nil {
		return false, 0, fmt.Errorf("检查发送频率失败：%w", err)
	}

	if count >= 1 {
		// 获取剩余时间
		ttl, err := m.rdb.TTL(ctx, key).Result()
		if err != nil {
			return false, 0, fmt.Errorf("获取限速剩余时间失败：%w", err)
		}
		return false, int(ttl.Seconds()), nil
	}

	return true, 0, nil
}

// CheckDailyLimit 检查每日发送次数限制
func (m *VerifyCodeManager) CheckDailyLimit(ctx context.Context, target string) (bool, error) {
	key := fmt.Sprintf("verify_code:daily:%s:%s", target, time.Now().Format("20060102"))
	count, err := m.rdb.Get(ctx, key).Int()
	if err != nil && err != redis.Nil {
		return false, fmt.Errorf("检查每日发送次数失败：%w", err)
	}
	return count < dailyMaxSends, nil
}

// RecordSend 记录发送（设置60秒窗口）
func (m *VerifyCodeManager) RecordSend(ctx context.Context, target string) error {
	rateKey := fmt.Sprintf("verify_code:rate:%s", target)
	dailyKey := fmt.Sprintf("verify_code:daily:%s:%s", target, time.Now().Format("20060102"))

	pipe := m.rdb.Pipeline()
	pipe.Set(ctx, rateKey, 1, sendRateLimit*time.Second)
	pipe.Incr(ctx, dailyKey)
	pipe.Expire(ctx, dailyKey, 24*time.Hour)

	_, err := pipe.Exec(ctx)
	return err
}

// Store 保存验证码（5分钟有效期）
func (m *VerifyCodeManager) Store(ctx context.Context, target, code string) error {
	key := fmt.Sprintf("verify_code:%s", target)
	return m.rdb.Set(ctx, key, code, codeExpireMinutes*time.Minute).Err()
}

// Verify 验证验证码
func (m *VerifyCodeManager) Verify(ctx context.Context, target, code string) (bool, error) {
	key := fmt.Sprintf("verify_code:%s", target)
	stored, err := m.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, nil // 验证码不存在或已过期
	}
	if err != nil {
		return false, fmt.Errorf("验证验证码失败：%w", err)
	}

	// 删除已使用的验证码
	m.rdb.Del(ctx, key)

	return stored == code, nil
}

// Delete 删除验证码
func (m *VerifyCodeManager) Delete(ctx context.Context, target string) error {
	key := fmt.Sprintf("verify_code:%s", target)
	return m.rdb.Del(ctx, key).Err()
}
