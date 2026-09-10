package vault

import (
	"context"
	"time"

	cache "github.com/patrickmn/go-cache"
)

// InMemoryVault keeps mappings in the process. Fine for a single instance;
// mappings are lost on restart.
type InMemoryVault struct {
	c *cache.Cache
}

func NewInMemoryVault() *InMemoryVault {
	return &InMemoryVault{c: cache.New(ttl, 10*time.Minute)}
}

func (v *InMemoryVault) SaveMapping(ctx context.Context, reqID, token, realValue string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.c.Set(key(reqID, token), realValue, cache.DefaultExpiration)
	return nil
}

func (v *InMemoryVault) GetMapping(ctx context.Context, reqID, token string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	val, ok := v.c.Get(key(reqID, token))
	if !ok {
		return "", ErrNotFound
	}
	s, ok := val.(string)
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}
