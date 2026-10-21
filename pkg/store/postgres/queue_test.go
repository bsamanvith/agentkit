//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// seedQueue writes n runs straight into the runs table of the schema at
// owner, created at 1..n ns: spread over 5 tenants and lanes lane0 to lane2
// at priorities 0 to 4, a quarter of them final, one in seven leased far
// into the future, one in eleven interactive, one in thirteen not started
// until far in the future. It then refreshes the planner's statistics.
func seedQueue(t testing.TB, owner string, n int) {
	t.Helper()
	exec(t, owner, `INSERT INTO runs (tenant, id, conversation, status, created_at, finished_at,
			lane, priority, start_at, lease_owner, lease_expires, interactive)
		SELECT 't' || (i % 5), 'r' || i, 'c' || i,
			CASE WHEN i % 4 = 0 THEN 'completed' ELSE 'running' END, i,
			CASE WHEN i % 4 = 0 THEN i END,
			'lane' || (i % 3), i % 5,
			CASE WHEN i % 13 = 0 THEN 9000000000000000000 ELSE i END,
			CASE WHEN i % 7 = 0 THEN 'busy' ELSE '' END,
			CASE WHEN i % 7 = 0 THEN 9000000000000000000 END,
			i % 11 = 0
		FROM generate_series(1, $1::int) AS i`, n)
	exec(t, owner, `ANALYZE runs`)
}

// The plan of ClaimNext walks the queue index (runs_queue) for each lane,
// on a table big enough that the planner has a choice.
func TestClaimNextUsesTheQueueIndex(t *testing.T) {
	t.Parallel()
	owner := schemaURL(t)
	if err := postgres.Migrate(context.Background(), owner); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedQueue(t, owner, 20000)
	for _, lanes := range [][]string{{"lane1"}, {"lane0", "lane2"}} {
		var plan []string
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, owner)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer pool.Close()
		rows, err := pool.Query(ctx, `EXPLAIN `+postgres.ClaimNextSQL, lanes, time.Now().UnixNano(), "w", int64(1))
		if err != nil {
			t.Fatalf("EXPLAIN: %v", err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		text := strings.Join(plan, "\n")
		if !strings.Contains(text, "Index Scan using runs_queue") || strings.Contains(text, "Seq Scan on runs") {
			t.Errorf("ClaimNext from %q does not walk runs_queue:\n%s", lanes, text)
		}
		t.Logf("plan for %q:\n%s", lanes, text)
	}
}

// BenchmarkClaimNext drains a queue of b.N due runs (in one lane, beside a
// table of 20k others) with 8 claimers at once per CPU (RunParallel), each
// claim leasing the run for an hour. Every run is claimed exactly once.
func BenchmarkClaimNext(b *testing.B) {
	owner := schemaURL(b)
	s := openURL(b, appURL(b, owner), postgres.WithMigrate(false), postgres.WithAdminURL(owner), postgres.WithClock(clock.Real()))
	seedQueue(b, owner, 20000)
	admin := postgres.Admin(s)
	ctx := context.Background()
	exec(b, owner, `DELETE FROM runs WHERE lane = 'bench'`)
	exec(b, owner, `INSERT INTO runs (tenant, id, conversation, status, created_at, lane, priority, start_at)
		SELECT 'bench', 'b' || i, 'b' || i, 'running', i, 'bench', i % 3, i FROM generate_series(1, $1::int) AS i`, b.N)
	exec(b, owner, `ANALYZE runs`)
	var (
		claimed sync.Map
		n       atomic.Int64
		mu      sync.Mutex
		failed  error
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if failed == nil {
			failed = err
		}
	}
	b.SetParallelism(8)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tr, err := admin.ClaimNext(ctx, []string{"bench"}, "w", time.Hour)
			if err != nil {
				fail(err)
				return
			}
			if _, dup := claimed.LoadOrStore(tr.Run.ID, true); dup {
				fail(fmt.Errorf("run %s claimed twice", tr.Run.ID))
				return
			}
			n.Add(1)
		}
	})
	b.StopTimer()
	if failed != nil {
		b.Fatal(failed)
	}
	if got := n.Load(); got != int64(b.N) {
		b.Fatalf("claimed %d runs, want %d", got, b.N)
	}
	if _, err := admin.ClaimNext(ctx, []string{"bench"}, "w", time.Hour); !errors.Is(err, store.ErrNotFound) {
		b.Fatalf("ClaimNext of a drained queue = %v, want ErrNotFound", err)
	}
}
