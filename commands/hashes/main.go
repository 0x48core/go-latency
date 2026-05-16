package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

// ============================================================
// 1. Store object/entity (User Profile)
// Each service only reads/writes the fields it needs
// ============================================================

func StoreUserProfile(rdb *redis.Client) {
	userKey := "user:1001"

	// Auth service only writes its field
	rdb.HSet(ctx, userKey,
		"email", "john@example.com",
		"password_hash", "hashed_password_xyz",
	)

	// The Profile service only updates fields owned by it
	// Prevents accidental overwrites of Auth service data
	rdb.HSet(ctx, userKey,
		"full_name", "John Doe",
		"avatar_url", "https://cdn.example.com/avatar/1001.jpg",
	)

	// Payment service only writes fields owned by it
	rdb.HSet(ctx, userKey,
		"plan", "premium",
		"billing_email", "john-billing@example.com",
	)
	email, _ := rdb.HGet(ctx, userKey, "email").Result()
	plan, _ := rdb.HGet(ctx, userKey, "plan").Result()
	fmt.Printf("Auth service reads   → email: %s\n", email)
	fmt.Printf("Payment service reads → plan: %s\n", plan)

	// Retrieve the full profile at once
	profile, _ := rdb.HGetAll(ctx, userKey).Result()
	fmt.Printf("Full profile: %v\n\n", profile)
}

// ============================================================
// 2. Grouping Related Keys
// Expire/Delete the entire group at once
// ============================================================

func GroupingRelatedKeys(rdb *redis.Client) {
	sessionKey := "session:abc123"

	// Store entire session data into 1 hash
	rdb.HSet(ctx, sessionKey,
		"user_id", "1001",
		"ip_address", "192.168.1.1",
		"device", "Chrome/MacOS",
		"last_active", "2024-01-15T10:30:00Z",
		"csrf_token", "token_xyz",
	)

	// Expire the entire session at once (instead of expiring each key individually)
	rdb.Expire(ctx, sessionKey, 24*time.Hour)

	// Fetch entire session at once
	session, _ := rdb.HGetAll(ctx, sessionKey).Result()
	fmt.Printf("Session data: %v\n", session)

	// Update only the last_active field while preserving all other fields
	rdb.HSet(ctx, sessionKey, "last_active", "2024-01-15T11:00:00Z")

	// Delete the entire session at once during logout
	// rdb.Del(ctx, sessionKey)
	fmt.Printf("Updated last_active: %s\n\n",
		must(rdb.HGet(ctx, sessionKey, "last_active").Result()))
}

// ============================================================
// 3. Memory Efficiency
// Hash uses ziplist encoding when the number of fields is small
// (< hash-max-ziplist-entries, default: 128)
// Compared to storing each field as a separate key
// ============================================================

func MemoryEfficiency(rdb *redis.Client) {
	rdb.Set(ctx, "user:1002:email", "jane@example.com", 0)
	rdb.Set(ctx, "user:1002:full_name", "Jane Doe", 0)
	rdb.Set(ctx, "user:1002:plan", "free", 0)

	rdb.HSet(ctx, "user:1003",
		"email", "jane@example.com",
		"full_name", "Jane Doe",
		"plan", "free",
	)

	encoding, _ := rdb.ObjectEncoding(ctx, "user:1003").Result()
	fmt.Printf("Hash encoding: %s\n", encoding)
}

func must(s string, err error) string {
	if err != nil {
		return ""
	}
	return s
}
