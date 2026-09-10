package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadMLURLEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "ml_engine:\n  url: http://from-file:8000/analyze\n")
	t.Setenv("SHINEL_ML_URL", "http://ml-engine:8000/analyze")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "http://ml-engine:8000/analyze"; cfg.MLEngine.URL != want {
		t.Errorf("MLEngine.URL = %q, want %q", cfg.MLEngine.URL, want)
	}
}

func TestLoadTargetURLEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "target_url: https://api.openai.com\n")
	t.Setenv("SHINEL_TARGET_URL", "http://upstream:8080")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "http://upstream:8080"; cfg.TargetURL != want {
		t.Errorf("TargetURL = %q, want %q", cfg.TargetURL, want)
	}
}

func TestLoadModelFromFile(t *testing.T) {
	path := writeConfig(t, "ml_engine:\n  model: org/other-gliner\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "org/other-gliner"; cfg.MLEngine.Model != want {
		t.Errorf("Model = %q, want %q", cfg.MLEngine.Model, want)
	}
}

func TestLoadAdminBindEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "admin:\n  bind: 127.0.0.1\n  port: 8081\n")
	t.Setenv("SHINEL_ADMIN_BIND", "0.0.0.0")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Admin.Bind; got != "0.0.0.0" {
		t.Errorf("Admin.Bind = %q, want 0.0.0.0", got)
	}
}

func TestLoadAdminTokenEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "admin:\n  token: from-file\n")
	t.Setenv("SHINEL_ADMIN_TOKEN", "from-env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.Token != "from-env" {
		t.Errorf("Token = %q, want from-env", cfg.Admin.Token)
	}
}

func TestLoadEmptyMLURLEnvLeavesFileValue(t *testing.T) {
	path := writeConfig(t, "ml_engine:\n  url: http://from-file:8000/analyze\n")
	t.Setenv("SHINEL_ML_URL", "")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "http://from-file:8000/analyze"; cfg.MLEngine.URL != want {
		t.Errorf("MLEngine.URL = %q, want %q", cfg.MLEngine.URL, want)
	}
}

func TestMLEngineTimeoutZeroUsesDefault(t *testing.T) {
	if got := (MLEngineConfig{TimeoutMS: 0}).Timeout(); got != 2*time.Second {
		t.Errorf("Timeout(0) = %v, want 2s", got)
	}
	if got := (MLEngineConfig{TimeoutMS: -1}).Timeout(); got != 2*time.Second {
		t.Errorf("Timeout(-1) = %v, want 2s", got)
	}
	if got := (MLEngineConfig{TimeoutMS: 500}).Timeout(); got != 500*time.Millisecond {
		t.Errorf("Timeout(500) = %v, want 500ms", got)
	}
}
