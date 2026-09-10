package stats

import (
	"testing"
)

func TestRecordCountsAndKeepsPairs(t *testing.T) {
	s := New(2)
	s.Record("POST", "/v1/chat", map[string]string{"[EMAIL_1]": "a@x.com", "[IP_1]": "10.0.0.1"})
	s.Record("GET", "/v1/models", nil)

	got := s.Snapshot()
	if got.Requests != 2 {
		t.Errorf("requests = %d, want 2", got.Requests)
	}
	if got.Masked != 2 {
		t.Errorf("masked = %d, want 2", got.Masked)
	}
	if len(got.Recent) != 2 {
		t.Fatalf("recent = %d, want 2", len(got.Recent))
	}
	if got.Recent[0].Path != "/v1/chat" || got.Recent[0].Masked != 2 {
		t.Errorf("first event = %+v", got.Recent[0])
	}
	if got.Recent[0].Mapping[0].Token != "[EMAIL_1]" {
		t.Errorf("pairs not sorted by token: %+v", got.Recent[0].Mapping)
	}
	if got.Recent[1].Masked != 0 {
		t.Errorf("empty body should record zero masked, got %d", got.Recent[1].Masked)
	}
}

func TestRecentRingDropsOldest(t *testing.T) {
	s := New(2)
	s.Record("POST", "/a", nil)
	s.Record("POST", "/b", nil)
	s.Record("POST", "/c", nil)

	got := s.Snapshot()
	if len(got.Recent) != 2 {
		t.Fatalf("recent = %d, want 2", len(got.Recent))
	}
	if got.Recent[0].Path != "/b" || got.Recent[1].Path != "/c" {
		t.Errorf("ring = %s %s, want /b /c", got.Recent[0].Path, got.Recent[1].Path)
	}
	if got.Requests != 3 {
		t.Errorf("requests = %d, want 3", got.Requests)
	}
}
