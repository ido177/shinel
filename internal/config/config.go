// Package config loads shinel settings from a YAML file.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int `yaml:"port"`
	} `yaml:"server"`
	TargetURL string      `yaml:"target_url"`
	Vault     VaultConfig `yaml:"vault"`
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
	return cfg, nil
}
