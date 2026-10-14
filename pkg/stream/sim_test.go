package stream_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/stream"
)

// TestSimulation runs seeded random runs: durable events and deltas, failed
// publishes, crashes between store and publish with a takeover, lost and
// trimmed history, and clients that watch from any ID and reconnect at
// random. Every stretch of every watch must hold to run.check: events as
// written, in order, no repeat, no durable one missing, and the end at
// run_done. A failure names its seed.
func TestSimulation(t *testing.T) {
	t.Parallel()
	for name, seeds := range map[string]int{"memory": 200, "redis": 15} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for seed := range seeds {
				t.Run(fmt.Sprint(seed), func(t *testing.T) {
					t.Parallel()
					simulate(t, name, uint64(seed))
				})
			}
		})
	}
}

// stretch is one connection of a client: the ID it started after and what
// it got.
type stretch struct {
	w   *watch
	got []stream.Event
}

func simulate(t *testing.T, name string, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 0x7090))
	maxLen := 5 + rng.IntN(60)
	trim := func(bus.Topic) bus.Trim { return bus.Trim{MaxLen: maxLen, MaxAge: time.Hour} }
	if name == "redis" {
		maxLen = 100 + rng.IntN(200) // Redis trims whole nodes of 100
	}
	var b backend
	if name == "memory" {
		b = memoryBackend(t, trim)
	} else {
		b = redisBackend(t, trim)
	}
	ctx := tenantCtx(t, "acme")
	log := memory.New()
	f := &faulty{Bus: b.bus}
	p, _ := stream.NewPublisher(log, f, quiet)
	r := newRun(ctx, t, p, log, "run")
	topic := runTopic(ctx, t, "run")

	var clients [][]*stretch // each client's connections, in order
	connect := func(after bus.ID) *stretch {
		return &stretch{w: startWatch(ctx, t, log, b.bus, "run", after)}
	}
	// collect moves what a connection got so far into its stretch.
	collect := func(s *stretch) {
		for {
			select {
			case e := <-s.w.events:
				s.got = append(s.got, e)
			default:
				return
			}
		}
	}
	var trace []string
	defer func() {
		if t.Failed() {
			t.Logf("seed %d, max len %d: %v", seed, maxLen, trace)
		}
	}()
	r.durable("run_start")
	steps := 60 + rng.IntN(240)
	for range steps {
		x := rng.IntN(100)
		trace = append(trace, fmt.Sprintf("%s:%d", r.s.Last(), x))
		switch {
		case x < 40:
			r.durable("step")
		case x < 70:
			r.delta()
		case x < 77:
			f.failNext(1)
		case x < 81: // the process dies after storing an event
			f.dropNext(1)
			r.durable("step")
			p2, _ := stream.NewPublisher(log, f, quiet)
			r.reopen(p2)
		case x < 83:
			b.lose(t, topic)
		case x < 91: // a new client, from the start or any ID it could have
			after := bus.ID{}
			if n := len(r.events); n > 0 && rng.IntN(3) > 0 {
				after = r.events[rng.IntN(n)].ID
			}
			clients = append(clients, []*stretch{connect(after)})
		default: // a client drops and reconnects with its last ID
			if len(clients) == 0 {
				continue
			}
			k := rng.IntN(len(clients))
			cur := clients[k][len(clients[k])-1]
			cur.w.stop()
			collect(cur)
			after := cur.w.after
			if n := len(cur.got); n > 0 {
				after = cur.got[n-1].ID
			}
			clients[k] = append(clients[k], connect(after))
		}
	}
	if rng.IntN(4) == 0 {
		f.failNext(1) // the end itself is lost on the bus
	}
	r.durable("run_done")
	clients = append(clients, []*stretch{connect(bus.ID{})})
	for k, conns := range clients {
		for i, s := range conns {
			last := i == len(conns)-1
			if last {
				rest, err := s.w.end()
				if err != nil {
					t.Fatalf("seed %d: client %d ended with %v", seed, k, err)
				}
				s.got = append(s.got, rest...)
			}
			if err := r.check(s.w.after, s.got, last); err != nil {
				t.Errorf("seed %d: client %d, connection %d after %s: %v", seed, k, i, s.w.after, err)
			}
		}
	}
}
