// White-box test: it drives the unexported update switch of Golden directly
// instead of mutating the global -update flag.
package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGoldenMatch(t *testing.T) {
	t.Parallel()
	path := writeFile(t, "hello\n")
	r := run(func(tb testing.TB) { golden(tb, path, []byte("hello\n"), false) })
	if r.failed {
		t.Fatalf("matching output failed: %v", r.msgs)
	}
}

func TestGoldenMismatchShowsDiff(t *testing.T) {
	t.Parallel()
	path := writeFile(t, "line one\nline two\n")
	r := run(func(tb testing.TB) { golden(tb, path, []byte("line one\nline 2\n"), false) })
	if !r.failed || r.fatal {
		t.Fatalf("mismatch should fail without stopping the test: failed=%v fatal=%v", r.failed, r.fatal)
	}
	out := strings.Join(r.msgs, "\n")
	if !strings.Contains(out, "-") || !strings.Contains(out, "line 2") {
		t.Errorf("failure message has no diff:\n%s", out)
	}
}

func TestGoldenUpdateRewritesFile(t *testing.T) {
	t.Parallel()
	path := writeFile(t, "old\n")
	r := run(func(tb testing.TB) { golden(tb, path, []byte("new\n"), true) })
	if r.failed {
		t.Fatalf("update failed: %v", r.msgs)
	}
	if got := readFile(t, path); got != "new\n" {
		t.Errorf("file after update = %q, want %q", got, "new\n")
	}
}

func TestGoldenMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "missing.golden")

	r := run(func(tb testing.TB) { golden(tb, path, []byte("x"), false) })
	if !r.fatal {
		t.Fatal("missing golden file should stop the test")
	}

	r = run(func(tb testing.TB) { golden(tb, path, []byte("x"), true) })
	if r.failed {
		t.Fatalf("update should create the file: %v", r.msgs)
	}
	if got := readFile(t, path); got != "x" {
		t.Errorf("created file = %q, want %q", got, "x")
	}
}

// IntegrationEnv tests use t.Setenv, which cannot be combined with t.Parallel.
func TestIntegrationEnv(t *testing.T) {
	const key = "AGENTKIT_TESTKIT_PROBE"

	t.Run("set", func(t *testing.T) {
		t.Setenv(key, "postgres://x")
		var got string
		r := run(func(tb testing.TB) { got = IntegrationEnv(tb, key) })
		if r.failed || r.skipped || got != "postgres://x" {
			t.Errorf("got %q failed=%v skipped=%v", got, r.failed, r.skipped)
		}
	})
	t.Run("unset skips", func(t *testing.T) {
		t.Setenv(key, "")
		t.Setenv(RequireIntegrationEnv, "")
		r := run(func(tb testing.TB) { IntegrationEnv(tb, key) })
		if !r.skipped || r.failed {
			t.Errorf("skipped=%v failed=%v, want skipped only", r.skipped, r.failed)
		}
	})
	t.Run("unset fails when required", func(t *testing.T) {
		t.Setenv(key, "")
		t.Setenv(RequireIntegrationEnv, "1")
		r := run(func(tb testing.TB) { IntegrationEnv(tb, key) })
		if !r.fatal || r.skipped {
			t.Errorf("fatal=%v skipped=%v, want fatal only", r.fatal, r.skipped)
		}
	})
}

func TestEventually(t *testing.T) {
	t.Parallel()

	t.Run("returns once the condition holds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		r := run(func(tb testing.TB) {
			Eventually(tb, time.Minute, func() bool { calls++; return calls == 3 })
		})
		if r.failed || calls != 3 {
			t.Errorf("failed=%v calls=%d, want success after 3 calls", r.failed, calls)
		}
	})
	t.Run("fails after the timeout", func(t *testing.T) {
		t.Parallel()
		r := run(func(tb testing.TB) {
			Eventually(tb, 20*time.Millisecond, func() bool { return false })
		})
		if !r.fatal {
			t.Error("condition that never holds should fail the test")
		}
	})
}

// recorder is a testing.TB that records outcomes instead of reporting them.
type recorder struct {
	testing.TB
	failed, fatal, skipped bool
	msgs                   []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failed = true
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	r.fatal = true
	runtime.Goexit()
}

func (r *recorder) Skipf(format string, args ...any) {
	r.skipped = true
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
	runtime.Goexit()
}

// run calls fn on its own goroutine, as the testing package does, so Fatalf
// and Skipf can stop it with runtime.Goexit.
func run(fn func(tb testing.TB)) *recorder {
	r := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	return r
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.golden")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
