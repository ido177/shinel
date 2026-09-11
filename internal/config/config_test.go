package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadRejectsTargetURL(t *testing.T) {
	path := writeConfig(t, "target_url: http://upstream:8080\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for leftover target_url")
	}
}

func TestLoadOpenAIEnvOverridesProvider(t *testing.T) {
	path := writeConfig(t, "providers:\n  openai: https://api.openai.com\n")
	t.Setenv("SHINEL_TARGET_URL", "http://upstream:8080")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "http://upstream:8080"; cfg.Providers["openai"] != want {
		t.Errorf("Providers[openai] = %q, want %q", cfg.Providers["openai"], want)
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

func TestLoadAdminRedactDefaultTrue(t *testing.T) {
	path := writeConfig(t, "admin:\n  port: 8081\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Admin.Redact {
		t.Fatal("Redact default is false, want true")
	}
}

func TestLoadAdminRedactFalse(t *testing.T) {
	path := writeConfig(t, "admin:\n  redact: false\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.Redact {
		t.Fatal("Redact = true, want false")
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

func TestLoadLogLevel(t *testing.T) {
	path := writeConfig(t, "log_level: debug\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestSlogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"nope", slog.LevelInfo},
	}
	for _, tc := range tests {
		if got := (&Config{LogLevel: tc.in}).SlogLevel(); got != tc.want {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLoadDefaultProviders(t *testing.T) {
	path := writeConfig(t, "server:\n  port: 8080\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["openai"] != "https://api.openai.com" {
		t.Errorf("openai = %q", cfg.Providers["openai"])
	}
	if cfg.Providers["anthropic"] != "https://api.anthropic.com" {
		t.Errorf("anthropic = %q", cfg.Providers["anthropic"])
	}
	if cfg.Providers["gemini"] != "https://generativelanguage.googleapis.com" {
		t.Errorf("gemini = %q", cfg.Providers["gemini"])
	}
}

func TestLoadDefaultMLLabels(t *testing.T) {
	path := writeConfig(t, "server:\n  port: 8080\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"PERSON", "ORG", "LOCATION", "PASSWORD", "SECRET"}
	if strings.Join(cfg.MLEngine.Labels, ",") != strings.Join(want, ",") {
		t.Errorf("Labels = %v, want %v", cfg.MLEngine.Labels, want)
	}
}

func TestLoadProvidersKeepsOthersWhenOpenAIEnvSet(t *testing.T) {
	path := writeConfig(t, "providers:\n  openai: https://api.openai.com\n  anthropic: https://api.anthropic.com\n")
	t.Setenv("SHINEL_TARGET_URL", "http://upstream:8080")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["openai"] != "http://upstream:8080" {
		t.Errorf("openai = %q", cfg.Providers["openai"])
	}
	if cfg.Providers["anthropic"] != "https://api.anthropic.com" {
		t.Errorf("anthropic overwritten: %v", cfg.Providers)
	}
}
