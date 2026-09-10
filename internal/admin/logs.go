package admin

import (
	"sync"
)

const defaultLogLines = 500

// LogSink is a ring of recent log lines plus live subscribers for SSE.
type LogSink struct {
	mu    sync.Mutex
	lines []string
	cap   int
	subs  map[chan string]struct{}
}

func NewLogSink(n int) *LogSink {
	if n <= 0 {
		n = defaultLogLines
	}
	return &LogSink{cap: n, subs: make(map[chan string]struct{})}
}

func (s *LogSink) Write(p []byte) (int, error) {
	line := string(p)
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if line == "" {
		return len(p), nil
	}

	s.mu.Lock()
	if len(s.lines) == s.cap {
		copy(s.lines, s.lines[1:])
		s.lines[s.cap-1] = line
	} else {
		s.lines = append(s.lines, line)
	}
	for ch := range s.subs {
		select {
		case ch <- line:
		default:
		}
	}
	s.mu.Unlock()
	return len(p), nil
}

func (s *LogSink) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.lines))
	copy(out, s.lines)
	return out
}

// Subscribe returns a channel of live lines and an unsubscribe func.
// The channel is buffered so a slow UI does not stall logging.
func (s *LogSink) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 32)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
		close(ch)
	}
}
