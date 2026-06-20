package util

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xy/LogAgent/internal/db"
)

// AllowRequest 滑动窗口限流逻辑
// key: 限流标识（如用户IP或全局API标识）
// limit: 窗口内允许的最大请求数
// window: 时间窗口大小
func AllowRequest(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	now := time.Now().UnixNano()
	windowStart := now - window.Nanoseconds()
	redisKey := fmt.Sprintf("ratelimit:%s", key)

	// 使用 Redis 事务管道保证原子性
	pipe := db.RDB.TxPipeline()

	// 1. 移除窗口外的旧数据
	pipe.ZRemRangeByScore(ctx, redisKey, "0", fmt.Sprintf("%d", windowStart))
	// 2. 获取当前窗口内的请求数
	pipe.ZCard(ctx, redisKey)
	// 3. 添加当前请求
	pipe.ZAdd(ctx, redisKey, redis.Z{Score: float64(now), Member: now})
	// 4. 设置过期时间（略大于窗口，防止冷数据堆积）
	pipe.Expire(ctx, redisKey, window+time.Second)

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		return false, err
	}

	// 第 2 个命令 ZCard 的结果即为当前窗口内的请求数
	count := cmds[1].(*redis.IntCmd).Val()

	if int(count) > limit {
		// 如果超限，可以考虑回退（即移除刚刚加进去的当前请求），也可以直接拒绝
		// 这里简单处理，直接拒绝
		return false, nil
	}

	return true, nil
}
