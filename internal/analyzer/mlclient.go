package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxMLResponse caps how much of a reply we are willing to decode, so a
// misbehaving sidecar cannot exhaust memory here.
const maxMLResponse = 8 << 20

// Entity is one span the ML sidecar found. Start and End are offsets in
// characters, matching Python string indexing; the engine converts them to the
// byte offsets Go slices with.
type Entity struct {
	Entity string `json:"entity"`
	Label  string `json:"label"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// MLEngineClient talks to the Python sidecar that runs GLiNER.
type MLEngineClient struct {
	url    string
	labels []string
	http   *http.Client
}

// NewMLEngineClient builds a client bounded by timeout, so a wedged sidecar
// delays a request by at most that long instead of hanging the proxy.
func NewMLEngineClient(url string, labels []string, timeout time.Duration) *MLEngineClient {
	return &MLEngineClient{
		url:    url,
		labels: labels,
		http:   &http.Client{Timeout: timeout},
	}
}

type analyzeRequest struct {
	Text   string   `json:"text"`
	Labels []string `json:"labels"`
}

// Analyze asks the sidecar which entities it finds in text. The caller decides
// what a failure means; for the engine it means carrying on without ML.
func (c *MLEngineClient) Analyze(ctx context.Context, text string) ([]Entity, error) {
	if len(c.labels) == 0 || text == "" {
		return nil, nil
	}

	payload, err := json.Marshal(analyzeRequest{Text: text, Labels: c.labels})
	if err != nil {
		return nil, fmt.Errorf("ml: encode request: %w", err)
	}

	// Bound the call even when the caller's context has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ml: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ml: call %s: %w", c.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ml: %s returned %s", c.url, resp.Status)
	}

	var entities []Entity
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMLResponse)).Decode(&entities); err != nil {
		return nil, fmt.Errorf("ml: decode response: %w", err)
	}
	return entities, nil
}
