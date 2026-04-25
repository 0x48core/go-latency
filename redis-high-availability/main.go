package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	masterName = "redis-master"
	password   = "redis-password"
)

func getRedisClient() *redis.Client {
	return redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    masterName,
		SentinelAddrs: []string{"redis-ha.redis.svc.cluster.local:26379"},
		Password:      password,
		DialTimeout:   5 * time.Second,
		ReadTimeout:   5 * time.Second,
		WriteTimeout:  5 * time.Second,
	})
}

func main() {
	ctx := context.Background()
	for {
		r := getRedisClient()
		if err := r.Set(ctx, "app:key", "hello-k8s", 0).Err(); err != nil {
			fmt.Println("Retrying connection...")
			r.Close()
			time.Sleep(2 * time.Second)
			continue
		}

		val, err := r.Get(ctx, "app:key").Result()
		if err != nil {
			fmt.Println("Retrying connection...")
			r.Close()
			time.Sleep(2 * time.Second)
			continue
		}

		fmt.Println(val)
		r.Close()
		break
	}
}
