package analyzer

import (
	"context"
	"log"
	"strings"
)

// resolveConflicts merges the two detection layers. Regex and dictionary spans
// are authoritative: an ML guess overlapping one is dropped outright, even when
// the guess starts earlier or covers more text. Whatever survives is handed to
// the usual leftmost-longest sweep, which settles guess-against-guess overlaps.
//
// This is why the tiers cannot simply be concatenated: for "Иван Петров
// ivan@x.com" the model returns one PERSON span covering the address too, and
// being both leftmost and longest it would win and swallow the email.
//
// ponytail: overlap is a linear scan of the trusted layer per guess, O(n*m) on
// the handful of spans one request produces. Upgrade path: binary search the
// sorted trusted layer if a text ever carries thousands of spans.
func resolveConflicts(sure, guess []span) []span {
	if len(guess) == 0 {
		return sure
	}

	merged := sure
	for _, g := range guess {
		if !overlapsAny(sure, g) {
			merged = append(merged, g)
		}
	}
	return merged
}

func overlapsAny(spans []span, s span) bool {
	for _, other := range spans {
		if s.start < other.end && other.start < s.end {
			return true
		}
	}
	return false
}

// collectML turns the sidecar's findings into spans. Everything here treats the
// reply as untrusted input: it arrives from another process that can be
// restarted, upgraded, or simply wrong.
func (e *AnalyzerEngine) collectML(ctx context.Context, text string) []span {
	if e.ml == nil {
		return nil
	}

	entities, err := e.ml.Analyze(ctx, text)
	if err != nil {
		// Masking still happened at the regex layer; log and carry on.
		log.Printf("analyzer: ml engine unavailable, continuing without it: %v", err)
		return nil
	}

	offsets := runeOffsets(text)
	spans := make([]span, 0, len(entities))
	for _, ent := range entities {
		start, end, ok := byteRange(offsets, ent.Start, ent.End)
		if !ok {
			log.Printf("analyzer: ml engine returned out of range span %d..%d for %q", ent.Start, ent.End, ent.Label)
			continue
		}
		kind := sanitizeLabel(ent.Label)
		if kind == "" {
			log.Printf("analyzer: ml engine returned unusable label %q", ent.Label)
			continue
		}
		spans = append(spans, span{start, end, kind})
	}
	return spans
}

// runeOffsets lists the byte offset of every character in text, plus a final
// entry for its end, so a character index can be turned into a byte index.
func runeOffsets(text string) []int {
	offsets := make([]int, 0, len(text)+1)
	for i := range text {
		offsets = append(offsets, i)
	}
	return append(offsets, len(text))
}

// byteRange converts a character range into a byte range, rejecting anything
// that would slice outside the text or backwards.
func byteRange(offsets []int, start, end int) (int, int, bool) {
	if start < 0 || end <= start || end > len(offsets)-1 {
		return 0, 0, false
	}
	return offsets[start], offsets[end], true
}

// sanitizeLabel reduces a model label to the alphabet a token kind may use.
// The de-masking grammar in the proxy accepts [A-Z]+ only, so "SECRET_PROJECT"
// has to become "SECRETPROJECT": a token it cannot parse is a value that never
// comes back.
func sanitizeLabel(label string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(label) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
