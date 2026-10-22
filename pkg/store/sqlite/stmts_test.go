package sqlite_test

import (
	"fmt"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// A store keeps its statements prepared (T056). When another handle on the
// file changes the schema under them, as a newer binary migrating the file
// would, the kept statements still read and write correctly: the reads and
// writes before the change and after it agree with each other.
func TestKeptStatementsSurviveASchemaChange(t *testing.T) {
	t.Parallel()
	path := migrated(t)
	s := openPath(t, path, sqlite.WithClock(clock.Real()))
	other := openPath(t, path, sqlite.WithClock(clock.Real()))
	ctx := tenantCtx(t, "acme")

	use := func(round int) {
		t.Helper()
		run := fmt.Sprintf("r%d", round)
		if _, err := s.Create(ctx, run, fmt.Sprintf("c%d", round)); err != nil {
			t.Fatalf("round %d: Create: %v", round, err)
		}
		if err := s.Append(ctx, "c", prompt(int64(round+1), run)); err != nil {
			t.Fatalf("round %d: Append: %v", round, err)
		}
		got, err := s.Get(ctx, run)
		if err != nil || got.ID != run {
			t.Fatalf("round %d: Get = %+v, %v", round, got, err)
		}
		page, err := s.History(ctx, "c", store.Page{})
		if err != nil || len(page.Entries) != round+1 {
			t.Fatalf("round %d: History = %d entries, %v; want %d", round, len(page.Entries), err, round+1)
		}
	}
	use(0) // every statement above is kept from here on
	use(1)
	for i, ddl := range []string{
		`CREATE TABLE schema_change_a (x INTEGER)`,
		`ALTER TABLE runs ADD COLUMN schema_change_b TEXT`,
		`CREATE INDEX schema_change_c ON entries (tenant, created_at)`,
	} {
		if err := sqlite.Exec(other, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
		use(2 + i)
	}
}
