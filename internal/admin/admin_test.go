package admin

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/stats"
)

func TestConfigRedactsRedisPassword(t *testing.T) {
	tests := []struct {
		name   string
		redis  string
		target string
		leak   string
	}{
		{"userinfo password", "redis://user:secret@localhost:6379/0", "https://api.openai.com", "secret"},
		{"password as user", "redis://secret@localhost:6379/0", "https://api.openai.com", "secret"},
		{"query password", "redis://localhost:6379/0?password=secret", "https://api.openai.com", "secret"},
		{"target basic auth", "redis://localhost:6379/0", "https://user:sk-live@api.example/v1", "sk-live"},
		{"target api_key", "redis://localhost:6379/0", "https://api.example/v1?api_key=sk-live", "sk-live"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Admin.Redact = true
			cfg.Vault.RedisURL = tc.redis
			cfg.TargetURL = tc.target
			rec := httptest.NewRecorder()
			New(cfg, stats.New(10), NewLogSink(10)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			body := rec.Body.String()
			if strings.Contains(body, tc.leak) {
				t.Errorf("leaked %q in %s", tc.leak, body)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	got := RedactURL("https://user:sk-live@api.example/v1?api_key=sk-live")
	if strings.Contains(got, "sk-live") {
		t.Errorf("leaked in %s", got)
	}
}

func TestConfigShowsSecretsWhenRedactOff(t *testing.T) {
	cfg := &config.Config{}
	cfg.Admin.Redact = false
	cfg.Vault.RedisURL = "redis://user:secret@localhost:6379/0"
	cfg.TargetURL = "https://user:sk-live@api.example/v1"
	rec := httptest.NewRecorder()
	New(cfg, stats.New(10), NewLogSink(10)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(body, "secret") || !strings.Contains(body, "sk-live") {
		t.Errorf("redact off, want raw secrets in %s", body)
	}
}

func TestDisplayURL(t *testing.T) {
	raw := "https://user:sk-live@api.example/v1"
	if got := DisplayURL(false, raw); got != raw {
		t.Errorf("redact off = %q, want raw", got)
	}
	if got := DisplayURL(true, raw); strings.Contains(got, "sk-live") {
		t.Errorf("redact on leaked in %s", got)
	}
}

func TestStatsAndIndex(t *testing.T) {
	st := stats.New(10)
	st.Record("POST", "/v1/chat", map[string]string{"[EMAIL_1]": "a@x.com"})
	h := New(&config.Config{}, st, NewLogSink(10))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "<title>Shinel</title>") {
		t.Fatalf("index status %d body %q", rec.Code, body[:min(120, rec.Body.Len())])
	}
	if strings.Contains(body, "<title>Shinel 🧥") {
		t.Error("title should not contain the emoji")
	}
	if !strings.Contains(body, `rel="icon"`) || !strings.Contains(body, "🧥") {
		t.Error("index missing favicon or coat mark")
	}
	if !strings.Contains(body, "<h1>Shinel</h1>") {
		t.Error("index missing service name")
	}
	if !strings.Contains(rec.Body.String(), "https://github.com/ido177/shinel") {
		t.Error("index missing GitHub link")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	var snap stats.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Requests != 1 || snap.Masked != 1 {
		t.Errorf("stats = %+v", snap)
	}
}

func TestLogSSESendsSnapshotThenLive(t *testing.T) {
	logs := NewLogSink(10)
	logs.Write([]byte("hello\n"))
	h := New(&config.Config{}, stats.New(10), logs)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/logs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}

	r := bufio.NewReader(resp.Body)
	first, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(first, "event: snapshot") {
		t.Fatalf("first line %q, want event: snapshot", first)
	}
	found := false
	for range 20 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("snapshot missing hello")
	}

	done := make(chan string, 1)
	go func() {
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(l, "later") {
				done <- l
				return
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	logs.Write([]byte("later\n"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("live line never arrived")
	}
}

func TestLogSinkRing(t *testing.T) {
	s := NewLogSink(2)
	io.WriteString(s, "a\n")
	io.WriteString(s, "b\n")
	io.WriteString(s, "c\n")
	got := s.Snapshot()
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("snapshot = %v", got)
	}
}

func TestAdminAuth(t *testing.T) {
	cfg := &config.Config{}
	cfg.Admin.Token = "s3cret"
	h := New(cfg, stats.New(10), NewLogSink(10))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=\"login\"") {
		t.Errorf("index without auth status %d, want 200 with login form", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("config no auth status %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad login status %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"s3cret"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.AddCookie(cookies[0])
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("cookie auth status %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Error("admin token leaked in /api/config")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.SetBasicAuth("admin", "s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("basic auth status %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.SetBasicAuth("admin", "nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad basic status %d, want 401", rec.Code)
	}
}

func TestEnsureToken(t *testing.T) {
	got, gen, err := EnsureToken("127.0.0.1", "")
	if err != nil || gen || got != "" {
		t.Errorf("loopback empty = %q gen=%v err=%v", got, gen, err)
	}
	got, gen, err = EnsureToken("0.0.0.0", "")
	if err != nil || !gen || len(got) != 32 {
		t.Errorf("public empty = %q gen=%v err=%v", got, gen, err)
	}
	got, gen, err = EnsureToken("0.0.0.0", "set")
	if err != nil || gen || got != "set" {
		t.Errorf("public set = %q gen=%v", got, gen)
	}
}

func TestBindIsLoopback(t *testing.T) {
	if !BindIsLoopback("127.0.0.1") || !BindIsLoopback("::1") || !BindIsLoopback("localhost") {
		t.Fatal("loopback should be true")
	}
	if BindIsLoopback("0.0.0.0") || BindIsLoopback("") {
		t.Fatal("wildcard should be false")
	}
}
