package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	"github.com/bsamanvith/agentkit/apps/farm/notify/sqlite"
)

func TestStore(t *testing.T) {
	notifytest.RunStore(t, func(t *testing.T) notify.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
