package model

import (
	"context"
	"sync"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

type miniRedisElapsedTimeHook struct {
	server *miniredis.Miniredis
	now    func() time.Time
	mu     sync.Mutex
	last   time.Time
}

func (hook *miniRedisElapsedTimeHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	now := hook.now()
	if elapsed := now.Sub(hook.last); elapsed > 0 {
		hook.server.FastForward(elapsed)
		hook.last = now
	}
	return ctx, nil
}

func (*miniRedisElapsedTimeHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (hook *miniRedisElapsedTimeHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return hook.BeforeProcess(ctx, nil)
}

func (*miniRedisElapsedTimeHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}
