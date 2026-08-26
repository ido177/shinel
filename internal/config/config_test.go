package config

import (
	"os"
	"path/filepath"
	"testing"
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
