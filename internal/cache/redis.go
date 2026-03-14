package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/yourname/recommendation-service-v2/internal/domain"
)

const (
	defaultTTL = 10 * time.Minute
)

// Cache wraps Redis operations for caching recommendations
type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewCache creates a new cache instance
func NewCache(redisURL string) (*Cache, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opt)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &Cache{
		client: client,
		ttl:    defaultTTL,
	}, nil
}

// BuildKey constructs cache key for recommendations
func BuildKey(userID int64, limit int32) string {
	return fmt.Sprintf("rec:user:%d:limit:%d", userID, limit)
}

// Get retrieves cached recommendations if available
func (c *Cache) Get(ctx context.Context, userID int64, limit int32) ([]domain.Recommendation, error) {
	key := BuildKey(userID, limit)
	val, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // cache miss, not an error
		}
		// Log but don't fail on Redis error
		return nil, nil
	}

	var recommendations []domain.Recommendation
	if err := json.Unmarshal([]byte(val), &recommendations); err != nil {
		return nil, nil // corrupted cache, treat as miss
	}

	return recommendations, nil
}

// Set stores recommendations in cache with TTL
func (c *Cache) Set(ctx context.Context, userID int64, limit int32, recommendations []domain.Recommendation) error {
	key := BuildKey(userID, limit)
	data, err := json.Marshal(recommendations)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, key, string(data), c.ttl).Err()
}

// InvalidateUser removes all cached recommendations for a user (all limit variants)
func (c *Cache) InvalidateUser(ctx context.Context, userID int64) error {
	pattern := fmt.Sprintf("rec:user:%d:limit:*", userID)
	keys, err := c.client.Keys(ctx, pattern).Result()
	if err != nil {
		// Log but don't fail
		return nil
	}

	if len(keys) > 0 {
		return c.client.Del(ctx, keys...).Err()
	}

	return nil
}

// Close closes the Redis connection
func (c *Cache) Close() error {
	return c.client.Close()
}
