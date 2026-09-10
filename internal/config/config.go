// Package config loads shinel settings from a YAML file.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int `yaml:"port"`
	} `yaml:"server"`
	TargetURL string      `yaml:"target_url"`
	Vault     VaultConfig `yaml:"vault"`
	// CustomWords are extra strings the analyzer masks alongside the built-in
	// entity detectors.
	CustomWords []string       `yaml:"custom_words"`
	MLEngine    MLEngineConfig `yaml:"ml_engine"`
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
		TargetURL: "https://api.openai.com",
		Vault: VaultConfig{
			Type:     "memory",
			RedisURL: "redis://localhost:6379/0",
		},
		MLEngine: MLEngineConfig{
			Model:     "urchade/gliner_multi-v2.1",
			Labels:    []string{"PERSON", "ORG", "LOCATION"},
			TimeoutMS: 2000,
		},
	}
	cfg.Server.Port = 8080
	return cfg
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
	// SHINEL_ML_URL wins over the file so one config works both on a laptop and
	// in compose, where the sidecar answers at a service name that does not
	// resolve anywhere else.
	if url := os.Getenv("SHINEL_ML_URL"); url != "" {
		cfg.MLEngine.URL = url
	}
	if url := os.Getenv("SHINEL_TARGET_URL"); url != "" {
		cfg.TargetURL = url
	}
	return cfg, nil
}
