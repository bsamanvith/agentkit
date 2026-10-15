package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestImageVariants builds agentkit as the Makefile's image variants build it
// (TAGS_local, TAGS_dev): each links only its backends, and one it leaves
// out fails at startup, saying so.
func TestImageVariants(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds the binary per variant")
	}
	const (
		sqlite   = "modernc.org/sqlite"
		postgres = "github.com/jackc/pgx/v5"
		redis    = "github.com/redis/go-redis/v9"
	)
	config := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(config, []byte(`{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		name, tags  string
		links, lack []string
		fails       [][]string // env and args of a run that names a backend left out, and what it says
	}{
		{"local", "nopostgres noredis", []string{sqlite}, []string{postgres, redis}, [][]string{
			{envStore + "=postgres://localhost/x", "serve", "built without Postgres"},
			{envBus + "=redis://localhost:6379/0", "serve", "built without Redis"},
			{envStore + "=postgres://localhost/x", "migrate", "built without Postgres"},
		}},
		{"dev", "nosqlite", []string{postgres, redis}, []string{sqlite}, [][]string{
			{envStore + "=sqlite:/tmp/x.db", "serve", "built without SQLite"},
		}},
	} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			out, err := exec.Command("go", "list", "-deps", "-tags", v.tags, ".").Output()
			if err != nil {
				t.Fatal(err)
			}
			deps := strings.Fields(string(out))
			for _, p := range v.links {
				if !slices.Contains(deps, p) {
					t.Errorf("-tags %q does not link %s", v.tags, p)
				}
			}
			for _, p := range v.lack {
				if slices.Contains(deps, p) {
					t.Errorf("-tags %q links %s", v.tags, p)
				}
			}
			bin := filepath.Join(t.TempDir(), "agentkit")
			if out, err := exec.Command("go", "build", "-tags", v.tags, "-o", bin, ".").CombinedOutput(); err != nil {
				t.Fatalf("go build -tags %q: %v\n%s", v.tags, err, out)
			}
			for _, f := range v.fails {
				args := []string{f[1]}
				if f[1] == "serve" {
					args = append(args, "-config", config)
				}
				cmd := exec.Command(bin, args...)
				cmd.Env = []string{"HOME=" + t.TempDir(), envComponents + "=agent", envAddr + "=127.0.0.1:0", f[0]}
				out, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), f[2]) {
					t.Errorf("%s with %s: %v\n%s\nwant exit 1 saying %q", f[1], f[0], err, out, f[2])
				}
			}
		})
	}
}
