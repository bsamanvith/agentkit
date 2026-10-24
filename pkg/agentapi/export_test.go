package agentapi

// WithGraceHook calls f with a run's ID when its reconnect grace starts:
// its timer is armed, so a test can advance the clock past it.
func WithGraceHook(f func(runID string)) Option { return func(c *config) { c.graceArmed = f } }

// WithWatchBuffer sets how many events a viewer of a shared watch may be
// behind before it is dropped to the log (T103); 0 drops a viewer that is
// not waiting for the next event.
func WithWatchBuffer(n int) Option { return func(c *config) { c.watchBuffer = n } }

// Watching returns how many shared watches the server runs, and how many
// viewers they have (T103).
func (s *Server) Watching() (watches, viewers int) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	for _, w := range s.watches {
		w.mu.Lock()
		if !w.done {
			viewers += w.members
		}
		w.mu.Unlock()
	}
	return len(s.watches), viewers
}

// MaxCallerNotificationStreams is how many GET /v1/events streams one
// caller may have open on a server.
const MaxCallerNotificationStreams = maxCallerNotificationStreams

// NotificationStreams returns how many GET /v1/events streams the server
// serves.
func (s *Server) NotificationStreams() int {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	return s.nOpen
}

// Retains reports whether the server still keeps the run's events in memory
// (its hub), rather than replaying them from the store. A run's client hears
// run_done just before the server files the run as finished, which is when
// older runs are let go.
func (s *Server) Retains(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.hubs {
		if k.runID == runID {
			return true
		}
	}
	return false
}
