// Package proxy forwards requests to the upstream API and restores the values
// that were masked on the way in.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/ido177/shinel/internal/vault"
)

var (
	_ http.ResponseWriter = (*StreamingResponseWriter)(nil)
	_ http.Flusher        = (*StreamingResponseWriter)(nil)
)

// maxTokenLen caps how long a token candidate may grow before we give up on it
// and pass it through. Real tokens are far shorter; the cap only stops an
// unclosed bracket from buffering the whole response.
const maxTokenLen = 64

// StreamingResponseWriter restores masked values in a response as it streams
// by. A token may arrive split across chunks ("[", "EMAIL_", "1]"), so a byte
// that could still start a token is held back instead of being forwarded.
//
// It is not safe for concurrent use: net/http calls a handler's writer from a
// single goroutine.
type StreamingResponseWriter struct {
	w     http.ResponseWriter
	f     http.Flusher
	vault vault.Vault
	reqID string
	ctx   context.Context
	buf   []byte
	err   error
}

func NewStreamingResponseWriter(w http.ResponseWriter, v vault.Vault, reqID string, ctx context.Context) *StreamingResponseWriter {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &StreamingResponseWriter{w: w, vault: v, reqID: reqID, ctx: ctx}
	if f, ok := w.(http.Flusher); ok {
		s.f = f
	}
	return s
}

func (s *StreamingResponseWriter) Header() http.Header {
	return s.w.Header()
}

// WriteHeader drops Content-Length because restoring values changes the body
// length, which would leave the announced size wrong.
func (s *StreamingResponseWriter) WriteHeader(statusCode int) {
	s.w.Header().Del("Content-Length")
	s.w.WriteHeader(statusCode)
}

// Write forwards p with tokens replaced by their real values. It always reports
// every byte of p as consumed: bytes held in the token buffer are not lost, and
// io.Writer treats a short count as an error.
func (s *StreamingResponseWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}

	out := make([]byte, 0, len(p))
	for _, b := range p {
		if len(s.buf) == 0 {
			if b == '[' {
				s.buf = append(s.buf, b)
			} else {
				out = append(out, b)
			}
			continue
		}

		s.buf = append(s.buf, b)
		switch classify(s.buf) {
		case completeToken:
			out = append(out, s.resolve(s.buf)...)
			s.buf = s.buf[:0]
		case deadToken:
			out = s.release(out)
		case partialToken:
			if len(s.buf) >= maxTokenLen {
				out = append(out, s.buf...)
				s.buf = s.buf[:0]
			}
		}
	}

	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			s.err = err
			return 0, err
		}
	}
	return len(p), nil
}

// release drains a candidate that just turned out not to be a token. Since the
// buffer is classified after every byte, the byte that killed it is always the
// last one, so it is the only byte that can open the next candidate.
func (s *StreamingResponseWriter) release(out []byte) []byte {
	last := len(s.buf) - 1
	if s.buf[last] == '[' {
		out = append(out, s.buf[:last]...)
		s.buf = append(s.buf[:0], '[')
		return out
	}
	out = append(out, s.buf...)
	s.buf = s.buf[:0]
	return out
}

// resolve looks a complete token up in the vault. An unknown or expired token
// is passed through untouched rather than treated as an error.
func (s *StreamingResponseWriter) resolve(token []byte) []byte {
	value, err := s.vault.GetMapping(s.ctx, s.reqID, string(token))
	if err != nil {
		if !errors.Is(err, vault.ErrNotFound) {
			log.Printf("proxy: vault lookup for %s failed: %v", token, err)
		}
		return token
	}
	return jsonEscape(value)
}

// jsonEscape prepares value for splicing into the JSON string it was masked
// out of, so a quote or backslash cannot break the client's parser.
func jsonEscape(value string) []byte {
	b, err := json.Marshal(value)
	if err != nil { // marshalling a string cannot fail
		return []byte(value)
	}
	return b[1 : len(b)-1]
}

// Flush pushes what has been written so far to the client. A partial token
// stays buffered: releasing it here would defeat the whole point of holding it.
func (s *StreamingResponseWriter) Flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// Close writes out a token candidate left dangling by the end of the stream
// (the model stopped after "[EMA") and flushes. http.ResponseWriter has no
// completion hook, so a handler must defer this call or lose those bytes.
func (s *StreamingResponseWriter) Close() error {
	if s.err != nil {
		return s.err
	}
	if len(s.buf) > 0 {
		if _, err := s.w.Write(s.buf); err != nil {
			s.err = err
			return err
		}
		s.buf = s.buf[:0]
	}
	s.Flush()
	return nil
}

type tokenState int

const (
	partialToken  tokenState = iota // could still grow into a token
	completeToken                   // exactly [KIND_N]
	deadToken                       // cannot become a token
)

// classify reports whether buf, which always starts with '[', is still on its
// way to being a token of the form [KIND_N] produced by the analyzer.
func classify(buf []byte) tokenState {
	i := 1
	for i < len(buf) && buf[i] >= 'A' && buf[i] <= 'Z' {
		i++
	}
	if i == len(buf) {
		return partialToken
	}
	if i == 1 {
		return deadToken // kind must have at least one letter
	}

	if buf[i] != '_' {
		return deadToken
	}
	i++

	digits := i
	for i < len(buf) && buf[i] >= '0' && buf[i] <= '9' {
		i++
	}
	if i == len(buf) {
		return partialToken
	}
	if i == digits {
		return deadToken // number must have at least one digit
	}

	if buf[i] == ']' && i == len(buf)-1 {
		return completeToken
	}
	return deadToken
}
