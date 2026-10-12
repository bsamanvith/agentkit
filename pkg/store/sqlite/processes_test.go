package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// helperEnv names the database, writer and entry count for the helper
// process: "path|writer|n".
const helperEnv = "AGENTKIT_SQLITE_APPEND_HELPER"

// Two processes append to one conversation of one file at once, each the way
// the runner does: read the context, append at Seq+1, and on ErrConflict
// reload and try again. No entry is lost or written twice, the log stays
// dense, and each process's entries are in its own order. The processes are
// this test binary run again (TestAppendHelperProcess), so the file's locks
// are real POSIX locks between processes.
func TestTwoProcessesAppendToOneConversation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("starts two processes")
	}
	path := filepath.Join(t.TempDir(), "agentkit.db")
	s := openPath(t, path) // migrated before the helpers start
	const perWriter = 150
	writers := []string{"w1", "w2"}

	var wg sync.WaitGroup
	outs := make([]string, len(writers))
	errs := make([]error, len(writers))
	for i, w := range writers {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAppendHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s|%s|%d", helperEnv, path, w, perWriter))
		wg.Go(func() {
			out, err := cmd.CombinedOutput()
			outs[i], errs[i] = string(out), err
		})
	}
	wg.Wait()
	conflicts := 0
	for i, w := range writers {
		if errs[i] != nil {
			t.Fatalf("helper %s: %v\n%s", w, errs[i], outs[i])
		}
		_, after, ok := strings.Cut(outs[i], "conflicts=")
		n, err := strconv.Atoi(strings.Fields(after + " x")[0])
		if !ok || err != nil {
			t.Fatalf("helper %s printed no conflict count:\n%s", w, outs[i])
		}
		conflicts += n
	}

	ctx := tenantCtx(t, "acme")
	page, err := s.History(ctx, "c", store.Page{Limit: store.MaxPageLimit})
	if err != nil || page.Next != "" {
		t.Fatalf("History = next %q, %v; want the whole log in one page", page.Next, err)
	}
	all := page.Entries
	if len(all) != perWriter*len(writers) {
		t.Fatalf("the log has %d entries, want %d", len(all), perWriter*len(writers))
	}
	next := map[string]int{}
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
		w, num, _ := strings.Cut(e.Messages[0].Text(), "/")
		if want := strconv.Itoa(next[w]); num != want {
			t.Fatalf("entry %d is %s's %s, want its %s", e.Seq, w, num, want)
		}
		next[w]++
	}
	for _, w := range writers {
		if next[w] != perWriter {
			t.Errorf("writer %s has %d entries, want %d", w, next[w], perWriter)
		}
	}
	t.Logf("%d entries from %d processes, %d conflicts reported and retried", len(all), len(writers), conflicts)
}

// TestAppendHelperProcess is one writer of
// TestTwoProcessesAppendToOneConversation; it does nothing in a normal run.
func TestAppendHelperProcess(t *testing.T) {
	spec := os.Getenv(helperEnv)
	if spec == "" {
		t.Skip("run by TestTwoProcessesAppendToOneConversation")
	}
	parts := strings.Split(spec, "|")
	n, err := strconv.Atoi(parts[2])
	if len(parts) != 3 || err != nil {
		t.Fatalf("bad %s %q", helperEnv, spec)
	}
	path, writer := parts[0], parts[1]
	s, err := sqlite.Open(context.Background(), path, sqlite.WithClock(clock.Real()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := tenantCtx(t, "acme")
	conflicts := 0
	for i := 0; i < n; {
		view, err := s.Context(ctx, "c")
		if err != nil {
			t.Fatalf("Context: %v", err)
		}
		err = s.Append(ctx, "c", prompt(view.Seq+1, fmt.Sprintf("%s/%d", writer, i)))
		switch {
		case err == nil:
			i++
		case errors.Is(err, store.ErrConflict):
			conflicts++ // another process took the position: reload
		default:
			t.Fatalf("Append: %v", err)
		}
	}
	fmt.Printf("conflicts=%d\n", conflicts)
}

// Many goroutines over two Stores on one file race for what only one may
// get: a conversation's unfinished run, a run's lease, and a run's move out
// of a status. Exactly one wins each race, and the losers get the error the
// contract names. The races start together from a closed channel.
func TestRacesAcrossHandlesHaveOneWinner(t *testing.T) {
	t.Parallel()
	p := openPair(t, clock.Real())
	ctx := tenantCtx(t, "acme")
	const racers = 16

	race := func(name string, try func(i int) error, lost error) {
		t.Helper()
		start := make(chan struct{})
		errs := make([]error, racers)
		var wg sync.WaitGroup
		for i := range racers {
			wg.Go(func() {
				<-start
				errs[i] = try(i)
			})
		}
		close(start)
		wg.Wait()
		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, lost):
				t.Errorf("%s: racer %d got %v, want nil or %v", name, i, err, lost)
			}
		}
		if won != 1 {
			t.Errorf("%s: %d racers won, want exactly 1", name, won)
		}
	}

	race("Create", func(i int) error {
		_, err := p.Create(ctx, fmt.Sprintf("r%d", i), "c")
		return err
	}, store.ErrUnfinished)
	run, err := p.Unfinished(ctx, "c")
	if err != nil {
		t.Fatalf("Unfinished: %v", err)
	}

	race("Claim", func(i int) error {
		_, err := p.Claim(ctx, run.ID, fmt.Sprintf("owner%d", i), time.Hour)
		return err
	}, store.ErrLeased)

	race("Transition", func(int) error {
		_, err := p.Transition(ctx, run.ID, store.StatusRunning, store.StatusFailed, store.Details{Error: "boom"})
		return err
	}, store.ErrConflict)
	if got, err := p.Get(ctx, run.ID); err != nil || got.Attempts != 1 {
		t.Errorf("after the race the run is %+v, %v; want one failure counted", got, err)
	}

	race("Append", func(i int) error {
		return p.Append(ctx, "c", prompt(1, fmt.Sprintf("racer %d", i)))
	}, store.ErrConflict)
}
