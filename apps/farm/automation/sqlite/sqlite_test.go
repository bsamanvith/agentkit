package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/automation/automationtest"
	"github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
)

func TestStore(t *testing.T) {
	t.Parallel()
	automationtest.RunStore(t, func(t *testing.T) automation.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
