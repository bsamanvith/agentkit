package storeopen_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/storeopen"
)

// TestOpen: the built-in specs open their backends, a program's own backend
// opens from its spec ahead of them, and a spec nothing opens is
// ErrUnknownSpec without the password it carried.
func TestOpen(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"", "memory", "sqlite:" + filepath.Join(t.TempDir(), "a.db")} {
		o, err := storeopen.Open(t.Context(), spec)
		if err != nil {
			t.Fatalf("Open(%q): %v", spec, err)
		}
		if o.Store == nil || o.Admin == nil {
			t.Errorf("Open(%q) = %+v, want a store and its Admin", spec, o)
		}
		if err := o.Close(); err != nil {
			t.Errorf("Open(%q): close: %v", spec, err)
		}
	}

	opened := 0
	own := storeopen.Backend{
		Name: "mine", Prefixes: []string{"mine://"},
		Open: func(_ context.Context, spec string, cfg storeopen.Config) (storeopen.Opened, error) {
			opened++
			if spec != "mine://x" || cfg.Clock == nil || !cfg.Migrate {
				t.Errorf("Open got %q %+v", spec, cfg)
			}
			s := memory.New()
			return storeopen.Opened{Store: s, Admin: memory.Admin(s), Close: func() error { return nil }}, nil
		},
	}
	if _, err := storeopen.Open(t.Context(), "mine://x", storeopen.WithBackends(own)); err != nil || opened != 1 {
		t.Errorf("own backend: %v, opened %d times", err, opened)
	}

	for _, spec := range []string{"mem", "sqlite:", "mysql://u:secret@host/db", "postgress://u:secret@host/db"} {
		_, err := storeopen.Open(t.Context(), spec)
		if !errors.Is(err, storeopen.ErrUnknownSpec) || strings.Contains(err.Error(), "secret") {
			t.Errorf("Open(%q) = %v, want ErrUnknownSpec without the password", spec, err)
		}
	}
	if err := storeopen.Migrate(t.Context(), "memory"); !errors.Is(err, storeopen.ErrNoMigrations) {
		t.Errorf("Migrate(memory) = %v, want ErrNoMigrations", err)
	}
}
