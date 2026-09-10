package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ido177/shinel/internal/vault"
)

const reqID = "req-1"

func newTestVault(t *testing.T, mappings map[string]string) vault.Vault {
	t.Helper()
	v := vault.NewInMemoryVault()
	for token, value := range mappings {
		if err := v.SaveMapping(t.Context(), reqID, token, value); err != nil {
			t.Fatalf("SaveMapping: %v", err)
		}
	}
	return v
}

// stream feeds chunks through a writer and returns everything the client saw.
func stream(t *testing.T, v vault.Vault, chunks []string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s := NewStreamingResponseWriter(rec, v, reqID, t.Context())
	for _, c := range chunks {
		n, err := s.Write([]byte(c))
		if err != nil {
			t.Fatalf("Write(%q): %v", c, err)
		}
		if n != len(c) {
			t.Fatalf("Write(%q) = %d, want %d", c, n, len(c))
		}
		s.Flush()
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return rec.Body.String()
}

// A token may be split anywhere, so every possible chunking of the same input
// must produce the same output.
func TestWriteIsSplitInvariant(t *testing.T) {
	v := newTestVault(t, map[string]string{
		"[EMAIL_1]": "alice@example.com",
		"[IP_1]":    "10.0.0.1",
	})
	const in = "Acme wrote to [EMAIL_1] from [IP_1]."
	const want = "Acme wrote to alice@example.com from 10.0.0.1."

	if got := stream(t, v, []string{in}); got != want {
		t.Fatalf("unsplit\n got %q\nwant %q", got, want)
	}

	for i := 0; i <= len(in); i++ {
		chunks := []string{in[:i], in[i:]}
		if got := stream(t, v, chunks); got != want {
			t.Errorf("split at %d %q\n got %q\nwant %q", i, chunks, got, want)
		}
		for j := i; j <= len(in); j++ {
			chunks := []string{in[:i], in[i:j], in[j:]}
			if got := stream(t, v, chunks); got != want {
				t.Errorf("split at %d,%d %q\n got %q\nwant %q", i, j, chunks, got, want)
			}
		}
	}
}

// Byte-at-a-time is the worst case named in the spec: "[", "EMAIL_", "1]".
func TestWriteBytewise(t *testing.T) {
	v := newTestVault(t, map[string]string{"[EMAIL_1]": "alice@example.com"})
	const in = "to [EMAIL_1] ok"

	chunks := make([]string, 0, len(in))
	for i := range len(in) {
		chunks = append(chunks, in[i:i+1])
	}

	want := "to alice@example.com ok"
	if got := stream(t, v, chunks); got != want {
		t.Errorf("bytewise\n got %q\nwant %q", got, want)
	}
}

func TestWrite(t *testing.T) {
	tests := []struct {
		name     string
		mappings map[string]string
		chunks   []string
		want     string
	}{
		{
			name:     "adjacent tokens",
			mappings: map[string]string{"[EMAIL_1]": "a@x.com", "[IP_1]": "10.0.0.1"},
			chunks:   []string{"[EMAIL_1][IP_1]"},
			want:     "a@x.com10.0.0.1",
		},
		{
			name:     "repeated token",
			mappings: map[string]string{"[EMAIL_1]": "a@x.com"},
			chunks:   []string{"[EMAIL_1] and [EMA", "IL_1]"},
			want:     "a@x.com and a@x.com",
		},
		{
			name:   "markdown link passes through",
			chunks: []string{"see [link](http://x.com)"},
			want:   "see [link](http://x.com)",
		},
		{
			name:   "lowercase bracket passes through",
			chunks: []string{"a [lowercase] b"},
			want:   "a [lowercase] b",
		},
		{
			name:   "token shaped but unknown",
			chunks: []string{"a [EMAIL_9] b"},
			want:   "a [EMAIL_9] b",
		},
		{
			name:     "double bracket keeps the inner token",
			mappings: map[string]string{"[EMAIL_1]": "a@x.com"},
			chunks:   []string{"[[EMAIL_1]"},
			want:     "[a@x.com",
		},
		{
			name:   "malformed shapes pass through",
			chunks: []string{"[EMAIL_] [_1] [EMAIL1] [EMAIL_1x]"},
			want:   "[EMAIL_] [_1] [EMAIL1] [EMAIL_1x]",
		},
		{
			name:     "dangling candidate is flushed on close",
			mappings: map[string]string{"[EMAIL_1]": "a@x.com"},
			chunks:   []string{"cut here [EMA"},
			want:     "cut here [EMA",
		},
		{
			name:     "value is json escaped",
			mappings: map[string]string{"[CUSTOM_1]": `say "hi"\done`},
			chunks:   []string{`{"content":"[CUSTOM_1]"}`},
			want:     `{"content":"say \"hi\"\\done"}`,
		},
		{
			name:     "multibyte value and surrounding text survive",
			mappings: map[string]string{"[CUSTOM_1]": "Акме"},
			chunks:   []string{"привет [CUSTOM_1] пока"},
			want:     "привет Акме пока",
		},
		{
			name:   "no tokens at all",
			chunks: []string{"plain text"},
			want:   "plain text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stream(t, newTestVault(t, tc.mappings), tc.chunks)
			if got != tc.want {
				t.Errorf("\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Text with no token candidate must reach the client immediately, otherwise
// streaming stalls until the response ends.
func TestWriteDoesNotStallPlainText(t *testing.T) {
	rec := httptest.NewRecorder()
	s := NewStreamingResponseWriter(rec, newTestVault(t, nil), reqID, t.Context())

	if _, err := s.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := rec.Body.String(); got != "hello" {
		t.Errorf("before Close: got %q, want %q", got, "hello")
	}

	if _, err := s.Write([]byte(" [EMA")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := rec.Body.String(); got != "hello " {
		t.Errorf("candidate should stay buffered: got %q, want %q", got, "hello ")
	}
}

// A bracket that never closes must not pin the stream: the candidate is capped
// and released mid-write, not held until Close.
func TestWriteCapsCandidateLength(t *testing.T) {
	rec := httptest.NewRecorder()
	s := NewStreamingResponseWriter(rec, newTestVault(t, nil), reqID, t.Context())

	run := "[" + strings.Repeat("A", maxTokenLen*2)
	if _, err := s.Write([]byte(run)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if rec.Body.Len() == 0 {
		t.Error("nothing reached the client: the candidate buffer grew unbounded")
	}
	if len(s.buf) >= maxTokenLen {
		t.Errorf("buffer holds %d bytes, want it capped below %d", len(s.buf), maxTokenLen)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rec.Body.String(); got != run {
		t.Errorf("\n got %q\nwant %q", got, run)
	}
}

func TestFlushKeepsCandidateBuffered(t *testing.T) {
	v := newTestVault(t, map[string]string{"[EMAIL_1]": "a@x.com"})
	rec := httptest.NewRecorder()
	s := NewStreamingResponseWriter(rec, v, reqID, t.Context())

	if _, err := s.Write([]byte("[EMAIL_")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	s.Flush()
	if got := rec.Body.String(); got != "" {
		t.Fatalf("Flush leaked the candidate: got %q", got)
	}

	if _, err := s.Write([]byte("1]")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rec.Body.String(); got != "a@x.com" {
		t.Errorf("got %q, want %q", got, "a@x.com")
	}
}

func TestWriteHeaderDropsContentLength(t *testing.T) {
	rec := httptest.NewRecorder()
	s := NewStreamingResponseWriter(rec, newTestVault(t, nil), reqID, t.Context())

	s.Header().Set("Content-Length", "42")
	s.Header().Set("Content-Type", "text/event-stream")
	s.WriteHeader(http.StatusOK)

	if got := rec.Result().Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want it dropped", got)
	}
	if got := rec.Result().Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want it kept", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		buf  string
		want tokenState
	}{
		{"[", partialToken},
		{"[E", partialToken},
		{"[EMAIL", partialToken},
		{"[EMAIL_", partialToken},
		{"[EMAIL_1", partialToken},
		{"[EMAIL_12", partialToken},
		{"[EMAIL_1]", completeToken},
		{"[IP_1]", completeToken},
		{"[e", deadToken},
		{"[_", deadToken},
		{"[1", deadToken},
		{"[EMAIL1", deadToken},
		{"[EMAIL_]", deadToken},
		{"[EMAIL_1x", deadToken},
		{"[EMAIL[", deadToken},
	}

	for _, tc := range tests {
		if got := classify([]byte(tc.buf)); got != tc.want {
			t.Errorf("classify(%q) = %d, want %d", tc.buf, got, tc.want)
		}
	}
}
