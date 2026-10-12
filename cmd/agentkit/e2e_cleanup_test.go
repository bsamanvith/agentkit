package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestE2EBinaryCleanup(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if os.Getenv("AGENTKIT_TEST_BINARY_CLEANUP_CHILD") == "1" {
		binary(t)
		return
	}

	for _, tc := range []struct {
		name     string
		exitCode int
	}{
		{name: "success", exitCode: 0},
		{name: "build failure", exitCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestE2EBinaryCleanup$", "-test.count=1")
			cmd.Env = append(os.Environ(), "AGENTKIT_TEST_BINARY_CLEANUP_CHILD=1", "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)
			if tc.exitCode != 0 {
				cmd.Env = append(cmd.Env, "PATH="+dir)
			}
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run test process: %v\n%s", err, out)
				}
				code = exitErr.ExitCode()
			}
			if code != tc.exitCode {
				t.Fatalf("exit %d, want %d\n%s", code, tc.exitCode, out)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("test process left temporary files: %v", entries)
			}
		})
	}
}
