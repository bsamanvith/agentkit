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
