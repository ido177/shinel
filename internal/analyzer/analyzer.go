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
	"unicode"
	"unicode/utf8"

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
// Matching is case-insensitive: "John" in the dictionary also hits "john".
func New(customWords []string, ml *MLEngineClient) *AnalyzerEngine {
	seen := make(map[string]struct{}, len(customWords))
	dict := make([]string, 0, len(customWords))
	for _, w := range customWords {
		folded := foldString(w)
		if folded == "" {
			continue
		}
		if _, ok := seen[folded]; ok {
			continue
		}
		seen[folded] = struct{}{}
		dict = append(dict, folded)
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
// JSON bodies are walked as a tree: only string values are masked, so a
// Luhn-valid number in `"amount": 4111…` stays a number. Anything that is not
// a single JSON value is treated as opaque text.
//
// The regex and dictionary detectors run first, then the ML sidecar, which only
// gets to claim text the deterministic layer left alone. An unreachable sidecar
// is logged and skipped rather than failing the call: names the model would
// have caught can then leave the process. That is intentional so a down
// sidecar cannot take the proxy with it.
func (e *AnalyzerEngine) Anonymize(ctx context.Context, text string) (string, map[string]string) {
	if v, ok := parseJSON(text); ok {
		return e.anonymizeJSON(ctx, v)
	}
	return e.anonymizeText(ctx, text, newSession())
}

type session struct {
	mapping  map[string]string
	tokenOf  map[string]string
	counters map[string]int
}

func newSession() *session {
	return &session{
		mapping:  make(map[string]string),
		tokenOf:  make(map[string]string),
		counters: make(map[string]int),
	}
}

func (e *AnalyzerEngine) anonymizeText(ctx context.Context, text string, sess *session) (string, map[string]string) {
	spans := resolveConflicts(e.collect(text), e.collectML(ctx, text))
	sortSpans(spans)

	var b strings.Builder
	last := 0
	for _, s := range spans {
		if s.start < last {
			continue
		}
		value := text[s.start:s.end]
		token, ok := sess.tokenOf[value]
		if !ok {
			sess.counters[s.kind]++
			token = fmt.Sprintf("[%s_%d]", s.kind, sess.counters[s.kind])
			sess.tokenOf[value] = token
			sess.mapping[token] = value
		}
		b.WriteString(text[last:s.start])
		b.WriteString(token)
		last = s.end
	}
	b.WriteString(text[last:])
	return b.String(), sess.mapping
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
// then locates their occurrences. Matching runs on a case-folded copy of the
// text; origOf maps each folded byte back to the original so the masked slice
// keeps the source spelling.
func (e *AnalyzerEngine) collectCustom(text string) []span {
	if e.matcher == nil {
		return nil
	}
	folded, origOf := foldForMatch(text)
	var spans []span
	for _, i := range e.matcher.MatchThreadSafe([]byte(folded)) {
		word := e.dict[i]
		for off := 0; ; {
			j := strings.Index(folded[off:], word)
			if j < 0 {
				break
			}
			start := off + j
			end := start + len(word)
			origStart, origEnd := origOf[start], origOf[end]
			if wordBounded(text, origStart, origEnd) {
				spans = append(spans, span{origStart, origEnd, "CUSTOM"})
			}
			off = end
		}
	}
	return spans
}

// foldForMatch lowercases s rune by rune and records, for every byte of the
// folded string plus a sentinel at the end, the corresponding original offset.
func foldForMatch(s string) (string, []int) {
	var b strings.Builder
	b.Grow(len(s))
	origOf := make([]int, 0, len(s)+1)
	for i, r := range s {
		low := string(unicode.ToLower(r))
		for range len(low) {
			origOf = append(origOf, i)
		}
		b.WriteString(low)
	}
	origOf = append(origOf, len(s))
	return b.String(), origOf
}

func foldString(s string) string {
	folded, _ := foldForMatch(s)
	return folded
}

func wordBounded(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if isWordChar(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if isWordChar(r) {
			return false
		}
	}
	return true
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
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
