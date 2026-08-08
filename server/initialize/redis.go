package initialize

import (
	"context"
	"fmt"
	"server/globals"
	"time"

	"github.com/go-redis/redis/v8"
)

// Redis 初始化Redis连接
func Redis() map[string]*redis.Client {
	redisClients := make(map[string]*redis.Client)

	// 创建默认Redis客户端
	redisClient := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%s", globals.GraConf.Redis.Host, globals.GraConf.Redis.Port),
		Password:     globals.GraConf.Redis.Password,
		DB:           globals.GraConf.Redis.DB,
		PoolSize:     10,               // 连接池大小
		MinIdleConns: 2,                // 最小空闲连接数
		DialTimeout:  5 * time.Second,  // 连接超时
		ReadTimeout:  3 * time.Second,  // 读取超时
		WriteTimeout: 3 * time.Second,  // 写入超时
		PoolTimeout:  4 * time.Second,  // 连接池超时
		IdleTimeout:  10 * time.Minute, // 空闲连接超时
	})

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := redisClient.Ping(ctx).Result()
	if err != nil {
		globals.Error(fmt.Sprintf("Redis连接失败: %v", err))
		globals.Warn("Redis未连接，相关功能将受限制")
		// 不返回错误，允许程序继续运行
	} else {
		globals.Info(fmt.Sprintf("Redis连接成功: %s:%s", globals.GraConf.Redis.Host, globals.GraConf.Redis.Port))
	}

	redisClients["default"] = redisClient
	redisClients["system"] = redisClient      // 系统级缓存使用同一个实例
	redisClients["suggestions"] = redisClient // 建议缓存使用同一个实例
	redisClients["cache"] = redisClient       // 通用缓存使用同一个实例

	return redisClients
}
