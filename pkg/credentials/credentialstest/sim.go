package credentialstest

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// Simulate runs one seeded simulation against a fresh store: creates,
// replaces, deletes, reads and lists with fresh and stale versions over two
// tenants, two users and two slots, plus rounds where several writers race
// on one version at once. Every result is checked against a model; a failure
// names the seed and step, and the same seed replays the same calls (the
// races aside, whose winner the model learns from the store).
func Simulate(t *testing.T, newStore func(t *testing.T) credentials.BlobStore, seed uint64) {
	t.Helper()
	r := newRig(t, newStore(t), nil)
	rng := rand.New(rand.NewPCG(seed, 0x51d))
	type mkey struct{ tenant, user, slot string }
	type entry struct {
		version int64
		data    string
		marked  bool // NeedsReconnect
	}
	model := map[mkey]entry{} // current credentials
	last := map[mkey]int64{}  // last version ever
	tenants := []string{tenantA, tenantB}
	slots := []string{"crm", "mail"}
	steps := 50 + rng.IntN(150)
	for step := range steps {
		k := mkey{tenants[rng.IntN(2)], users[rng.IntN(2)], slots[rng.IntN(2)]}
		ctx := r.ctx(k.tenant)
		cur, exists := model[k]
		// A version: the current one, a stale or future one, or 0.
		pick := func() int64 {
			switch rng.IntN(4) {
			case 0:
				return 0
			case 1:
				return cur.version
			case 2:
				return max(1, cur.version-1-int64(rng.IntN(2)))
			}
			return cur.version + 1
		}
		where := fmt.Sprintf("seed %d step %d %+v", seed, step, k)
		data := fmt.Sprintf("data-%d-%d", seed, step)
		switch op := rng.IntN(10); {
		case op < 4: // put
			v := pick()
			b := blob(data, t0.Add(time.Duration(step)*time.Second))
			b.NeedsReconnect = rng.IntN(3) == 0
			m, err := r.store.Put(ctx, k.user, k.slot, b, v)
			switch {
			case v == 0 && exists:
				if !is(err, credentials.ErrExists) {
					t.Fatalf("%s: create over v%d: %v, want ErrExists", where, cur.version, err)
				}
			case v != 0 && (!exists || v != cur.version):
				if !is(err, credentials.ErrConflict) {
					t.Fatalf("%s: put if %d (current %d, exists %v): %v, want ErrConflict", where, v, cur.version, exists, err)
				}
			default:
				if err != nil || m.Version != last[k]+1 || m.NeedsReconnect != b.NeedsReconnect {
					t.Fatalf("%s: put if %d: %+v, %v; want version %d, NeedsReconnect %v", where, v, m, err, last[k]+1, b.NeedsReconnect)
				}
				model[k], last[k] = entry{m.Version, data, b.NeedsReconnect}, m.Version
			}
		case op < 6: // delete
			v := pick()
			err := r.store.Delete(ctx, k.user, k.slot, v)
			switch {
			case !exists:
				if !is(err, credentials.ErrNotFound) {
					t.Fatalf("%s: delete of nothing: %v", where, err)
				}
			case v != 0 && v != cur.version:
				if !is(err, credentials.ErrConflict) {
					t.Fatalf("%s: delete if %d of v%d: %v", where, v, cur.version, err)
				}
			default:
				if err != nil {
					t.Fatalf("%s: delete: %v", where, err)
				}
				delete(model, k)
			}
		case op < 8: // get
			b, err := r.store.Get(ctx, k.user, k.slot)
			if exists && (err != nil || b.Version != cur.version || string(b.Data) != cur.data || b.NeedsReconnect != cur.marked) ||
				!exists && !is(err, credentials.ErrNotFound) {
				t.Fatalf("%s: get = v%d %q, %v; model %+v (exists %v)", where, b.Version, b.Data, err, cur, exists)
			}
		case op < 9: // list
			metas, err := r.store.List(ctx, k.user)
			if err != nil {
				t.Fatalf("%s: list: %v", where, err)
			}
			want := 0
			for mk, e := range model {
				if mk.tenant == k.tenant && mk.user == k.user {
					want++
					found := false
					for _, m := range metas {
						found = found || m.Slot == mk.slot && m.Version == e.version && m.NeedsReconnect == e.marked
					}
					if !found {
						t.Fatalf("%s: list %+v misses %+v", where, metas, mk)
					}
				}
			}
			if len(metas) != want {
				t.Fatalf("%s: list has %d, model %d", where, len(metas), want)
			}
		default: // a race on one version
			if !exists {
				continue
			}
			var wg sync.WaitGroup
			var mu sync.Mutex
			var wins []credentials.Meta
			var winData string
			for w := range 2 + rng.IntN(6) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					d := fmt.Sprintf("%s-w%d", data, w)
					m, err := r.store.Put(ctx, k.user, k.slot, blob(d, t0), cur.version)
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						wins, winData = append(wins, m), d
					} else if !is(err, credentials.ErrConflict) {
						t.Errorf("%s: racing writer: %v", where, err)
					}
				}()
			}
			wg.Wait()
			if len(wins) != 1 || wins[0].Version != last[k]+1 {
				t.Fatalf("%s: race on v%d: winners %+v, want exactly one at v%d", where, cur.version, wins, last[k]+1)
			}
			model[k], last[k] = entry{wins[0].Version, winData, false}, wins[0].Version
		}
	}
}
