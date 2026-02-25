package rdb

import (
	"context"
	"sync"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"api-server/config"
)

var (
	client   *redis.Client
	initOnce sync.Once
)

func GetClient() *redis.Client {
	initOnce.Do(func() {
		if err := Init(); err != nil {
			zap.L().Fatal("redis init failed", zap.Error(err))
		}
	})
	return client
}

// Init 初始化 Redis 客户端连接池
func Init() error {
	client = redis.NewClient(&redis.Options{
		Addr:         config.RedisHost,
		Password:     config.RedisPassword,
		PoolSize:     100,
		MinIdleConns: 50,
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		zap.L().Error("redis连接失败", zap.Error(err))
		return err
	}
	return nil
}

func CloseClient() {
	client.Close()
	client = nil
}
