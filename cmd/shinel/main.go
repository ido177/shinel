package main

import (
	"flag"
	"log"

	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/vault"
)

func main() {
	path := flag.String("config", "config.yaml", "path to the config file")
	flag.Parse()

	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	if _, err := vault.New(cfg.Vault); err != nil {
		log.Fatalf("shinel: %v", err)
	}

	log.Printf("shinel: vault=%s, target=%s, port=%d", cfg.Vault.Type, cfg.TargetURL, cfg.Server.Port)
}
