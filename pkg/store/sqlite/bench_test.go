package sqlite_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// BenchmarkAppend appends to one conversation, one entry per operation, as
// the runner does after every turn (a committed transaction each). Run with
// -benchtime=1000x for the cost of 1k entries.
func BenchmarkAppend(b *testing.B) {
	s := open(b, clock.Real())
	ctx := tenantCtx(b, "acme")
	var seq int64
	b.ReportAllocs()
	for b.Loop() {
		seq++
		if err := s.Append(ctx, "c", prompt(seq, fmt.Sprintf("turn %d", seq))); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListResumable reads a page of 100 resumable runs from a tenant
// with 10k runs, with ListResumable and with Admin's ListResumableAll.
//
//   - mixed: a quarter of the runs each never claimed (resumable), leased,
//     suspended and completed, in creation order;
//   - mostly-leased: every running run holds a live lease except the newest
//     100, so the query walks the whole resumable index to find them.
func BenchmarkListResumable(b *testing.B) {
	const n = 10_000
	for _, layout := range []string{"mixed", "mostly-leased"} {
		s := open(b, clock.Real())
		ctx := tenantCtx(b, "acme")
		for i := range n {
			id := fmt.Sprintf("r%05d", i)
			if _, err := s.Create(ctx, id, "c"+id); err != nil {
				b.Fatal(err)
			}
			var err error
			switch {
			case layout == "mostly-leased" && i < n-100, layout == "mixed" && i%4 == 1:
				_, err = s.Claim(ctx, id, "worker", time.Hour)
			case layout == "mixed" && i%4 == 2:
				_, err = s.Transition(ctx, id, store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"main/ask"}})
			case layout == "mixed" && i%4 == 3:
				_, err = s.Transition(ctx, id, store.StatusRunning, store.StatusCompleted, store.Details{})
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		admin := sqlite.Admin(s)
		b.Run(layout+"/ListResumable", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				runs, err := s.ListResumable(ctx, time.Now(), 100)
				if err != nil || len(runs) != 100 {
					b.Fatalf("ListResumable = %d runs, %v", len(runs), err)
				}
			}
		})
		b.Run(layout+"/ListResumableAll", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				runs, err := admin.ListResumableAll(ctx, time.Now(), 100)
				if err != nil || len(runs) != 100 {
					b.Fatalf("ListResumableAll = %d runs, %v", len(runs), err)
				}
			}
		})
	}
}
