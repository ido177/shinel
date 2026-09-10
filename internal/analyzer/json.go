package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// parseJSON reports whether text is exactly one JSON value. Trailing junk
// means this is not a JSON body and the opaque-text path should run instead.
func parseJSON(text string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return v, true
}

func (e *AnalyzerEngine) anonymizeJSON(ctx context.Context, v any) (string, map[string]string) {
	sess := newSession()
	switch v.(type) {
	case json.Number, float64, bool, nil:
		// A bare number/bool/null is valid JSON; masking it would turn a
		// numeric card into the token [CARD_1], which is not JSON.
		encoded, err := marshalJSON(v)
		if err != nil {
			return "", sess.mapping
		}
		return string(encoded), sess.mapping
	}
	e.walkJSON(ctx, &v, sess)
	encoded, err := marshalJSON(v)
	if err != nil {
		return "", sess.mapping
	}
	return string(encoded), sess.mapping
}

func (e *AnalyzerEngine) walkJSON(ctx context.Context, v *any, sess *session) {
	switch x := (*v).(type) {
	case string:
		masked, _ := e.anonymizeText(ctx, x, sess)
		*v = masked
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c := x[k]
			e.walkJSON(ctx, &c, sess)
			x[k] = c
		}
	case []any:
		for i := range x {
			e.walkJSON(ctx, &x[i], sess)
		}
	}
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
