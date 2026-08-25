package proxy

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ido177/shinel/internal/analyzer"
	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/vault"
)

// upstream stands in for the API. It records the body it was handed, which is
// how the test checks that nothing sensitive left the process.
type upstream struct {
	got     chan string
	respond func(w http.ResponseWriter, body string)
}

func newUpstream(t *testing.T, respond func(w http.ResponseWriter, body string)) (*upstream, *httptest.Server) {
	t.Helper()
	u := &upstream{got: make(chan string, 8), respond: respond}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("upstream read: %v", err)
			return
		}
		// Never block: a test that ignores the recorded bodies would
		// otherwise deadlock the upstream once the channel filled up.
		select {
		case u.got <- string(body):
		default:
		}
		u.respond(w, string(body))
	}))
	t.Cleanup(srv.Close)
	return u, srv
}

func (u *upstream) received(t *testing.T) string {
	t.Helper()
	select {
	case body := <-u.got:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received a request")
		return ""
	}
}

func newProxy(t *testing.T, targetURL string, customWords []string) *httptest.Server {
	t.Helper()
	cfg := &config.Config{TargetURL: targetURL}
	h, err := New(cfg, vault.NewInMemoryVault(), analyzer.New(customWords, nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestProxyMasksRequestAndRestoresJSONResponse(t *testing.T) {
	// The upstream echoes back whatever it was sent, so a leak in either
	// direction shows up in the client's copy.
	up, upSrv := newUpstream(t, func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
	px := newProxy(t, upSrv.URL, []string{"Acme"})

	const sent = `{"content":"Acme: mail alice@example.com from 10.0.0.1 card 4111111111111111"}`
	resp := post(t, px.URL, sent)

	got := up.received(t)
	for _, secret := range []string{"Acme", "alice@example.com", "10.0.0.1", "4111111111111111"} {
		if strings.Contains(got, secret) {
			t.Errorf("upstream saw %q in %q", secret, got)
		}
	}
	for _, token := range []string{"[CUSTOM_1]", "[EMAIL_1]", "[IP_1]", "[CARD_1]"} {
		if !strings.Contains(got, token) {
			t.Errorf("upstream body %q is missing %s", got, token)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(body) != sent {
		t.Errorf("client got %q, want the original %q", body, sent)
	}

	// A restored body is longer than the masked one the upstream announced.
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(sent)) {
		t.Errorf("Content-Length = %q, want %d", cl, len(sent))
	}
}

func TestProxyRestoresEventStream(t *testing.T) {
	// Emit the token one byte per event so it can only come back whole if the
	// sliding window survives across chunks.
	up, upSrv := newUpstream(t, func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, piece := range []string{"data: mail ", "[EMA", "IL_", "1", "]", " done\n\n"} {
			io.WriteString(w, piece)
			flusher.Flush()
		}
	})
	px := newProxy(t, upSrv.URL, nil)

	resp := post(t, px.URL, `{"content":"alice@example.com","stream":true}`)
	up.received(t)

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	const want = "data: mail alice@example.com done\n\n"
	if string(body) != want {
		t.Errorf("stream\n got %q\nwant %q", body, want)
	}
}

// A stream can stop in the middle of something that looked like a token. Those
// held-back bytes still belong to the client.
func TestProxyReleasesDanglingCandidateAtStreamEnd(t *testing.T) {
	_, upSrv := newUpstream(t, func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: cut here [EMA")
	})
	px := newProxy(t, upSrv.URL, nil)

	resp := post(t, px.URL, `{"content":"alice@example.com"}`)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if want := "data: cut here [EMA"; string(body) != want {
		t.Errorf("\n got %q\nwant %q", body, want)
	}
}

// The point of streaming is that an event reaches the client before the
// response ends, so read one event while the upstream is still writing.
func TestProxyStreamsWithoutWaitingForTheEnd(t *testing.T) {
	release := make(chan struct{})
	_, upSrv := newUpstream(t, func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		io.WriteString(w, "data: [EMAIL_1]\n\n")
		flusher.Flush()
		<-release // hold the response open
		io.WriteString(w, "data: bye\n\n")
		flusher.Flush()
	})
	px := newProxy(t, upSrv.URL, nil)
	defer close(release)

	resp := post(t, px.URL, `{"content":"alice@example.com"}`)

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first event: %v", err)
	}
	if want := "data: alice@example.com\n"; line != want {
		t.Errorf("first event = %q, want %q", line, want)
	}
}

// Different requests mask different values to the very same token, so one
// request handing back another's value would be a cross-user leak. Both are
// held in flight at once: run sequentially, each would overwrite the mapping
// before its own response arrived and the test would pass either way.
func TestProxyScopesTokensPerRequest(t *testing.T) {
	emails := []string{"first@example.com", "second@example.com"}

	var barrier sync.WaitGroup
	barrier.Add(len(emails))
	_, upSrv := newUpstream(t, func(w http.ResponseWriter, body string) {
		barrier.Done()
		barrier.Wait() // no reply until every request has stored its mapping
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"echo":"[EMAIL_1]"}`)
	})
	px := newProxy(t, upSrv.URL, nil)

	got := make([]string, len(emails))
	errs := make([]error, len(emails))
	var wg sync.WaitGroup
	for i, email := range emails {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(px.URL, "application/json", strings.NewReader(`{"content":"`+email+`"}`))
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			errs[i] = err
			got[i] = string(body)
		}()
	}
	wg.Wait()

	for i, email := range emails {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if want := `{"echo":"` + email + `"}`; got[i] != want {
			t.Errorf("request %d\n got %q\nwant %q", i, got[i], want)
		}
	}
}

func TestProxyPassesThroughBodilessRequests(t *testing.T) {
	up, upSrv := newUpstream(t, func(w http.ResponseWriter, body string) {
		io.WriteString(w, "ok")
	})
	px := newProxy(t, upSrv.URL, nil)

	resp, err := http.Get(px.URL + "/v1/models")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if got := up.received(t); got != "" {
		t.Errorf("upstream got body %q, want empty", got)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestProxyReportsUpstreamFailure(t *testing.T) {
	px := newProxy(t, "http://127.0.0.1:1", nil) // nothing listens there

	resp := post(t, px.URL, `{"content":"hi"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

func TestNewRejectsBadTarget(t *testing.T) {
	for _, target := range []string{"", "not-a-url", "://missing-scheme"} {
		cfg := &config.Config{TargetURL: target}
		if _, err := New(cfg, vault.NewInMemoryVault(), analyzer.New(nil, nil)); err == nil {
			t.Errorf("New(%q): want error, got nil", target)
		}
	}
}
