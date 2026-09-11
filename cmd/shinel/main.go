package main

import (
	"flag"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ido177/shinel/internal/admin"
	"github.com/ido177/shinel/internal/analyzer"
	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/proxy"
	"github.com/ido177/shinel/internal/stats"
	"github.com/ido177/shinel/internal/vault"
)

func main() {
	path := flag.String("config", "config.yaml", "path to the config file")
	flag.Parse()

	logs := admin.NewLogSink(500)

	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	out := io.MultiWriter(os.Stderr, logs)
	log.SetOutput(out)
	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: cfg.SlogLevel()})))

	v, err := vault.New(cfg.Vault)
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	var ml *analyzer.MLEngineClient
	if cfg.MLEngine.URL != "" {
		ml = analyzer.NewMLEngineClient(cfg.MLEngine.URL, cfg.MLEngine.Labels, cfg.MLEngine.Timeout())
	}

	st := stats.New(100)
	handler, err := proxy.New(cfg, v, analyzer.New(cfg.CustomWords, ml), proxy.WithRecorder(st))
	if err != nil {
		log.Fatalf("shinel: %v", err)
	}

	if cfg.Admin.Port > 0 {
		tok, generated, err := admin.EnsureToken(cfg.Admin.Bind, cfg.Admin.Token)
		if err != nil {
			log.Fatalf("shinel: admin token: %v", err)
		}
		cfg.Admin.Token = tok
		if generated {
			log.Printf("shinel: admin dashboard password=%s", tok)
		}
		go serveAdmin(cfg, st, logs)
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
	log.Printf("shinel: vault=%s, providers=%d, ml=%s, listening on %s", cfg.Vault.Type, len(cfg.Providers), admin.DisplayURL(cfg.Admin.Redact, mlState), addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("shinel: %v", err)
	}
}

func serveAdmin(cfg *config.Config, st *stats.Store, logs *admin.LogSink) {
	addr := net.JoinHostPort(cfg.Admin.Bind, strconv.Itoa(cfg.Admin.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("shinel: admin disabled: %v", err)
		return
	}
	log.Printf("shinel: admin on http://%s", addr)
	srv := &http.Server{
		Handler:           admin.New(cfg, st, logs),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("shinel: admin: %v", err)
	}
}
