package analyzer

import (
	"context"
	"log"
	"sort"
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
func resolveConflicts(sure, guess []span) []span {
	if len(guess) == 0 {
		return sure
	}

	index := newOverlapIndex(sure)
	merged := sure
	for _, g := range guess {
		if !index.overlaps(g) {
			merged = append(merged, g)
		}
	}
	return merged
}

// overlapIndex answers "does this span overlap any trusted span?" in
// O(log n) after an O(n log n) build. sure is copied so the caller's order is
// left alone for the later leftmost-longest sweep.
type overlapIndex struct {
	sure   []span
	maxEnd []int
}

func newOverlapIndex(sure []span) overlapIndex {
	idx := make([]span, len(sure))
	copy(idx, sure)
	sort.Slice(idx, func(i, j int) bool { return idx[i].start < idx[j].start })
	maxEnd := make([]int, len(idx))
	for i, s := range idx {
		maxEnd[i] = s.end
		if i > 0 && maxEnd[i-1] > maxEnd[i] {
			maxEnd[i] = maxEnd[i-1]
		}
	}
	return overlapIndex{sure: idx, maxEnd: maxEnd}
}

func (o overlapIndex) overlaps(g span) bool {
	// Last trusted span that starts before g ends. Among those, if the
	// furthest end crosses g.start, something overlaps.
	i := sort.Search(len(o.sure), func(i int) bool { return o.sure[i].start >= g.end }) - 1
	if i < 0 {
		return false
	}
	return o.maxEnd[i] > g.start
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
		// Regex/dictionary masking still ran. Names only the model would have
		// caught can now leave the process; a down sidecar must not take the
		// proxy down with it.
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
		got := text[start:end]
		if ent.Entity != "" && got != ent.Entity {
			log.Printf("analyzer: ml engine span %d..%d (%q) does not match entity %q", ent.Start, ent.End, got, ent.Entity)
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
