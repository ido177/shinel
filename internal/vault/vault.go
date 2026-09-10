// Package vault stores the token -> real value mappings produced while
// anonymizing a request, so the response can be de-anonymized again.
package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ido177/shinel/internal/config"
)

// Vault maps a placeholder token back to the real value it replaced, scoped to
// a single request. ctx lets a cancelled client abort a Redis round-trip.
type Vault interface {
	SaveMapping(ctx context.Context, reqID, token, realValue string) error
	GetMapping(ctx context.Context, reqID, token string) (string, error)
}

// ErrNotFound is returned when a mapping is absent or has expired.
var ErrNotFound = errors.New("vault: mapping not found")

// ttl is the idle lifetime of a mapping. GetMapping refreshes it so a long
// streaming response cannot outlive the values it still needs to restore.
const ttl = 5 * time.Minute

func key(reqID, token string) string {
	return reqID + ":" + token
}

// New builds the Vault implementation named by cfg.Type.
func New(cfg config.VaultConfig) (Vault, error) {
	switch cfg.Type {
	case "memory":
		return NewInMemoryVault(), nil
	case "redis":
		return NewRedisVault(cfg.RedisURL)
	default:
		return nil, fmt.Errorf("vault: unknown type %q, want \"memory\" or \"redis\"", cfg.Type)
	}
}
