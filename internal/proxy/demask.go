package proxy

import (
	"bytes"
	"io"
	"net/http"

	"github.com/ido177/shinel/internal/vault"
)

// ModifyResponse only ever sees an *http.Response, while the token logic lives
// in StreamingResponseWriter. bufferSink is the adapter between the two: a
// minimal http.ResponseWriter that collects output in memory, so a response
// body can be pushed through the same tested sliding window a real client
// connection would use.
type bufferSink struct {
	buf    bytes.Buffer
	header http.Header
}

func (b *bufferSink) Header() http.Header {
	if b.header == nil {
		b.header = make(http.Header)
	}
	return b.header
}

func (b *bufferSink) Write(p []byte) (int, error) { return b.buf.Write(p) }

func (b *bufferSink) WriteHeader(int) {}

// demask restores a complete body in one shot.
func demask(body []byte, v vault.Vault, reqID string) []byte {
	sink := &bufferSink{}
	s := NewStreamingResponseWriter(sink, v, reqID)
	if _, err := s.Write(body); err != nil {
		return body // bufferSink never fails, so this is unreachable in practice
	}
	if err := s.Close(); err != nil {
		return body
	}
	return sink.buf.Bytes()
}

// demaskReader restores a body while it streams. Each Read pulls from upstream
// only as far as needed to produce output, so events reach the client as they
// arrive rather than at the end of the response.
type demaskReader struct {
	src   io.ReadCloser
	sink  *bufferSink
	w     *StreamingResponseWriter
	chunk []byte
	drain bool // upstream is exhausted; only the sink is left to serve
}

func newDemaskReader(src io.ReadCloser, v vault.Vault, reqID string) *demaskReader {
	sink := &bufferSink{}
	return &demaskReader{
		src:   src,
		sink:  sink,
		w:     NewStreamingResponseWriter(sink, v, reqID),
		chunk: make([]byte, 4096),
	}
}

func (d *demaskReader) Read(p []byte) (int, error) {
	// A chunk that is entirely a partial token produces no output, so keep
	// pulling until there is something to hand back or upstream is done.
	for d.sink.buf.Len() == 0 {
		if d.drain {
			return 0, io.EOF
		}

		n, err := d.src.Read(d.chunk)
		if n > 0 {
			if _, werr := d.w.Write(d.chunk[:n]); werr != nil {
				return 0, werr
			}
		}
		if err != nil {
			d.drain = true
			// Release a token candidate the stream ended in the middle of.
			if cerr := d.w.Close(); cerr != nil {
				return 0, cerr
			}
			if err != io.EOF {
				return 0, err
			}
		}
	}
	return d.sink.buf.Read(p)
}

func (d *demaskReader) Close() error { return d.src.Close() }
