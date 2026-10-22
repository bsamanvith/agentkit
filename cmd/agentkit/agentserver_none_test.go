//go:build noagent

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentNotBuilt: an image without the agent component (the runner and
// toolsets images) refuses it at startup and names the tag, whatever else
// is listed. Run with -tags noagent.
func TestAgentNotBuilt(t *testing.T) {
	t.Parallel()
	for _, components := range []string{"agent", "toolsets,agent", "agent,mcp-server"} {
		env := map[string]string{envComponents: components, envAddr: "127.0.0.1:0"}
		var stderr bytes.Buffer
		c := cli{getenv: func(k string) string { return env[k] }, stdout: io.Discard, stderr: &stderr}
		code := c.main(t.Context(), []string{"serve", "-config", writeNoAgentConfig(t)})
		const want = "AGENTKIT_COMPONENTS: this agentkit is built without the agent API (-tags noagent); use an image that has it"
		if code != 1 || !strings.Contains(stderr.String(), want) {
			t.Errorf("%s: serve exited %d: %s, want 1 and %q", components, code, stderr.String(), want)
		}
	}
}

// TestMigrateNotBuilt: so does agentkit migrate, which migrates the agent
// API's store.
func TestMigrateNotBuilt(t *testing.T) {
	t.Parallel()
	err := cli{getenv: func(string) string { return "postgres://db/agentkit" }}.migrate(t.Context(), nil)
	const want = "agentkit migrate: this agentkit is built without the agent API's store (-tags noagent); use an image that has it"
	if err == nil || err.Error() != want {
		t.Fatalf("migrate: %v, want %q", err, want)
	}
}

func writeNoAgentConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(`{"mcp": {"toolsets": [{"name": "demo", "type": "demo"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
