package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/stats"
)

// Handler serves the dashboard and JSON/SSE endpoints.
type Handler struct {
	cfg   *config.Config
	stats *stats.Store
	logs  *LogSink
	pages fs.FS
}

func New(cfg *config.Config, st *stats.Store, logs *LogSink) http.Handler {
	h := &Handler{cfg: cfg, stats: st, logs: logs, pages: uiFS}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /api/config", h.config)
	mux.HandleFunc("GET /api/stats", h.snapshot)
	mux.HandleFunc("GET /api/logs", h.streamLogs)
	return basicAuth(cfg.Admin.Token, mux)
}

func basicAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || subtle.ConstantTimeCompare([]byte(pass), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="shinel"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BindIsLoopback reports whether the admin listener is only reachable on this host.
func BindIsLoopback(bind string) bool {
	switch bind {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

// EnsureToken returns the dashboard password. A non-loopback bind with an empty
// token gets a random one (generated=true) so sibling containers cannot scrape PII.
func EnsureToken(bind, token string) (string, bool, error) {
	if token != "" {
		return token, false, nil
	}
	if BindIsLoopback(bind) {
		return "", false, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(b[:]), true, nil
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(h.pages, "ui/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func (h *Handler) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, publicConfig(h.cfg))
}

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.stats.Snapshot())
}

func (h *Handler) streamLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ioWrite := func(s string) { _, _ = w.Write([]byte(s)) }
	ioWrite("event: snapshot\ndata: {}\n\n")
	for _, line := range h.logs.Snapshot() {
		writeSSE(w, line)
	}
	flusher.Flush()

	ch, cancel := h.logs.Subscribe()
	defer cancel()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, line)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, line string) {
	payload, _ := json.Marshal(line)
	w.Write([]byte("data: "))
	w.Write(payload)
	w.Write([]byte("\n\n"))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

type view struct {
	Server struct {
		Port int `json:"port"`
	} `json:"server"`
	TargetURL   string   `json:"target_url"`
	CustomWords []string `json:"custom_words"`
	Vault       struct {
		Type     string `json:"type"`
		RedisURL string `json:"redis_url,omitempty"`
	} `json:"vault"`
	MLEngine struct {
		URL       string   `json:"url"`
		Model     string   `json:"model"`
		Labels    []string `json:"labels"`
		TimeoutMS int      `json:"timeout_ms"`
	} `json:"ml_engine"`
	Admin struct {
		Port int    `json:"port"`
		Bind string `json:"bind"`
	} `json:"admin"`
}

func publicConfig(cfg *config.Config) view {
	var v view
	v.Server.Port = cfg.Server.Port
	v.TargetURL = redactURL(cfg.TargetURL)
	v.CustomWords = cfg.CustomWords
	v.Vault.Type = cfg.Vault.Type
	v.Vault.RedisURL = redactURL(cfg.Vault.RedisURL)
	v.MLEngine.URL = cfg.MLEngine.URL
	v.MLEngine.Model = cfg.MLEngine.Model
	v.MLEngine.Labels = cfg.MLEngine.Labels
	v.MLEngine.TimeoutMS = cfg.MLEngine.TimeoutMS
	v.Admin.Port = cfg.Admin.Port
	v.Admin.Bind = cfg.Admin.Bind
	return v
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "****")
		} else {
			u.User = url.User("****")
		}
	}
	q := u.Query()
	changed := false
	for k := range q {
		if secretQueryKey(k) {
			q.Set(k, "****")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func secretQueryKey(k string) bool {
	switch strings.ToLower(k) {
	case "password", "pass", "pwd", "token", "api_key", "apikey", "access_token", "secret":
		return true
	default:
		return false
	}
}
