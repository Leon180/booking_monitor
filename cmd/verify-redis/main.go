package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	// 1. Connect to Redis
	r := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	if err := r.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// 2. Setup (Reset)
	eventID := 999
	inventoryKey := fmt.Sprintf("event:%d:qty", eventID)
	buyersKey := fmt.Sprintf("event:%d:buyers", eventID)
	r.Del(ctx, inventoryKey, buyersKey)
	r.Set(ctx, inventoryKey, 5, 0) // Inventory = 5

	// Load Script
	script := `
	-- KEYS[1]: inventory_key
	-- KEYS[2]: buyers_key
	-- ARGV[1]: user_id
	-- ARGV[2]: count

	if redis.call("SISMEMBER", KEYS[2], ARGV[1]) == 1 then
		return -2
	end

	local current = tonumber(redis.call("GET", KEYS[1]) or "0")
	if current < tonumber(ARGV[2]) then
		return -1
	end

	redis.call("DECRBY", KEYS[1], ARGV[2])
	redis.call("SADD", KEYS[2], ARGV[1])
	return 1
	`
	sha, err := r.ScriptLoad(ctx, script).Result()
	if err != nil {
		log.Fatalf("Failed to load script: %v", err)
	}

	// 3. Test Cases
	tests := []struct {
		name   string
		userID int
		count  int
		expect int // 1=Success, -1=SoldOut, -2=Duplicate
	}{
		{"User 1 Buy (Success)", 101, 1, 1},
		{"User 1 Buy Again (Duplicate)", 101, 1, -2}, // Duplicate
		{"User 2 Buy (Success)", 102, 1, 1},
		{"User 3 Buy (Success)", 103, 1, 1},
		{"User 4 Buy (Success)", 104, 1, 1},
		{"User 5 Buy (Success)", 105, 1, 1},
		{"User 6 Buy (Sold Out)", 106, 1, -1}, // Sold Out (5-5=0)
	}

	for _, tt := range tests {
		keys := []string{inventoryKey, buyersKey}
		args := []interface{}{tt.userID, tt.count}
		res, err := r.EvalSha(ctx, sha, keys, args...).Int()
		if err != nil {
			log.Printf("❌ %s: Error %v\n", tt.name, err)
			os.Exit(1)
		}

		if res == tt.expect {
			fmt.Printf("✅ %s: Got %d\n", tt.name, res)
		} else {
			fmt.Printf("❌ %s: Expected %d, Got %d\n", tt.name, tt.expect, res)
			os.Exit(1)
		}
	}

	val, _ := r.Get(ctx, inventoryKey).Int()
	fmt.Printf("Final Inventory: %d (Expected 0)\n", val)
}
