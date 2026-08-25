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
			name:   "empty dictionary entry is ignored",
			custom: []string{""},
			in:     "nothing to mask",
			want:   "nothing to mask",
		},
		{
			name: "no matches",
			in:   "nothing to mask",
			want: "nothing to mask",
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
