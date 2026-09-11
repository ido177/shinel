// Black-box checks against the docker-compose.itest.yml stack.
// Run from the tester container; make test on the host never sees this module.
package itest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMaskRestoreJSON(t *testing.T) {
	const sent = `{"content":"mail alice@example.com"}`
	resp := postRetry(t, proxyURL(t)+"/openai/", sent)
	defer resp.Body.Close()

	last := getLast(t)
	if strings.Contains(last, "alice@example.com") {
		t.Errorf("upstream saw the email: %q", last)
	}
	if !strings.Contains(last, "[EMAIL_1]") {
		t.Errorf("upstream body %q is missing [EMAIL_1]", last)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	logRoundTrip(t, last, string(got))
	if string(got) != sent {
		t.Errorf("client\n got %q\nwant %q", got, sent)
	}
}

func TestMaskPasswordContext(t *testing.T) {
	const sent = `{"content":"password: hunter2"}`
	resp := postRetry(t, proxyURL(t)+"/openai/", sent)
	defer resp.Body.Close()

	last := getLast(t)
	if strings.Contains(last, "hunter2") {
		t.Errorf("upstream saw the password: %q", last)
	}
	if !strings.Contains(last, "[PASSWORD_1]") {
		t.Errorf("upstream body %q is missing [PASSWORD_1]", last)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	logRoundTrip(t, last, string(got))
	if string(got) != sent {
		t.Errorf("client\n got %q\nwant %q", got, sent)
	}
}

func TestJSONNumberStaysANumber(t *testing.T) {
	const sent = `{"amount":4111111111111111,"note":"alice@example.com"}`
	resp := postRetry(t, proxyURL(t)+"/openai/", sent)
	defer resp.Body.Close()

	last := getLast(t)
	if !strings.Contains(last, "4111111111111111") {
		t.Errorf("upstream lost the numeric amount: %q", last)
	}
	if strings.Contains(last, "alice@example.com") {
		t.Errorf("upstream saw the email: %q", last)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	logRoundTrip(t, last, string(got))
	if string(got) != sent {
		t.Errorf("client\n got %q\nwant %q", got, sent)
	}
}

func TestSSERestoresAndDropsContentLength(t *testing.T) {
	const sent = `{"content":"alice@example.com","stream":true}`
	resp := postRetry(t, proxyURL(t)+"/openai/", sent)
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length = %q, want it dropped for SSE", cl)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	logRoundTrip(t, getLast(t), string(got))
	if !strings.Contains(string(got), "alice@example.com") {
		t.Errorf("stream missing restored email: %q", got)
	}
	if strings.Contains(string(got), "[EMAIL_1]") {
		t.Errorf("stream still has a token: %q", got)
	}
}

func TestGLiNERModelFromConfig(t *testing.T) {
	// Direct sidecar call only proves the image loaded a real model.
	// Masking is a second GLiNER round-trip with conflict filtering, so the
	// entities from this call are not what the proxy must have removed.
	const text = "Ivan Petrov wrote to Alice."
	if n := analyzeCount(t, text); n == 0 {
		t.Fatalf("model from config returned no entities for %q", text)
	}

	payload := `{"content":"` + text + `"}`
	presp := postRetry(t, proxyURL(t)+"/openai/", payload)
	defer presp.Body.Close()
	last := getLast(t)
	if !strings.Contains(last, "[PERSON_") {
		t.Errorf("proxy sent no PERSON token to upstream: %q", last)
	}
	if strings.Contains(last, "Ivan Petrov") {
		t.Errorf("upstream still has Ivan Petrov in %q", last)
	}

	got, err := io.ReadAll(presp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	logRoundTrip(t, last, string(got))
	if !strings.Contains(string(got), text) {
		t.Errorf("client lost the original text:\n got %q\nwant substring %q", got, text)
	}
}

func analyzeCount(t *testing.T, text string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"text":   text,
		"labels": []string{"PERSON", "ORG", "LOCATION"},
	})
	resp, err := httpClient().Post(mlURL(t)+"/analyze", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read analyze: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("analyze status %d: %s", resp.StatusCode, raw)
	}
	var entities []struct {
		Entity string `json:"entity"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(raw, &entities); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return len(entities)
}

func proxyURL(t *testing.T) string    { return mustEnv(t, "PROXY_URL") }
func upstreamURL(t *testing.T) string { return mustEnv(t, "UPSTREAM_URL") }
func mlURL(t *testing.T) string       { return mustEnv(t, "ML_URL") }

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set", key)
	}
	return v
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

func postRetry(t *testing.T, url, body string) *http.Response {
	t.Helper()
	var last error
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := httpClient().Post(url, "application/json", strings.NewReader(body))
		if err == nil {
			return resp
		}
		last = err
		time.Sleep(time.Second)
	}
	t.Fatalf("proxy never accepted connections: %v", last)
	return nil
}

func logRoundTrip(t *testing.T, upstream, client string) {
	t.Helper()
	t.Logf("upstream received:\n%s", upstream)
	t.Logf("client received:\n%s", client)
}

func getLast(t *testing.T) string {
	t.Helper()
	resp, err := httpClient().Get(upstreamURL(t) + "/last")
	if err != nil {
		t.Fatalf("GET /last: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /last: %v", err)
	}
	return string(b)
}
