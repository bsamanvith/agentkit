package toolsettest

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Spy is a testing.TB that records failures, skips and logs instead of
// acting on them, so tests can check that the kit fails a test, and how.
// [Spy.Run] runs the code under test on it.
type Spy struct {
	testing.TB // the real test: Context, TempDir, Helper, ...

	mu       sync.Mutex
	errs     []string
	logs     []string
	skip     string
	failed   bool
	cleanups []func()
}

// NewSpy returns a spy on t.
func NewSpy(t *testing.T) *Spy { return &Spy{TB: t} }

// Run runs fn on the spy in its own goroutine (Fatal and Skip end it there),
// then the cleanups fn registered, last first.
func (s *Spy) Run(fn func(tb testing.TB)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(s)
	}()
	<-done
	s.mu.Lock()
	cleanups := s.cleanups
	s.cleanups = nil
	s.mu.Unlock()
	for _, f := range slices.Backward(cleanups) {
		f()
	}
}

// Errs returns the failures recorded, Fatal ones included.
func (s *Spy) Errs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.errs)
}

// LogText returns everything logged.
func (s *Spy) LogText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.logs, "\n")
}

// SkipReason returns why the code skipped, "" if it did not.
func (s *Spy) SkipReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skip
}

// HasErr reports whether a recorded failure contains every one of parts.
func (s *Spy) HasErr(parts ...string) bool {
	for _, e := range s.Errs() {
		if containsAll(e, parts...) {
			return true
		}
	}
	return false
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func (s *Spy) record(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, msg)
	s.failed = true
}

func (s *Spy) Error(args ...any)                 { s.record(fmt.Sprintln(args...)) }
func (s *Spy) Errorf(format string, args ...any) { s.record(fmt.Sprintf(format, args...)) }
func (s *Spy) Fail()                             { s.mu.Lock(); s.failed = true; s.mu.Unlock() }
func (s *Spy) FailNow()                          { s.Fail(); runtime.Goexit() }
func (s *Spy) Fatal(args ...any)                 { s.Error(args...); runtime.Goexit() }
func (s *Spy) Fatalf(format string, args ...any) { s.Errorf(format, args...); runtime.Goexit() }
func (s *Spy) Log(args ...any)                   { s.Logf("%s", fmt.Sprintln(args...)) }
func (s *Spy) Skip(args ...any)                  { s.Skipf("%s", fmt.Sprintln(args...)) }
func (s *Spy) SkipNow()                          { s.Skipf("SkipNow") }
func (s *Spy) Cleanup(f func())                  { s.mu.Lock(); s.cleanups = append(s.cleanups, f); s.mu.Unlock() }

func (s *Spy) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func (s *Spy) Skipped() bool { return s.SkipReason() != "" }

func (s *Spy) Logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
}

func (s *Spy) Skipf(format string, args ...any) {
	s.mu.Lock()
	s.skip = fmt.Sprintf(format, args...)
	s.mu.Unlock()
	runtime.Goexit()
}
