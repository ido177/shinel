package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/ido177/shinel/internal/analyzer"
	"github.com/ido177/shinel/internal/config"
	"github.com/ido177/shinel/internal/vault"
)

// reqIDKey carries the per-request vault scope from the handler down to the
// director and the response rewriter. A context value rather than a header, so
// the id is never sent upstream.
type reqIDKey struct{}

func reqIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(reqIDKey{}).(string)
	return id
}

// New builds the reverse proxy: requests are masked on the way to the upstream
// API and restored on the way back.
func New(cfg *config.Config, v vault.Vault, a *analyzer.AnalyzerEngine) (http.Handler, error) {
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

		maskRequest(req, v, a)
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		return restoreResponse(resp, v)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy: upstream %s %s failed: %v", r.Method, r.URL.Path, err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
	}

	return withRequestID(rp), nil
}

// withRequestID assigns each request the vault scope its tokens live under.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), reqIDKey{}, uuid.NewString())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// maskRequest swaps sensitive values in the outbound body for tokens and files
// the mapping under this request's id.
//
// Director cannot report an error, so every failure here has to fail safe:
// whatever happens, the body that goes upstream is never less masked than what
// we managed to produce.
func maskRequest(req *http.Request, v vault.Vault, a *analyzer.AnalyzerEngine) {
	if req.Body == nil || req.ContentLength == 0 {
		return
	}

	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		// The body is already partly consumed and cannot be replayed. Send
		// nothing rather than risk forwarding an unmasked remainder.
		log.Printf("proxy: read request body: %v", err)
		setBody(req, nil)
		return
	}

	masked, mapping := a.Anonymize(req.Context(), string(body))
	reqID := reqIDFrom(req.Context())
	for token, value := range mapping {
		if err := v.SaveMapping(req.Context(), reqID, token, value); err != nil {
			// The masked text still goes out, it just will not be restored.
			log.Printf("proxy: save mapping %s: %v", token, err)
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
		resp.Body = newDemaskReader(resp.Body, v, reqID, ctx)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("proxy: read response body: %w", err)
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
