package analyzer

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// tokenGrammar is what internal/proxy/streaming.go accepts when it de-masks. A
// token that does not match here is a value the client never gets back, so
// every token the engine emits is checked against it.
var tokenGrammar = regexp.MustCompile(`^\[[A-Z]+_[0-9]+\]$`)

// mlServer answers /analyze with the given entities and records the request.
func mlServer(t *testing.T, entities []Entity) (*httptest.Server, *analyzeRequest) {
	t.Helper()
	var got analyzeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("sidecar got bad request %q: %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(entities); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func mlEngine(t *testing.T, customWords []string, entities []Entity) *AnalyzerEngine {
	t.Helper()
	srv, _ := mlServer(t, entities)
	return New(customWords, NewMLEngineClient(srv.URL, []string{"PERSON"}, 2*time.Second))
}

func checkTokens(t *testing.T, mapping map[string]string) {
	t.Helper()
	for token := range mapping {
		if !tokenGrammar.MatchString(token) {
			t.Errorf("token %q cannot be de-masked: it does not match %s", token, tokenGrammar)
		}
	}
}

// The sidecar reports offsets in characters, Go slices in bytes. For Cyrillic
// those differ by a factor of two, so using them raw would cut into a rune.
func TestAnonymizeConvertsCharacterOffsets(t *testing.T) {
	const in = "Иван ушёл домой"
	// "Иван" is characters 0..4 but bytes 0..8.
	e := mlEngine(t, nil, []Entity{{Entity: "Иван", Label: "PERSON", Start: 0, End: 4}})

	got, mapping := e.Anonymize(t.Context(), in)

	if want := "[PERSON_1] ушёл домой"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if value := mapping["[PERSON_1]"]; value != "Иван" {
		t.Errorf("mapping[[PERSON_1]] = %q, want %q", value, "Иван")
	}
	if restored := restore(got, mapping); restored != in {
		t.Errorf("round trip\n got %q\nwant %q", restored, in)
	}
	checkTokens(t, mapping)
}

func TestAnonymizeConvertsOffsetsMidText(t *testing.T) {
	const in = "Привет, Иван!"
	// "Иван" starts at character 8 and byte 14.
	e := mlEngine(t, nil, []Entity{{Entity: "Иван", Label: "PERSON", Start: 8, End: 12}})

	got, mapping := e.Anonymize(t.Context(), in)

	if want := "Привет, [PERSON_1]!"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if restored := restore(got, mapping); restored != in {
		t.Errorf("round trip\n got %q\nwant %q", restored, in)
	}
}

// A label with an underscore would produce [SECRET_PROJECT_1], which the
// de-masking grammar rejects outright.
func TestAnonymizeSanitizesLabels(t *testing.T) {
	const in = "run Bluebird now"
	e := mlEngine(t, nil, []Entity{{Entity: "Bluebird", Label: "SECRET_PROJECT", Start: 4, End: 12}})

	got, mapping := e.Anonymize(t.Context(), in)

	if want := "run [SECRETPROJECT_1] now"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	checkTokens(t, mapping)
	if restored := restore(got, mapping); restored != in {
		t.Errorf("round trip\n got %q\nwant %q", restored, in)
	}
}

func TestSanitizeLabel(t *testing.T) {
	tests := map[string]string{
		"PERSON":         "PERSON",
		"person":         "PERSON",
		"SECRET_PROJECT": "SECRETPROJECT",
		"e-mail":         "EMAIL",
		"Org 2":          "ORG",
		"___":            "",
		"":               "",
	}
	for in, want := range tests {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Regex wins every overlap, including one where the model's span starts
// earlier and runs longer, which is exactly the case leftmost-longest would
// otherwise decide the other way.
func TestAnonymizeGivesRegexPriorityOverML(t *testing.T) {
	const in = "Ivan Petrov ivan@example.com wrote"
	e := mlEngine(t, nil, []Entity{
		{Entity: "Ivan Petrov ivan@example.com", Label: "PERSON", Start: 0, End: 28},
	})

	got, mapping := e.Anonymize(t.Context(), in)

	if want := "Ivan Petrov [EMAIL_1] wrote"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if _, ok := mapping["[PERSON_1]"]; ok {
		t.Error("the ML span overlapping the email should have been dropped")
	}
	if restored := restore(got, mapping); restored != in {
		t.Errorf("round trip\n got %q\nwant %q", restored, in)
	}
}

func TestAnonymizeGivesCustomWordsPriorityOverML(t *testing.T) {
	const in = "Acme Corp ships"
	e := mlEngine(t, []string{"Acme"}, []Entity{
		{Entity: "Acme Corp", Label: "ORG", Start: 0, End: 9},
	})

	got, _ := e.Anonymize(t.Context(), in)

	if want := "[CUSTOM_1] Corp ships"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

// An ML span that touches nothing the deterministic layer claimed is kept.
func TestAnonymizeKeepsNonOverlappingMLSpans(t *testing.T) {
	const in = "Ivan wrote to a@x.com"
	e := mlEngine(t, nil, []Entity{{Entity: "Ivan", Label: "PERSON", Start: 0, End: 4}})

	got, mapping := e.Anonymize(t.Context(), in)

	if want := "[PERSON_1] wrote to [EMAIL_1]"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if restored := restore(got, mapping); restored != in {
		t.Errorf("round trip\n got %q\nwant %q", restored, in)
	}
}

// Coordinates arrive from another process, so nonsense must be dropped rather
// than panic on a slice.
func TestAnonymizeRejectsOutOfRangeSpans(t *testing.T) {
	const in = "Ivan wrote"
	bad := []Entity{
		{Entity: "x", Label: "PERSON", Start: -1, End: 4},
		{Entity: "x", Label: "PERSON", Start: 4, End: 4},
		{Entity: "x", Label: "PERSON", Start: 6, End: 2},
		{Entity: "x", Label: "PERSON", Start: 0, End: 999},
		{Entity: "x", Label: "PERSON", Start: 999, End: 1000},
		{Entity: "x", Label: "!!!", Start: 0, End: 4},
		{Entity: "wrong", Label: "PERSON", Start: 0, End: 4},
	}

	for _, entity := range bad {
		e := mlEngine(t, nil, []Entity{entity})
		got, mapping := e.Anonymize(t.Context(), in)
		if got != in {
			t.Errorf("entity %+v changed the text to %q", entity, got)
		}
		if len(mapping) != 0 {
			t.Errorf("entity %+v produced %v", entity, mapping)
		}
	}
}

func TestAnonymizeSendsConfiguredLabels(t *testing.T) {
	srv, got := mlServer(t, nil)
	labels := []string{"PERSON", "ORG"}
	e := New(nil, NewMLEngineClient(srv.URL, labels, time.Second))

	e.Anonymize(t.Context(), "some text")

	if got.Text != "some text" {
		t.Errorf("sidecar got text %q", got.Text)
	}
	if strings.Join(got.Labels, ",") != strings.Join(labels, ",") {
		t.Errorf("sidecar got labels %v, want %v", got.Labels, labels)
	}
}

// Every way the sidecar can let us down still has to leave regex masking intact.
func TestAnonymizeSurvivesSidecarFailures(t *testing.T) {
	const in = "Ivan wrote to a@x.com"
	const want = "Ivan wrote to [EMAIL_1]"

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing listens there any more

	broken, _ := mlServer(t, nil)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	t.Cleanup(slow.Close)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json at all")
	}))
	t.Cleanup(garbage.Close)

	clients := map[string]*MLEngineClient{
		"unreachable":  NewMLEngineClient(deadURL, []string{"PERSON"}, time.Second),
		"bad url":      NewMLEngineClient("://nope", []string{"PERSON"}, time.Second),
		"timeout":      NewMLEngineClient(slow.URL, []string{"PERSON"}, 50*time.Millisecond),
		"server error": NewMLEngineClient(failing.URL, []string{"PERSON"}, time.Second),
		"garbage body": NewMLEngineClient(garbage.URL, []string{"PERSON"}, time.Second),
		"no labels":    NewMLEngineClient(broken.URL, nil, time.Second),
	}

	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			got, mapping := New(nil, client).Anonymize(t.Context(), in)
			if got != want {
				t.Errorf("\n got %q\nwant %q", got, want)
			}
			checkTokens(t, mapping)
		})
	}
}

func TestAnonymizeWithoutMLIsUnchanged(t *testing.T) {
	const in = "Ivan wrote to a@x.com"

	got, _ := New(nil, nil).Anonymize(t.Context(), in)

	if want := "Ivan wrote to [EMAIL_1]"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

// The timeout must bound the call rather than the whole proxy waiting on it.
func TestMLEngineClientTimesOut(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(slow.Close)
	c := NewMLEngineClient(slow.URL, []string{"PERSON"}, 50*time.Millisecond)

	start := time.Now()
	_, err := c.Analyze(t.Context(), "text")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want a timeout error, got nil")
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("waited %v, want the call bounded near 50ms", elapsed)
	}
}

func TestMLEngineClientParsesTheContract(t *testing.T) {
	// Byte-for-byte the payload ml_engine/test_main.py asserts on.
	const payload = `[{"entity": "Иван", "label": "PERSON", "start": 0, "end": 4}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	got, err := NewMLEngineClient(srv.URL, []string{"PERSON"}, time.Second).Analyze(t.Context(), "Иван")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	want := []Entity{{Entity: "Иван", Label: "PERSON", Start: 0, End: 4}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("\n got %+v\nwant %+v", got, want)
	}
}
