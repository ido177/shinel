package analyzer

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	kindPassword = "PASSWORD"
	kindSecret   = "SECRET"
)

var (
	// Marker then : or =, then the value. The span is the value only.
	passwordAssignRe = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|passphrase|secret|api[_-]?key|token)\s*[:=]\s*(?:"([^"]+)"|'([^']+)'|(\S+))`)
	// "is" only after password-like markers; unquoted English words are skipped
	// so "the token is invalid" is not treated as a credential.
	passwordIsRe = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|passphrase)\s+is\s+(?:"([^"]+)"|'([^']+)'|(\S+))`)

	skRe        = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)
	awsKeyRe    = regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`)
	googleKeyRe = regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{20,}`)
	githubPATRe = regexp.MustCompile(`\b(?:ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)
	jwtRe       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	pemRe       = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
)

func secretKey(k string) bool {
	switch strings.ToLower(strings.ReplaceAll(k, "-", "_")) {
	case "password", "passwd", "pwd", "passphrase", "secret", "api_key", "apikey", "token":
		return true
	default:
		return false
	}
}

func collectSecrets(text string) []span {
	spans := collectPasswordContext(text)
	for _, re := range []*regexp.Regexp{skRe, awsKeyRe, googleKeyRe, githubPATRe, jwtRe, pemRe} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			spans = append(spans, span{m[0], m[1], kindSecret})
		}
	}
	return spans
}

func collectPasswordContext(text string) []span {
	spans := contextSpans(text, passwordAssignRe, false)
	return append(spans, contextSpans(text, passwordIsRe, true)...)
}

func contextSpans(text string, re *regexp.Regexp, skipProse bool) []span {
	var spans []span
	for _, idx := range re.FindAllStringSubmatchIndex(text, -1) {
		start, end, quoted := valueRange(idx)
		if start < 0 || end <= start {
			continue
		}
		if !quoted {
			trimmed := strings.TrimRight(text[start:end], ".,;")
			end = start + len(trimmed)
			if end <= start {
				continue
			}
			if skipProse && looksLikeProse(text[start:end]) {
				continue
			}
		}
		spans = append(spans, span{start, end, kindPassword})
	}
	return spans
}

// looksLikeProse reports a bare word with no digits or symbols: "required",
// "invalid". "hunter2" and "p@ss" are left for the password path.
func looksLikeProse(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if unicode.IsLetter(r) || r == '-' || r == '\'' {
			continue
		}
		return false
	}
	return true
}

// valueRange picks the captured value (double-quoted, single-quoted, or bare).
func valueRange(idx []int) (start, end int, quoted bool) {
	for i, q := range []bool{true, true, false} {
		a, b := 2+2*i, 3+2*i
		if b < len(idx) && idx[a] >= 0 {
			return idx[a], idx[b], q
		}
	}
	return -1, -1, false
}
