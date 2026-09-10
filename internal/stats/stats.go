// Package stats keeps a process-local tally of proxied requests and the
// values that were masked on the way out.
package stats

import (
	"sort"
	"sync"
	"time"
)

const defaultRecent = 100

// Pair is one real value and the token that replaced it.
type Pair struct {
	Value string `json:"value"`
	Token string `json:"token"`
}

// Event is one proxied request.
type Event struct {
	Time    time.Time `json:"time"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Masked  int       `json:"masked"`
	Mapping []Pair    `json:"mapping"`
}

// Snapshot is a consistent copy of counters plus the recent ring.
type Snapshot struct {
	Requests int     `json:"requests"`
	Masked   int     `json:"masked"`
	Recent   []Event `json:"recent"`
}

// Store is safe for the proxy hot path and the admin UI.
type Store struct {
	mu       sync.Mutex
	requests int
	masked   int
	recent   []Event
	cap      int
}

func New(recent int) *Store {
	if recent <= 0 {
		recent = defaultRecent
	}
	return &Store{cap: recent}
}

// Record notes one outbound request. mapping is token → real value, matching
// the analyzer; a nil mapping still counts as a request with nothing masked.
func (s *Store) Record(method, path string, mapping map[string]string) {
	if s == nil {
		return
	}
	ev := Event{
		Time:   time.Now().UTC(),
		Method: method,
		Path:   path,
		Masked: len(mapping),
	}
	if len(mapping) > 0 {
		ev.Mapping = make([]Pair, 0, len(mapping))
		for token, value := range mapping {
			ev.Mapping = append(ev.Mapping, Pair{Value: value, Token: token})
		}
		sort.Slice(ev.Mapping, func(i, j int) bool { return ev.Mapping[i].Token < ev.Mapping[j].Token })
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	s.masked += ev.Masked
	if len(s.recent) == s.cap {
		copy(s.recent, s.recent[1:])
		s.recent[s.cap-1] = ev
		return
	}
	s.recent = append(s.recent, ev)
}

func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		Requests: s.requests,
		Masked:   s.masked,
		Recent:   make([]Event, len(s.recent)),
	}
	copy(out.Recent, s.recent)
	for i := range out.Recent {
		if len(out.Recent[i].Mapping) == 0 {
			continue
		}
		pairs := make([]Pair, len(out.Recent[i].Mapping))
		copy(pairs, out.Recent[i].Mapping)
		out.Recent[i].Mapping = pairs
	}
	return out
}
