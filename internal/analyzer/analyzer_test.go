package analyzer

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestAnonymize(t *testing.T) {
	tests := []struct {
		name   string
		custom []string
		in     string
		want   string
	}{
		{
			name: "email",
			in:   "write to alice@example.com today",
			want: "write to [EMAIL_1] today",
		},
		{
			name: "ipv4",
			in:   "host 192.168.1.10 is up",
			want: "host [IP_1] is up",
		},
		{
			name: "card plain",
			in:   "pay with 4111111111111111 now",
			want: "pay with [CARD_1] now",
		},
		{
			name: "card with spaces and dashes",
			in:   "4111 1111 1111 1111 and 5555-5555-5555-4444",
			want: "[CARD_1] and [CARD_2]",
		},
		{
			name:   "custom word",
			custom: []string{"Acme"},
			in:     "the Acme report",
			want:   "the [CUSTOM_1] report",
		},
		{
			name:   "custom word is case insensitive",
			custom: []string{"Acme"},
			in:     "the ACME report and acme too",
			want:   "the [CUSTOM_1] report and [CUSTOM_2] too",
		},
		{
			name:   "duplicate dictionary casing is one pattern",
			custom: []string{"Acme", "ACME"},
			in:     "Acme",
			want:   "[CUSTOM_1]",
		},
		{
			name: "bad luhn is left alone",
			in:   "pay with 4111111111111112 now",
			want: "pay with 4111111111111112 now",
		},
		{
			name: "impossible octets are left alone",
			in:   "host 999.999.999.999 is up",
			want: "host 999.999.999.999 is up",
		},
		{
			name: "distinct values get distinct numbers",
			in:   "a@x.com and b@x.com",
			want: "[EMAIL_1] and [EMAIL_2]",
		},
		{
			name: "repeated value reuses its token",
			in:   "a@x.com then a@x.com again",
			want: "[EMAIL_1] then [EMAIL_1] again",
		},
		{
			name:   "custom word inside an email does not split it",
			custom: []string{"example"},
			in:     "mail alice@example.com now",
			want:   "mail [EMAIL_1] now",
		},
		{
			name:   "custom word repeated outside and inside an email",
			custom: []string{"example"},
			in:     "example: alice@example.com",
			want:   "[CUSTOM_1]: [EMAIL_1]",
		},
		{
			name:   "several kinds at once",
			custom: []string{"Acme"},
			in:     "Acme, alice@example.com, 10.0.0.1, 4111111111111111",
			want:   "[CUSTOM_1], [EMAIL_1], [IP_1], [CARD_1]",
		},
		{
			name:   "custom word is a whole word, not a substring",
			custom: []string{"Inc"},
			in:     "Include Inc.",
			want:   "Include [CUSTOM_1].",
		},
		{
			name: "json number is not turned into a token",
			in:   `{"amount":4111111111111111,"note":"alice@example.com"}`,
			want: `{"amount":4111111111111111,"note":"[EMAIL_1]"}`,
		},
		{
			name: "bare json number is left alone",
			in:   `4111111111111111`,
			want: `4111111111111111`,
		},
		{
			name: "json strings share one token counter",
			in:   `{"a":"a@x.com","b":"b@x.com"}`,
			want: `{"a":"[EMAIL_1]","b":"[EMAIL_2]"}`,
		},
		{
			name: "no matches",
			in:   "nothing to mask",
			want: "nothing to mask",
		},
		{
			name: "password assignment",
			in:   "my password is hunter2",
			want: "my password is [PASSWORD_1]",
		},
		{
			name: "password colon strips trailing punct",
			in:   "password: hunter2.",
			want: "password: [PASSWORD_1].",
		},
		{
			name: "password quoted value",
			in:   `password="hunter2"`,
			want: `password="[PASSWORD_1]"`,
		},
		{
			name: "password policy is not an assignment",
			in:   "review the password policy",
			want: "review the password policy",
		},
		{
			name: "token is prose",
			in:   "the token is invalid",
			want: "the token is invalid",
		},
		{
			name: "password is prose",
			in:   "the password is required",
			want: "the password is required",
		},
		{
			name: "api key is prose",
			in:   "The API key is required",
			want: "The API key is required",
		},
		{
			name: "secret is prose",
			in:   "the secret is important",
			want: "the secret is important",
		},
		{
			name: "json password key",
			in:   `{"password":"hunter2"}`,
			want: `{"password":"[PASSWORD_1]"}`,
		},
		{
			name: "json content with password context",
			in:   `{"content":"password: hunter2"}`,
			want: `{"content":"password: [PASSWORD_1]"}`,
		},
		{
			name: "openai-like key",
			in:   "key sk-abcdefghijklmnopqrst",
			want: "key [SECRET_1]",
		},
		{
			name: "aws access key",
			in:   "id AKIAIOSFODNN7EXAMPLE",
			want: "id [SECRET_1]",
		},
		{
			name: "jwt",
			in:   "auth eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.sig",
			want: "auth [SECRET_1]",
		},
		{
			name: "pem",
			in:   "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----",
			want: "[SECRET_1]",
		},
		{
			name: "rsa pem",
			in:   "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----",
			want: "[SECRET_1]",
		},
		{
			name: "json pem one line",
			in:   `{"k":"-----BEGIN PRIVATE KEY-----abc-----END PRIVATE KEY-----"}`,
			want: `{"k":"[SECRET_1]"}`,
		},
		{
			name: "github pat",
			in:   "tok ghp_abcdefghijklmnopqrst",
			want: "tok [SECRET_1]",
		},
		{
			name: "google api key",
			in:   "k AIzaSyDaGmWKa4JsXZ-HjGw7ISLn",
			want: "k [SECRET_1]",
		},
		{
			name: "nested json password key",
			in:   `{"user":{"password":"hunter2"}}`,
			want: `{"user":{"password":"[PASSWORD_1]"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, mapping := New(tc.custom, nil).Anonymize(t.Context(), tc.in)
			if got != tc.want {
				t.Errorf("Anonymize(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			if restored := restore(got, mapping); restored != tc.in {
				t.Errorf("round trip\n got %q\nwant %q", restored, tc.in)
			}
		})
	}
}

// restore substitutes real values back in, which must reproduce the input.
func restore(masked string, mapping map[string]string) string {
	for token, value := range mapping {
		masked = strings.ReplaceAll(masked, token, value)
	}
	return masked
}

func TestAnonymizeMappingIsOnePerValue(t *testing.T) {
	_, mapping := New(nil, nil).Anonymize(t.Context(), "a@x.com then a@x.com again")

	if len(mapping) != 1 {
		t.Fatalf("mapping has %d entries, want 1: %v", len(mapping), mapping)
	}
	if got := mapping["[EMAIL_1]"]; got != "a@x.com" {
		t.Errorf("mapping[\"[EMAIL_1]\"] = %q, want %q", got, "a@x.com")
	}
}

// The engine is shared by concurrent requests, so Anonymize must not race.
// Run with -race for this to mean anything.
func TestAnonymizeConcurrent(t *testing.T) {
	e := New([]string{"Acme"}, nil)
	const want = "[CUSTOM_1] wrote to [EMAIL_1]"

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if got, _ := e.Anonymize(context.Background(), "Acme wrote to alice@example.com"); got != want {
					t.Errorf("Anonymize = %q, want %q", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestSecretKey(t *testing.T) {
	for _, k := range []string{"password", "Password", "api_key", "API-Key", "token"} {
		if !secretKey(k) {
			t.Errorf("secretKey(%q) = false", k)
		}
	}
	for _, k := range []string{"content", "messages", "model", "note"} {
		if secretKey(k) {
			t.Errorf("secretKey(%q) = true", k)
		}
	}
}

func TestOverlapIndex(t *testing.T) {
	idx := newOverlapIndex([]span{{start: 0, end: 50, kind: "EMAIL"}, {start: 80, end: 90, kind: "IP"}})

	if !idx.overlaps(span{start: 40, end: 60}) {
		t.Error("guess overlapping the long early span should lose")
	}
	if idx.overlaps(span{start: 50, end: 60}) {
		t.Error("touching at the boundary is not an overlap")
	}
	if !idx.overlaps(span{start: 85, end: 88}) {
		t.Error("guess nested in the later span should lose")
	}
	if idx.overlaps(span{start: 60, end: 70}) {
		t.Error("gap between trusted spans should be free for ML")
	}
}
