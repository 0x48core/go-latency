package queue

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisKey = "waiting-room:queue"

type RedisQueue struct {
	client *redis.Client
}

func NewRedisQueue(client *redis.Client) *RedisQueue {
	return &RedisQueue{client: client}
}

func (q *RedisQueue) Enqueue(ctx context.Context, sessionID string) (int64, error) {
	score := float64(time.Now().UnixNano())

	// NX = only add if not already a member
	added, err := q.client.ZAddNX(ctx, redisKey, redis.Z{
		Score:  score,
		Member: sessionID,
	}).Result()
	if err != nil {
		return 0, err
	}
	_ = added // 0 means already existed, 1 means newly added

	return q.Position(ctx, sessionID)
}

func (q *RedisQueue) Position(ctx context.Context, sessionID string) (int64, error) {
	// ZRANK returns 0-based index; -1 means not found
	rank, err := q.client.ZRank(ctx, redisKey, sessionID).Result()
	if err == redis.Nil {
		return -1, nil // not in queue
	}
	return rank + 1, nil
}

func (q *RedisQueue) Admit(ctx context.Context, n int64) ([]string, error) {
	// Atomically pop the n lowest-score (oldest) members
	members, err := q.client.ZPopMin(ctx, redisKey, n).Result()
	if err != nil {
		return nil, err
	}

	ids := make([]string, len(members))
	for i, member := range members {
		ids[i] = member.Member.(string)
	}

	return ids, nil
}

func (q *RedisQueue) Size(ctx context.Context) (int64, error) {
	return q.client.ZCard(ctx, redisKey).Result()
}

func (q *RedisQueue) Remove(ctx context.Context, sessionID string) error {
	return q.client.ZRem(ctx, redisKey, sessionID).Err()
}
