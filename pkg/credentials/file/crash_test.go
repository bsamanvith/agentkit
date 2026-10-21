//go:build unix

package file_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials/file"
)

// The crash tests run a child process (this test binary, TestCrashChild)
// that opens the file, starts a write and kills itself with SIGKILL at a
// point in the write: no deferred code, no flush, as when the machine loses
// the process.
const (
	envCrashPath  = "CREDFILE_CRASH_PATH"
	envCrashPoint = "CREDFILE_CRASH_POINT"
)

// TestCrashChild is the child's side; it does nothing in a normal run.
func TestCrashChild(t *testing.T) {
	path, point := os.Getenv(envCrashPath), os.Getenv(envCrashPoint)
	if path == "" {
		t.Skip("run by TestCrashSafety")
	}
	file.SetCrashHook(func(p string) {
		if p == point {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // the signal arrives
		}
	})
	s, err := file.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := ctx(t, "a")
	b, err := s.Get(c, "", "crm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(c, "", "crm", blob("new"), b.Version); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("the write finished: no crash point %q", point)
}

// TestCrashSafety: a process killed in the middle of writing the file, before
// the rename or after it, leaves the old state or the new one, which the
// next Open reads.
//
// Not parallel: a fork holds the parent's descriptors, the lock's among them,
// until it execs, so another test's reopen could see the file locked.
func TestCrashSafety(t *testing.T) {
	for _, tc := range []struct {
		point string
		want  string // the crm credential after the crash
	}{
		{"mid-write", "old"},
		{"before-rename", "old"},
		{"after-rename", "new"},
	} {
		t.Run(tc.point, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			s := open(t, path)
			c := ctx(t, "a")
			if _, err := s.Put(c, "", "crm", blob("old"), 0); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)

			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), envCrashPath+"="+path, envCrashPoint+"="+tc.point)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("child: %v, want killed at %s; output:\n%s", err, tc.point, out)
			}

			s = open(t, path) // the lock died with the child
			b, err := s.Get(c, "", "crm")
			if err != nil || string(b.Data) != tc.want {
				t.Fatalf("after a crash at %s, crm = %q, %v; want %q", tc.point, b.Data, err, tc.want)
			}
			if after, _ := os.ReadFile(path); tc.want == "old" && string(after) != string(before) {
				t.Errorf("after a crash at %s the file changed:\n%s", tc.point, after)
			}
			if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the temp file is left after reopening: %v", err)
			}
			checkMode(t, path)
			if m, err := s.Put(c, "", "crm", blob("after"), b.Version); err != nil || m.Version != b.Version+1 {
				t.Errorf("a write after the crash = %+v, %v", m, err)
			}
			if strings.Contains(string(out), "PASS") {
				t.Errorf("the child finished:\n%s", out)
			}
		})
	}
}
