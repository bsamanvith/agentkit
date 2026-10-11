package agentapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// sseWriter writes a run's events as Server-Sent Events:
//
//	id: <seq>
//	event: <type>
//	data: <the event's JSON, one line>
//
// The first write that fails calls gone (which cancels the run) and turns
// every later write into a no-op. It is safe for concurrent use: the
// keep-alive writes from its own goroutine.
type sseWriter struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	rc     *http.ResponseController
	failed bool
	gone   func()
}

func newSSEWriter(w http.ResponseWriter, gone func()) *sseWriter {
	return &sseWriter{w: w, rc: http.NewResponseController(w), gone: gone}
}

// open sends the headers, so the client sees the stream before the first
// event.
func (s *sseWriter) open() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.WriteHeader(http.StatusOK)
	s.flushLocked()
}

func (s *sseWriter) event(e Event) {
	data, err := json.Marshal(e)
	if err != nil { // cannot happen: every field marshals
		data, _ = json.Marshal(Event{
			V: WireVersion, Type: TypeRunError, Seq: e.Seq, RunID: e.RunID, At: e.At,
			Error: &Error{Code: CodeInternal, Message: "encoding an event failed"},
		})
	}
	s.write(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, data))
}

func (s *sseWriter) write(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return
	}
	if err := s.rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.failLocked()
		return
	}
	if _, err := s.w.Write([]byte(msg)); err != nil {
		s.failLocked()
		return
	}
	s.flushLocked()
}

func (s *sseWriter) flushLocked() {
	if err := s.rc.Flush(); err != nil {
		s.failLocked()
	}
}

func (s *sseWriter) failLocked() {
	if !s.failed {
		s.failed = true
		s.gone()
	}
}

// keepAlive writes an SSE comment every d until the returned function is
// called; that function returns once the goroutine has stopped, so nothing is
// written after the handler returns.
func (s *sseWriter) keepAlive(clk clock.Clock, d time.Duration) func() {
	t := clk.NewTicker(d)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-t.C():
				s.write(": keep-alive\n\n")
			case <-stop:
				return
			}
		}
	}()
	return func() {
		t.Stop()
		close(stop)
		<-done
	}
}

// close clears the write deadline, so it does not outlive the response on a
// kept-alive connection.
func (s *sseWriter) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.rc.SetWriteDeadline(time.Time{})
}
