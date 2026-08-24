package vault

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisVault keeps mappings in Redis so several shinel instances can serve
// halves of the same request.
type RedisVault struct {
	client *redis.Client
}

func NewRedisVault(url string) (*RedisVault, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("vault: bad redis url: %w", err)
	}
	return &RedisVault{client: redis.NewClient(opt)}, nil
}

func (v *RedisVault) SaveMapping(reqID, token, realValue string) error {
	if err := v.client.Set(context.Background(), key(reqID, token), realValue, ttl).Err(); err != nil {
		return fmt.Errorf("vault: save mapping: %w", err)
	}
	return nil
}

func (v *RedisVault) GetMapping(reqID, token string) (string, error) {
	val, err := v.client.Get(context.Background(), key(reqID, token)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("vault: get mapping: %w", err)
	}
	return val, nil
}

func (v *RedisVault) Close() error {
	return v.client.Close()
}
