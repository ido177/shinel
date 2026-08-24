package vault

import (
	"errors"
	"os"
	"testing"

	"github.com/ido177/shinel/internal/config"
)

// testVault is the contract every implementation must satisfy.
func testVault(t *testing.T, v Vault) {
	t.Helper()

	if err := v.SaveMapping("req-1", "TOKEN_1", "alice@example.com"); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}
	got, err := v.GetMapping("req-1", "TOKEN_1")
	if err != nil {
		t.Fatalf("GetMapping: %v", err)
	}
	if got != "alice@example.com" {
		t.Errorf("GetMapping = %q, want %q", got, "alice@example.com")
	}

	if _, err := v.GetMapping("req-1", "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing token: err = %v, want ErrNotFound", err)
	}

	// The same token in another request must not leak across.
	if err := v.SaveMapping("req-2", "TOKEN_1", "bob@example.com"); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}
	got, err = v.GetMapping("req-1", "TOKEN_1")
	if err != nil {
		t.Fatalf("GetMapping: %v", err)
	}
	if got != "alice@example.com" {
		t.Errorf("req-1 leaked: got %q, want %q", got, "alice@example.com")
	}
}

func TestInMemoryVault(t *testing.T) {
	testVault(t, NewInMemoryVault())
}

func TestRedisVault(t *testing.T) {
	url := os.Getenv("SHINEL_TEST_REDIS")
	if url == "" {
		t.Skip("set SHINEL_TEST_REDIS to a redis:// url to run this test")
	}
	v, err := NewRedisVault(url)
	if err != nil {
		t.Fatalf("NewRedisVault: %v", err)
	}
	defer v.Close()

	testVault(t, v)
}

func TestNewUnknownType(t *testing.T) {
	if _, err := New(config.VaultConfig{Type: "postgres"}); err == nil {
		t.Error("New with unknown type: want error, got nil")
	}
}
