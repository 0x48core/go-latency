package main

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// ================= Use case 1: Memory bloat ================== //

func MemoryBloat(ctx context.Context, rdb *redis.Client, key, val string) {
	// ❌ Bad - buffer 1 million commands in memory
	pipeline := rdb.Pipeline()
	for i := 0; i < 1000000; i++ {
		pipeline.Set(ctx, key, val, 0)
	}
	pipeline.Exec(ctx) // OOM risk

	// ✅ Good - process in small batches
	for i := 0; i < 1_000_000; i += 1000 {
		pipeline := rdb.Pipeline()
		// add 1000 commands
		pipeline.Exec(ctx)
	}
}

// ==================== Use case 2: Error handling is ignored ==================== //

func ErrorHandling(ctx context.Context, rdb *redis.Client) {
	pipeline := rdb.Pipeline()

	// ❌ Bad - ignore errors from individual commands
	cmds, _ := pipeline.Exec(ctx)

	// ✅ Good - check errors for each command

	cmds, err := pipeline.Exec(ctx)
	for _, cmd := range cmds {
		if cmd.Err() != nil {
			// handle error
			slog.Error(err.Error())
		}
	}
}
