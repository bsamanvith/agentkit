package memorytest

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// simLimits are small, so a simulation reaches every limit.
var simLimits = memory.Limits{MaxFileBytes: 48, MaxScopeBytes: 160, MaxFiles: 6}

// simSeeds is how many seeds the suite's simulation runs.
const simSeeds = 8

// testSimulation runs Simulate over seeds 1 to simSeeds, each on a new store.
func testSimulation(t *testing.T, newStore NewStore) {
	for seed := range uint64(simSeeds) {
		t.Run(fmt.Sprint("seed", seed+1), func(t *testing.T) {
			t.Parallel()
			s := newStore(t, memory.Config{Limits: simLimits})
			Simulate(t, s, simLimits, seed+1, 50)
		})
	}
}

// Simulate runs n random commands chosen by seed on s, a store of limits
// l, through Step's checks. Commands mix valid and invalid paths, edits that
// match once, never and twice, lines in and out of files, and writes past
// the limits. A failure names the seed and the step.
func Simulate(t testing.TB, s memory.Store, l memory.Limits, seed uint64, n int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	ctx := Ctx(t, "sim", "u")
	files := map[string]string{}
	for i := range n {
		o := randomOp(rng, files)
		before := t.Failed()
		files, _ = Step(ctx, t, s, l, files, o)
		if !before && t.Failed() {
			t.Fatalf("seed %d, step %d: %v broke a check (above)", seed, i, o)
		}
	}
}

var (
	simNames = []string{"a", "ab", "b", "notes.md", "c"}
	simWords = []string{"alpha", "beta", "alpha beta", "", "gamma\n", "x"}
	simBad   = []string{"/memories/../x", "/etc/passwd", "/memories/.hidden", "", "memories/a", "/memories/a/../b"}
)

// randomPath is a path from a small set, so commands collide: mostly valid,
// sometimes unclean (which is fine), sometimes invalid, sometimes Root.
func randomPath(rng *rand.Rand, files map[string]string) string {
	switch r := rng.IntN(20); {
	case r == 0:
		return simBad[rng.IntN(len(simBad))]
	case r == 1:
		return memory.Root
	case r < 14 && len(files) > 0:
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range files {
				if !yield(k) {
					return
				}
			}
		})
		p := keys[rng.IntN(len(keys))]
		if rng.IntN(4) == 0 {
			// Its directory, or the same path unclean.
			if ps := memory.Parents(p); len(ps) > 0 {
				return ps[rng.IntN(len(ps))]
			}
			return strings.Replace(p, "/", "//", 2) + "/"
		}
		return p
	}
	var b strings.Builder
	b.WriteString(memory.Root)
	for range 1 + rng.IntN(3) {
		b.WriteString("/" + simNames[rng.IntN(len(simNames))])
	}
	return b.String()
}

func randomText(rng *rand.Rand) string {
	var parts []string
	for range rng.IntN(4) {
		parts = append(parts, simWords[rng.IntN(len(simWords))])
	}
	if rng.IntN(5) == 0 {
		parts = append(parts, strings.Repeat("z", rng.IntN(60)))
	}
	return strings.Join(parts, "\n")
}

func randomOp(rng *rand.Rand, files map[string]string) Op {
	p := randomPath(rng, files)
	switch rng.IntN(6) {
	case 0:
		return view(p)
	case 1:
		return create(p, randomText(rng))
	case 2:
		old := simWords[rng.IntN(len(simWords))]
		if text, ok := files[p]; ok && text != "" && rng.IntN(2) == 0 {
			i := rng.IntN(len(text))
			old = text[i : i+1+rng.IntN(len(text)-i)]
		}
		return replace(p, old, randomText(rng))
	case 3:
		return insert(p, rng.IntN(5)-1, randomText(rng))
	case 4:
		return del(p)
	default:
		return rename(p, randomPath(rng, files))
	}
}

// testConcurrent runs workers that edit at once: each owns a line of a shared
// file, which it rewrites with str_replace, and files of its own, which it
// creates, appends to and renames. No edit may be lost: at the end the shared
// file has each worker's last line once, and each worker's files are what it
// last wrote. Two tenants run at once, and neither sees the other's files.
func testConcurrent(t *testing.T, newStore NewStore) {
	const workers, steps = 6, 25
	l := memory.Limits{MaxFileBytes: 4 << 10, MaxScopeBytes: 64 << 10, MaxFiles: 100}
	s := newStore(t, memory.Config{Limits: l})
	for _, tenant := range []string{"c1", "c2"} {
		ctx := Ctx(t, tenant, "")
		var shared strings.Builder
		for w := range workers {
			fmt.Fprintf(&shared, "w%d=0\n", w)
		}
		if err := s.Create(ctx, "/memories/shared.md", shared.String()); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		worker  int
		version int
		files   map[string]string
	}
	results := map[string][]result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tenant := range []string{"c1", "c2"} {
		for w := range workers {
			wg.Go(func() {
				ctx := Ctx(t, tenant, "")
				rng := rand.New(rand.NewPCG(uint64(w), uint64(len(tenant))))
				res := result{worker: w, files: map[string]string{}}
				own := fmt.Sprintf("/memories/w%d", w)
				for i := range steps {
					switch rng.IntN(4) {
					case 0:
						old, new := fmt.Sprintf("w%d=%d\n", w, res.version), fmt.Sprintf("w%d=%d\n", w, res.version+1)
						if err := s.StrReplace(ctx, "/memories/shared.md", old, new); err != nil {
							t.Errorf("%s worker %d: str_replace: %v", tenant, w, err)
							return
						}
						res.version++
					case 1:
						p, text := fmt.Sprintf("%s/f%d.md", own, rng.IntN(3)), fmt.Sprintf("%s %d %d", tenant, w, i)
						if err := s.Create(ctx, p, text); err != nil {
							t.Errorf("%s worker %d: create: %v", tenant, w, err)
							return
						}
						res.files[p] = text
					case 2:
						p := own + "/log.md"
						line := fmt.Sprintf("%s %d step %d", tenant, w, i)
						var err error
						if _, ok := res.files[p]; ok {
							err = s.Insert(ctx, p, strings.Count(res.files[p], "\n"), line)
						} else {
							err = s.Create(ctx, p, "")
							if err == nil {
								err = s.Insert(ctx, p, 0, line)
							}
						}
						if err != nil {
							t.Errorf("%s worker %d: insert: %v", tenant, w, err)
							return
						}
						res.files[p] += line + "\n"
					default:
						p := fmt.Sprintf("%s/f%d.md", own, rng.IntN(3))
						text, ok := res.files[p]
						if !ok {
							continue
						}
						to := fmt.Sprintf("%s/r%d/f.md", own, i)
						if err := s.Rename(ctx, p, to); err != nil {
							t.Errorf("%s worker %d: rename: %v", tenant, w, err)
							return
						}
						delete(res.files, p)
						res.files[to] = text
					}
				}
				mu.Lock()
				results[tenant] = append(results[tenant], res)
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for _, tenant := range []string{"c1", "c2"} {
		ctx := Ctx(t, tenant, "")
		files := CheckState(ctx, t, s, l)
		want := map[string]string{}
		shared := files["/memories/shared.md"]
		for _, r := range results[tenant] {
			for p, text := range r.files {
				want[p] = text
			}
			if line := fmt.Sprintf("w%d=%d\n", r.worker, r.version); strings.Count(shared, line) != 1 {
				t.Errorf("%s: shared file %q does not have worker %d's last line %q once (an edit was lost)", tenant, shared, r.worker, line)
			}
		}
		if n := strings.Count(shared, "\n"); n != workers {
			t.Errorf("%s: shared file has %d lines, want %d: %q", tenant, n, workers, shared)
		}
		delete(files, "/memories/shared.md")
		for p, text := range want {
			if files[p] != text {
				t.Errorf("%s: %s = %q, want %q (an edit was lost)", tenant, p, files[p], text)
			}
		}
		if len(files) != len(want) {
			t.Errorf("%s: %d files besides the shared one, want %d", tenant, len(files), len(want))
		}
	}
}
