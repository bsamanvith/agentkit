// Package testkit holds the few test helpers shared across the module. Keep it
// small: add a helper only when a second package needs it.
// See docs/ENGINEERING.md §2.5.
package testkit

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// UpdateGoldenEnv is the environment variable that, set to 1, makes [Golden]
// rewrite golden files instead of comparing. It is an env var rather than a
// -update flag so that `make golden` (go test ./...) also works for packages
// that never call Golden.
const UpdateGoldenEnv = "AGENTKIT_UPDATE_GOLDEN"

// Golden compares got with the contents of the golden file at path. Run
// `make golden` to write got to the file instead; review the diff.
func Golden(tb testing.TB, path string, got []byte) {
	tb.Helper()
	golden(tb, path, got, os.Getenv(UpdateGoldenEnv) == "1")
}

func golden(tb testing.TB, path string, got []byte, update bool) {
	tb.Helper()
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("golden: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			tb.Fatalf("golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		tb.Fatalf("golden file %s does not exist; run `make golden` to create it", path)
	}
	if err != nil {
		tb.Fatalf("golden: %v", err)
	}
	if !bytes.Equal(want, got) {
		tb.Errorf("output differs from golden file %s (-want +got):\n%s\nrun `make golden` if the change is intended",
			path, cmp.Diff(string(want), string(got)))
	}
}

// RequireIntegrationEnv makes skipped integration tests fail instead (set in CI).
const RequireIntegrationEnv = "AGENTKIT_REQUIRE_INTEGRATION"

// IntegrationEnv returns the value of the environment variable key, which
// points an integration test at a real dependency (for example
// AGENTKIT_PG_URL). If it is unset the test is skipped, or fails when
// AGENTKIT_REQUIRE_INTEGRATION=1, so CI never silently skips.
func IntegrationEnv(tb testing.TB, key string) string {
	tb.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	if os.Getenv(RequireIntegrationEnv) == "1" {
		tb.Fatalf("%s is not set and %s=1", key, RequireIntegrationEnv)
	}
	tb.Skipf("%s is not set; make integration STORE=postgres starts them and runs these tests", key)
	return ""
}

// Eventually polls cond until it returns true, failing the test if that does
// not happen within timeout. It is the only sanctioned way to wait in tests.
func Eventually(tb testing.TB, timeout time.Duration, cond func() bool) {
	tb.Helper()
	const tick = 5 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("condition not met within %v", timeout)
		}
		time.Sleep(tick)
	}
}
