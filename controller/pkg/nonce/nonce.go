package nonce

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var (
	ErrNonceExists    = errors.New("Nonce 已存在")
	ErrNonceNotFound  = errors.New("Nonce 不存在")
	ErrNonceExpired   = errors.New("Nonce 已过期")
	ErrTimestampInvalid = errors.New("时间戳无效")
)

// NonceManager Nonce 管理器
type NonceManager struct {
	rdb   *redis.Client
	ttl   time.Duration
}

// NewNonceManager 创建 Nonce 管理器
func NewNonceManager(rdb *redis.Client, ttl time.Duration) *NonceManager {
	return &NonceManager{
		rdb: rdb,
		ttl: ttl,
	}
}

// Generate 生成新 Nonce
func (m *NonceManager) Generate() string {
	return uuid.New().String()
}

// Check 检查 Nonce 是否有效
func (m *NonceManager) Check(ctx context.Context, nonce string) error {
	key := fmt.Sprintf("nonce:%s", nonce)
	exists, err := m.rdb.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("检查 Nonce 失败：%w", err)
	}
	if exists > 0 {
		return ErrNonceExists
	}
	return nil
}

// Add 添加 Nonce
func (m *NonceManager) Add(ctx context.Context, nonce string) error {
	key := fmt.Sprintf("nonce:%s", nonce)
	err := m.rdb.Set(ctx, key, "1", m.ttl).Err()
	if err != nil {
		return fmt.Errorf("添加 Nonce 失败：%w", err)
	}
	return nil
}

// ValidateTimestamp 验证时间戳（±5分钟）
func (m *NonceManager) ValidateTimestamp(timestamp int64) error {
	now := time.Now().Unix()
	diff := now - timestamp
	if diff < -300 || diff > 300 {
		return ErrTimestampInvalid
	}
	return nil
}

// GenerateSignature 生成 HMAC 签名
func (m *NonceManager) GenerateSignature(secret, nonce string, timestamp int64, method, path, body string) string {
	data := fmt.Sprintf("%s%d%s%s%s", nonce, timestamp, method, path, body)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateSignature 验证 HMAC 签名
func (m *NonceManager) ValidateSignature(secret, nonce string, timestamp int64, method, path, body, signature string) bool {
	expected := m.GenerateSignature(secret, nonce, timestamp, method, path, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}
