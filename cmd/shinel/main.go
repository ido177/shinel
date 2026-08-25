package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ido177/shinel/internal/analyzer"
	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/proxy"
	"github.com/ido177/shinel/internal/vault"
)

func main() {
	path := flag.String("config", "config.yaml", "path to the config file")
	flag.Parse()

	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	v, err := vault.New(cfg.Vault)
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	var ml *analyzer.MLEngineClient
	if cfg.MLEngine.URL != "" {
		ml = analyzer.NewMLEngineClient(cfg.MLEngine.URL, cfg.MLEngine.Labels, cfg.MLEngine.Timeout())
	}

	handler, err := proxy.New(cfg, v, analyzer.New(cfg.CustomWords, ml))
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	addr := net.JoinHostPort("", strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// No ReadTimeout or WriteTimeout: either one would cut a long
		// completion stream short. ReadHeaderTimeout still bounds a client
		// that opens a connection and never finishes its headers.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	mlState := "off"
	if ml != nil {
		mlState = cfg.MLEngine.URL
	}
	log.Printf("shinel: vault=%s, target=%s, ml=%s, listening on %s", cfg.Vault.Type, cfg.TargetURL, mlState, addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("shinel: %v", err)
	}
}
