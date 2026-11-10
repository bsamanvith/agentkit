package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/members"
	"github.com/bsamanvith/agentkit/apps/farm/members/memberstest"
	"github.com/bsamanvith/agentkit/apps/farm/members/sqlite"
)

func TestStore(t *testing.T) {
	memberstest.RunStore(t, func(t *testing.T) members.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
