// Package analyzer replaces sensitive values in a text with placeholder
// tokens, handing back the mapping needed to restore them later.
package analyzer

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/cloudflare/ahocorasick"
)

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	ipRe    = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	// 13 to 19 digits, optionally grouped by a single space or dash. Dots are
	// not separators here, so an IPv4 address can never look like a card.
	cardRe = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)

	cardSeparators = strings.NewReplacer(" ", "", "-", "")
)

// AnalyzerEngine masks sensitive values in a text. It is safe for concurrent
// use: Anonymize keeps no state between calls.
type AnalyzerEngine struct {
	matcher *ahocorasick.Matcher
	dict    []string
	ml      *MLEngineClient
}

// New builds an engine that also masks every word in customWords. A nil ml
// turns off the model layer, leaving the deterministic detectors on their own.
//
// ponytail: custom words are matched byte-exactly, so "John" does not match
// "john". Upgrade path: fold case into a normalized copy of the text and keep
// an offset map back to the original, or swap in a case-insensitive matcher.
func New(customWords []string, ml *MLEngineClient) *AnalyzerEngine {
	dict := make([]string, 0, len(customWords))
	for _, w := range customWords {
		// An empty pattern would spin forever in the strings.Index loop below.
		if w != "" {
			dict = append(dict, w)
		}
	}
	e := &AnalyzerEngine{dict: dict, ml: ml}
	if len(dict) > 0 {
		e.matcher = ahocorasick.NewStringMatcher(dict)
	}
	return e
}

// span is a half-open byte range [start, end) of text holding one value.
type span struct {
	start, end int
	kind       string
}

// Anonymize returns text with every detected value replaced by a token, plus a
// map from token to the real value it stands for. Repeats of the same value
// share one token.
//
// The regex and dictionary detectors run first, then the ML sidecar, which only
// gets to claim text the deterministic layer left alone. An unreachable sidecar
// is logged and skipped rather than failing the call.
func (e *AnalyzerEngine) Anonymize(ctx context.Context, text string) (string, map[string]string) {
	spans := resolveConflicts(e.collect(text), e.collectML(ctx, text))
	sortSpans(spans)

	var b strings.Builder
	mapping := make(map[string]string)
	tokenOf := make(map[string]string)
	counters := make(map[string]int)
	last := 0

	for _, s := range spans {
		if s.start < last {
			continue // overlaps a span we already took
		}
		value := text[s.start:s.end]
		token, ok := tokenOf[value]
		if !ok {
			counters[s.kind]++
			token = fmt.Sprintf("[%s_%d]", s.kind, counters[s.kind])
			tokenOf[value] = token
			mapping[token] = value
		}
		b.WriteString(text[last:s.start])
		b.WriteString(token)
		last = s.end
	}
	b.WriteString(text[last:])

	return b.String(), mapping
}

func (e *AnalyzerEngine) collect(text string) []span {
	var spans []span

	for _, m := range emailRe.FindAllStringIndex(text, -1) {
		spans = append(spans, span{m[0], m[1], "EMAIL"})
	}
	for _, m := range ipRe.FindAllStringIndex(text, -1) {
		if net.ParseIP(text[m[0]:m[1]]) != nil {
			spans = append(spans, span{m[0], m[1], "IP"})
		}
	}
	for _, m := range cardRe.FindAllStringIndex(text, -1) {
		if luhn(cardSeparators.Replace(text[m[0]:m[1]])) {
			spans = append(spans, span{m[0], m[1], "CARD"})
		}
	}
	spans = append(spans, e.collectCustom(text)...)

	return spans
}

// collectCustom uses Aho-Corasick to learn which dictionary words occur at all,
// then locates their occurrences. The matcher reports each word once and
// without offsets, so the positions come from a scan per present word.
func (e *AnalyzerEngine) collectCustom(text string) []span {
	if e.matcher == nil {
		return nil
	}
	var spans []span
	for _, i := range e.matcher.MatchThreadSafe([]byte(text)) {
		word := e.dict[i]
		for off := 0; ; {
			j := strings.Index(text[off:], word)
			if j < 0 {
				break
			}
			start := off + j
			spans = append(spans, span{start, start + len(word), "CUSTOM"})
			off = start + len(word)
		}
	}
	return spans
}

// sortSpans orders spans leftmost-longest, so that the sweep in Anonymize keeps
// the outermost match and drops the ones nested in it.
func sortSpans(spans []span) {
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
}
