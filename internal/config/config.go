// Package config loads shinel settings from a YAML file.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int `yaml:"port"`
	} `yaml:"server"`
	// Providers maps a path prefix (openai, anthropic, gemini) to an origin.
	// A request to /openai/v1/... is forwarded to that origin at /v1/...
	Providers map[string]string `yaml:"providers"`
	Vault     VaultConfig       `yaml:"vault"`
	// CustomWords are extra strings the analyzer masks alongside the built-in
	// entity detectors.
	CustomWords []string       `yaml:"custom_words"`
	MLEngine    MLEngineConfig `yaml:"ml_engine"`
	Admin       AdminConfig    `yaml:"admin"`
	// LogLevel is debug | info | warn | error. Default info.
	LogLevel string `yaml:"log_level"`
}

// AdminConfig is the local dashboard. It shows logs, the loaded config, and
// recent mask mappings, so it binds to loopback by default.
type AdminConfig struct {
	Port int    `yaml:"port"`
	Bind string `yaml:"bind"`
	// Token is the dashboard password (user "admin" for HTTP basic).
	// Required when Bind is not loopback; empty on 127.0.0.1 leaves the UI open.
	Token string `yaml:"token"`
	// Redact hides credentials in the dashboard config view and process logs.
	// Mask mappings on the Stats tab are unaffected.
	Redact bool `yaml:"redact"`
}

// MLEngineConfig points at the Python sidecar. An empty URL turns the model
// layer off, so shinel runs on its own.
type MLEngineConfig struct {
	URL    string   `yaml:"url"`
	Labels []string `yaml:"labels"`
	// Model is the HuggingFace id the Python sidecar loads. Go does not
	// download it; both processes read the same yaml.
	Model string `yaml:"model"`
	// TimeoutMS bounds one call to the sidecar. Milliseconds rather than a
	// duration string, so a typo cannot turn into a parse error at startup.
	TimeoutMS int `yaml:"timeout_ms"`
}

// Timeout is TimeoutMS as a duration.
func (m MLEngineConfig) Timeout() time.Duration {
	if m.TimeoutMS <= 0 {
		return 2 * time.Second
	}
	return time.Duration(m.TimeoutMS) * time.Millisecond
}

type VaultConfig struct {
	Type     string `yaml:"type"`
	RedisURL string `yaml:"redis_url"`
}

func defaults() *Config {
	cfg := &Config{
		Providers: map[string]string{
			"openai":    "https://api.openai.com",
			"anthropic": "https://api.anthropic.com",
			"gemini":    "https://generativelanguage.googleapis.com",
		},
		Vault: VaultConfig{
			Type:     "memory",
			RedisURL: "redis://localhost:6379/0",
		},
		MLEngine: MLEngineConfig{
			Model:     "urchade/gliner_multi-v2.1",
			Labels:    []string{"PERSON", "ORG", "LOCATION", "PASSWORD", "SECRET"},
			TimeoutMS: 2000,
		},
		Admin: AdminConfig{
			Port:   8081,
			Bind:   "127.0.0.1",
			Redact: true,
		},
	}
	cfg.Server.Port = 8080
	cfg.LogLevel = "info"
	return cfg
}

// SlogLevel maps LogLevel to a slog level. Unknown values are info.
func (c *Config) SlogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Load reads path and overlays it onto the defaults: fields absent from the
// file keep their default value.
func Load(path string) (*Config, error) {
	cfg := defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	var leftover struct {
		TargetURL string `yaml:"target_url"`
	}
	if err := yaml.Unmarshal(data, &leftover); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if leftover.TargetURL != "" {
		return nil, fmt.Errorf("config %s: target_url is removed; set providers.openai instead", path)
	}
	// SHINEL_ML_URL wins over the file so one config works both on a laptop and
	// in compose, where the sidecar answers at a service name that does not
	// resolve anywhere else.
	if url := os.Getenv("SHINEL_ML_URL"); url != "" {
		cfg.MLEngine.URL = url
	}
	// Itest points the openai prefix at the echo container. Other providers stay as in yaml.
	if url := os.Getenv("SHINEL_TARGET_URL"); url != "" {
		if cfg.Providers == nil {
			cfg.Providers = map[string]string{}
		}
		cfg.Providers["openai"] = url
	}
	if bind := os.Getenv("SHINEL_ADMIN_BIND"); bind != "" {
		cfg.Admin.Bind = bind
	}
	if tok := os.Getenv("SHINEL_ADMIN_TOKEN"); tok != "" {
		cfg.Admin.Token = tok
	}
	return cfg, nil
}
