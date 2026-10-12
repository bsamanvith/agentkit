//go:build integration

package postgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// Many goroutines over two pools on one schema race for what only one may
// get: a conversation's unfinished run, a run's lease, a run's move out of a
// status, and a position in a conversation and in an event log. Exactly one
// wins each race, and the losers get the error the contract names. The races
// start together from a closed channel.
func TestRacesAcrossPoolsHaveOneWinner(t *testing.T) {
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

	race("AppendEvents", func(i int) error {
		return p.AppendEvents(ctx, run.ID, []store.Event{{Seq: 1, Type: "racer", Data: json.RawMessage(fmt.Sprint(i))}})
	}, store.ErrConflict)
	if last, err := p.LastEventSeq(ctx, run.ID); err != nil || last != 1 {
		t.Errorf("after the race the log ends at %d, %v; want 1", last, err)
	}
}

// Messages, records and event data are stored as the bytes Append accepted:
// the columns are TEXT, which Postgres keeps as given, not JSONB, which
// re-encodes (it drops whitespace, sorts keys, keeps the last of duplicate
// keys and rewrites numbers). An Opaque block's data, which a provider signs,
// must come back byte for byte.
func TestJSONIsStoredByteForByte(t *testing.T) {
	t.Parallel()
	u := schemaURL(t)
	s := openURL(t, u)
	ctx := tenantCtx(t, "acme")

	data := json.RawMessage("{ \"type\":\"thinking\",  \"signature\" : \"c2ln<&>\",\n\t\"b\": 1, \"a\": 1.50e0, \"a\": 2, \"u\": \"\\u00e9\" }")
	e := store.Entry{Seq: 1, Kind: store.KindTurn, Messages: []llm.Message{
		llm.Assistant(llm.Opaque{Provider: "anthropic", Data: data}, llm.Text{Text: "é ✓"}),
	}}
	if err := s.Append(ctx, "c", e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	want, err := json.Marshal(e.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	queryRow(t, u, `SELECT messages FROM entries WHERE conversation = 'c' AND seq = 1`, nil, &raw)
	if raw != string(want) {
		t.Errorf("the messages column holds\n%s\nwant the bytes Append was given\n%s", raw, want)
	}
	page, err := s.History(ctx, "c", store.Page{})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("History = %+v, %v", page.Entries, err)
	}
	if o, ok := page.Entries[0].Messages[0].Content[0].(llm.Opaque); !ok || string(o.Data) != string(data) {
		t.Errorf("Opaque block read back as %#v, want data %s", page.Entries[0].Messages[0].Content[0], data)
	}

	if err := s.AppendEvents(ctx, "r", []store.Event{{Seq: 1, Type: "t", Data: data}}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	queryRow(t, u, `SELECT data FROM events WHERE run_id = 'r' AND seq = 1`, nil, &raw)
	if raw != string(data) {
		t.Errorf("the events data column holds\n%s\nwant\n%s", raw, data)
	}
}

// failNext makes the next n writes to table fail with the SQLSTATE code: a
// constraint trigger deferred to commit raises it, as Postgres raises a
// serialization failure when a transaction commits. The sequence it counts
// with is not transactional, so a rolled-back attempt still counts; tries
// reports how many writes the trigger has seen.
func failNext(t *testing.T, url, table, code string, n int) (tries func() int64) {
	t.Helper()
	seq := "fail_" + table
	exec(t, url, `CREATE SEQUENCE IF NOT EXISTS `+seq)
	exec(t, url, `CREATE OR REPLACE FUNCTION fail_next() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval(TG_ARGV[0]) <= TG_ARGV[2]::bigint THEN
				RAISE EXCEPTION 'forced by the test' USING ERRCODE = TG_ARGV[1];
			END IF;
			RETURN NULL;
		END $$`)
	exec(t, url, fmt.Sprintf(`CREATE CONSTRAINT TRIGGER fail_next AFTER INSERT OR UPDATE ON %s
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_next('%s', '%s', '%d')`, table, seq, code, n))
	return func() int64 {
		var v int64
		queryRow(t, url, `SELECT COALESCE(last_value, 0) FROM pg_sequences WHERE schemaname = current_schema() AND sequencename = $1`,
			[]any{seq}, &v)
		return v
	}
}

// A write the database refuses with a serialization failure or a deadlock
// runs again, and its effect is there once: no duplicate row, no failure
// counted twice. One that keeps being refused gives up after a bounded number
// of attempts with the database's error, having changed nothing.
func TestSerializationFailuresAreRetried(t *testing.T) {
	t.Parallel()
	type op struct {
		table string
		setup func(t *testing.T, s *postgres.Store)
		write func(s *postgres.Store) error
		// check is called after the write: done says whether it succeeded.
		check func(t *testing.T, s *postgres.Store, done bool)
	}
	events := []store.Event{{Seq: 1, Type: "a"}, {Seq: 2, Type: "b"}, {Seq: 3, Type: "c"}}
	createRun := func(t *testing.T, s *postgres.Store) {
		t.Helper()
		if _, err := s.Create(tenantCtx(t, "acme"), "r", "c"); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	ops := map[string]op{
		"Append": {
			table: "entries",
			write: func(s *postgres.Store) error { return s.Append(tenantCtx(t, "acme"), "c", prompt(1, "once")) },
			check: func(t *testing.T, s *postgres.Store, done bool) {
				page, err := s.History(tenantCtx(t, "acme"), "c", store.Page{})
				if want := map[bool]int{true: 1, false: 0}[done]; err != nil || len(page.Entries) != want {
					t.Errorf("History = %d entries, %v; want %d", len(page.Entries), err, want)
				}
			},
		},
		"AppendEvents": {
			table: "events",
			write: func(s *postgres.Store) error { return s.AppendEvents(tenantCtx(t, "acme"), "r", events) },
			check: func(t *testing.T, s *postgres.Store, done bool) {
				got, err := s.EventsAfter(tenantCtx(t, "acme"), "r", 0, 0)
				if want := map[bool]int{true: 3, false: 0}[done]; err != nil || len(got) != want {
					t.Errorf("EventsAfter = %d events, %v; want %d", len(got), err, want)
				}
			},
		},
		"Create": {
			table: "runs",
			write: func(s *postgres.Store) error {
				_, err := s.Create(tenantCtx(t, "acme"), "r", "c")
				return err
			},
			check: func(t *testing.T, s *postgres.Store, done bool) {
				page, err := s.List(tenantCtx(t, "acme"), "c", store.Page{})
				if want := map[bool]int{true: 1, false: 0}[done]; err != nil || len(page.Runs) != want {
					t.Errorf("List = %d runs, %v; want %d", len(page.Runs), err, want)
				}
			},
		},
		"Transition": {
			table: "runs",
			setup: createRun,
			write: func(s *postgres.Store) error {
				_, err := s.Transition(tenantCtx(t, "acme"), "r", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom"})
				return err
			},
			check: func(t *testing.T, s *postgres.Store, done bool) {
				r, err := s.Get(tenantCtx(t, "acme"), "r")
				want := map[bool]store.Status{true: store.StatusFailed, false: store.StatusRunning}[done]
				if err != nil || r.Status != want || r.Attempts != map[bool]int{true: 1, false: 0}[done] {
					t.Errorf("Get = %s with %d attempts, %v; want %s with one attempt per failure", r.Status, r.Attempts, err, want)
				}
			},
		},
		"Claim": {
			table: "runs",
			setup: createRun,
			write: func(s *postgres.Store) error {
				_, err := s.Claim(tenantCtx(t, "acme"), "r", "w1", time.Hour)
				return err
			},
			check: func(t *testing.T, s *postgres.Store, done bool) {
				_, err := s.Claim(tenantCtx(t, "acme"), "r", "w2", time.Hour)
				if done && !errors.Is(err, store.ErrLeased) || !done && err != nil {
					t.Errorf("a second Claim = %v; want ErrLeased if the first took the lease, else nil", err)
				}
			},
		},
		"Save": {
			table: "checkpoints",
			write: func(s *postgres.Store) error {
				return s.Save(tenantCtx(t, "acme"), "r", flow.Checkpoint{RunID: "r", Shape: "v1:x"})
			},
			check: func(t *testing.T, s *postgres.Store, done bool) {
				_, err := s.Load(tenantCtx(t, "acme"), "r")
				if done && err != nil || !done && !errors.Is(err, flow.ErrNotFound) {
					t.Errorf("Load = %v; want the checkpoint only if Save succeeded", err)
				}
			},
		},
	}
	for name, o := range ops {
		for _, code := range []string{"40001", "40P01"} {
			t.Run(name+"/"+code+"/once", func(t *testing.T) {
				t.Parallel()
				u := schemaURL(t)
				s := openURL(t, u)
				if o.setup != nil {
					o.setup(t, s)
				}
				tries := failNext(t, u, o.table, code, 1)
				if err := o.write(s); err != nil {
					t.Fatalf("after one refused attempt: %v; want it tried again and done", err)
				}
				if got := tries(); got < 2 {
					t.Errorf("the trigger saw %d writes, want the refused one and its retry", got)
				}
				o.check(t, s, true)
			})
			t.Run(name+"/"+code+"/always", func(t *testing.T) {
				t.Parallel()
				u := schemaURL(t)
				s := openURL(t, u)
				if o.setup != nil {
					o.setup(t, s)
				}
				tries := failNext(t, u, o.table, code, 1<<30)
				err := o.write(s)
				var pe *pgconn.PgError
				if !errors.As(err, &pe) || pe.Code != code {
					t.Fatalf("always refused: %v; want the database's %s", err, code)
				}
				if got := tries(); got != 8 {
					t.Errorf("the write ran %d times, want 8 (the bound)", got)
				}
				exec(t, u, `DROP TRIGGER fail_next ON `+o.table) // so that check's own writes go through
				o.check(t, s, false)
			})
		}
	}
}

// A checkpoint is one JSON document the store does not check, and
// json.Marshal passes the bytes of a json.RawMessage through, so its state can
// hold bytes that are not UTF-8, which a TEXT column cannot. So can its run
// id, which Save does not check. Postgres refuses both, and the store reports
// that as store.ErrInvalid, not as an error of the database's.
func TestCheckpointTextPostgresCannotHold(t *testing.T) {
	t.Parallel()
	s := open(t, clock.Real())
	ctx := tenantCtx(t, "acme")
	for name, save := range map[string]func() error{
		"state": func() error {
			return s.Save(ctx, "r", flow.Checkpoint{RunID: "r", Initial: json.RawMessage("\"\xff\"")})
		},
		"run id": func() error { return s.Save(ctx, "r\x00", flow.Checkpoint{RunID: "r"}) },
	} {
		if err := save(); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("Save of a checkpoint with a %s that is not text = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.Load(ctx, "r"); !errors.Is(err, flow.ErrNotFound) {
		t.Errorf("Load after the refused saves = %v, want ErrNotFound", err)
	}
}
