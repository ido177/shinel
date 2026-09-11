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
	target, err := url.Parse(cfg.TargetURL)
	if err != nil {
		return nil, fmt.Errorf("proxy: bad target url: %w", err)
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("proxy: target url %q needs a scheme and a host", cfg.TargetURL)
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	route := rp.Director
	rp.Director = func(req *http.Request) {
		route(req)
		// NewSingleHostReverseProxy only rewrites the URL, so the Host header
		// would still name shinel and a virtual-hosted API would reject it.
		req.Host = target.Host
		// Ask for an identity encoding: the transport adds its own gzip and
		// transparently decodes it, which keeps response bodies maskable.
		req.Header.Del("Accept-Encoding")

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

	return withRequestLog(rp), nil
}

type maskedKey struct{}

type maskedCount struct{ n int }

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
		ctx := context.WithValue(r.Context(), reqIDKey{}, uuid.NewString())
		ctx = context.WithValue(ctx, maskedKey{}, mc)
		r = r.WithContext(ctx)
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		slog.Info("proxy",
			"method", r.Method,
			"path", r.URL.Path,
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
