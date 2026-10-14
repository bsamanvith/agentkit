package agentapi

// WithGraceHook calls f with a run's ID when its reconnect grace starts:
// its timer is armed, so a test can advance the clock past it.
func WithGraceHook(f func(runID string)) Option { return func(c *config) { c.graceArmed = f } }
