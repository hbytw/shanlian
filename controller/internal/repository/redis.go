package repository

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisDB Redis 数据库连接
type RedisDB struct {
	*redis.Client
}

// NewRedisDB 创建 Redis 连接
func NewRedisDB(addr string) *RedisDB {
	opt, err := redis.ParseURL(addr)
	if err != nil {
		// 如果解析失败，尝试作为普通地址处理
		opt = &redis.Options{
			Addr:     addr,
			Password: "",
			DB:       0,
		}
	}
	client := redis.NewClient(opt)

	return &RedisDB{Client: client}
}

// Ping 检查 Redis 连接
func (r *RedisDB) Ping(ctx context.Context) error {
	_, err := r.Client.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("Redis Ping 失败：%w", err)
	}
	return nil
}
