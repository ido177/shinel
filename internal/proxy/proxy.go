package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ido177/shinel/internal/analyzer"
	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/vault"
)

// maxBodyBytes is how much of a request or non-stream response we will hold
// in memory. A larger body is dropped rather than forwarded unmasked.
const maxBodyBytes = 16 << 20

// reqIDKey carries the per-request vault scope from the handler down to the
// director and the response rewriter. A context value rather than a header, so
// the id is never sent upstream.
type reqIDKey struct{}

func reqIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(reqIDKey{}).(string)
	return id
}

// Recorder notes one outbound request and the token mapping produced for it.
// A nil Recorder is ignored.
type Recorder interface {
	Record(method, path string, mapping map[string]string)
}

type options struct {
	rec    Recorder
	redact bool
}

// WithRecorder attaches request stats to the proxy.
func WithRecorder(r Recorder) func(*options) {
	return func(o *options) { o.rec = r }
}

// New builds the reverse proxy: requests are masked on the way to the upstream
// API and restored on the way back.
func New(cfg *config.Config, v vault.Vault, a *analyzer.AnalyzerEngine, opts ...func(*options)) (http.Handler, error) {
	o := options{redact: cfg.Admin.Redact}
	for _, opt := range opts {
		opt(&o)
	}
	origins, err := parseProviders(cfg.Providers)
	if err != nil {
		return nil, err
	}
	if len(origins) == 0 {
		return nil, fmt.Errorf("proxy: providers is empty")
	}

	rp := &httputil.ReverseProxy{}
	rp.Director = func(req *http.Request) {
		name, target, path, ok := route(req.URL.Path, origins)
		if !ok {
			return
		}
		if p, ok := req.Context().Value(providerKey{}).(*providerName); ok {
			p.s = name
		}
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = path
		req.URL.RawPath = ""
		req.Host = target.Host
		req.Header.Del("Accept-Encoding")
		if _, ok := req.Header["User-Agent"]; !ok {
			req.Header.Set("User-Agent", "")
		}

		maskRequest(req, v, a, o.rec, o.redact)
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if err := restoreResponse(resp, v); err != nil {
			slog.Error("proxy restore", "path", resp.Request.URL.Path, "err", err)
			return err
		}
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy upstream", "method", r.Method, "path", r.URL.Path, "err", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
	}

	return withRequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, _, ok := route(r.URL.Path, origins); !ok {
			http.Error(w, "unknown provider", http.StatusNotFound)
			return
		}
		rp.ServeHTTP(w, r)
	})), nil
}

type maskedKey struct{}

type maskedCount struct{ n int }

type providerKey struct{}

type providerName struct{ s string }

func parseOrigin(label, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy: bad %s: %w", label, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy: %s %q needs a scheme and a host", label, raw)
	}
	return u, nil
}

func parseProviders(m map[string]string) (map[string]*url.URL, error) {
	out := make(map[string]*url.URL, len(m))
	for name, raw := range m {
		u, err := parseOrigin("provider "+name, raw)
		if err != nil {
			return nil, err
		}
		out[name] = u
	}
	return out, nil
}

// route picks an origin from the first path segment when it names a provider.
// /openai/v1/chat → openai, /v1/chat. Unknown prefixes are not forwarded.
func route(path string, providers map[string]*url.URL) (name string, target *url.URL, rest string, ok bool) {
	first, rest, ok := splitPrefix(path)
	if !ok {
		return "", nil, path, false
	}
	u, hit := providers[first]
	if !hit {
		return first, nil, path, false
	}
	return first, u, rest, true
}

func splitPrefix(path string) (first, rest string, ok bool) {
	p := strings.TrimPrefix(path, "/")
	if p == "" {
		return "", path, false
	}
	i := strings.IndexByte(p, '/')
	if i < 0 {
		return p, "/", true
	}
	rest = p[i:]
	if rest == "" {
		rest = "/"
	}
	return p[:i], rest, true
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// withRequestLog assigns a vault scope and writes one info line after the
// upstream round-trip: method, path, status, duration, how many values were masked.
func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mc := &maskedCount{}
		pn := &providerName{}
		ctx := context.WithValue(r.Context(), reqIDKey{}, uuid.NewString())
		ctx = context.WithValue(ctx, maskedKey{}, mc)
		ctx = context.WithValue(ctx, providerKey{}, pn)
		r = r.WithContext(ctx)
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		slog.Info("proxy",
			"method", r.Method,
			"path", r.URL.Path,
			"provider", pn.s,
			"status", sw.status(),
			"dur", time.Since(start).Round(time.Millisecond),
			"masked", mc.n,
		)
	})
}

// maskRequest swaps sensitive values in the outbound body for tokens and files
// the mapping under this request's id.
//
// Director cannot report an error, so every failure here has to fail safe:
// whatever happens, the body that goes upstream is never less masked than what
// we managed to produce.
func maskRequest(req *http.Request, v vault.Vault, a *analyzer.AnalyzerEngine, rec Recorder, redact bool) {
	var mapping map[string]string
	defer func() {
		if c, ok := req.Context().Value(maskedKey{}).(*maskedCount); ok {
			c.n = len(mapping)
		}
		if rec != nil {
			rec.Record(req.Method, req.URL.Path, mapping)
		}
	}()

	if req.Body == nil || req.ContentLength == 0 {
		return
	}
	if req.ContentLength > maxBodyBytes {
		slog.Warn("proxy: request body too large, dropping it", "bytes", req.ContentLength, "max", maxBodyBytes)
		req.Body.Close()
		setBody(req, nil)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes+1))
	req.Body.Close()
	if err != nil {
		slog.Warn("proxy: read request body", "err", err)
		setBody(req, nil)
		return
	}
	if int64(len(body)) > maxBodyBytes {
		slog.Warn("proxy: request body too large, dropping it", "bytes", len(body), "max", maxBodyBytes)
		setBody(req, nil)
		return
	}

	masked, mapping := a.Anonymize(req.Context(), string(body))
	reqID := reqIDFrom(req.Context())
	for token, value := range mapping {
		if err := v.SaveMapping(req.Context(), reqID, token, value); err != nil {
			// The masked text still goes out, it just will not be restored.
			slog.Error("proxy: save mapping", "token", token, "err", err)
		}
		if redact {
			slog.Debug("proxy mask", "token", token)
		} else {
			slog.Debug("proxy mask", "token", token, "value", value)
		}
	}
	setBody(req, []byte(masked))
}

// setBody replaces the outbound body and the length that describes it.
func setBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

// restoreResponse puts the real values back. A streamed response is rewritten
// chunk by chunk so the client keeps receiving events as they arrive; anything
// else is rewritten in one go, which lets us keep an accurate Content-Length.
func restoreResponse(resp *http.Response, v vault.Vault) error {
	ctx := resp.Request.Context()
	reqID := reqIDFrom(ctx)

	if isEventStream(resp.Header.Get("Content-Type")) {
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Body = newDemaskReader(resp.Body, v, reqID, ctx)
		return nil
	}

	if resp.ContentLength > maxBodyBytes {
		return fmt.Errorf("proxy: response body %d bytes exceeds %d", resp.ContentLength, maxBodyBytes)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("proxy: read response body: %w", err)
	}
	if int64(len(body)) > maxBodyBytes {
		return fmt.Errorf("proxy: response body exceeds %d bytes", maxBodyBytes)
	}

	restored := demask(body, v, reqID, ctx)
	resp.Body = io.NopCloser(bytes.NewReader(restored))
	resp.ContentLength = int64(len(restored))
	resp.Header.Set("Content-Length", strconv.Itoa(len(restored)))
	return nil
}

func isEventStream(contentType string) bool {
	base, _, err := mime.ParseMediaType(contentType)
	return err == nil && base == "text/event-stream"
}
