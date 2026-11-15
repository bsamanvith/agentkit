package cycle_test

import (
	"encoding/json"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// regional is the same plot three times, in three states: one whose
// overlay changes the cycle rules, one whose overlay switches a crop off,
// and one with no overlay. With and without the region packs.
type regional struct {
	packed, plain *cycle.Service
	plan          *planner.Planner
	plots         map[string]string // by state
}

const otherState = "Telangana"

func setupRegions(t *testing.T, overlays map[string]string) *regional {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range overlays {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	set, err := region.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.NewFake(now)
	loc := &farmtest.Locator{}
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(farm.WithLocator(loc)), farm.WithServiceClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	climate := &agrotest.Source{Clim: agrotest.Hyderabad()}
	r := &regional{plots: map[string]string{}}
	r.plan = planner.New(svc, set.Base().Library(), climate, planner.WithClock(clk), planner.WithRegions(set))
	plain := planner.New(svc, set.Base().Library(), climate, planner.WithClock(clk))
	r.packed = cycle.New(r.plan, cycletest.NewMemory())
	r.plain = cycle.New(plain, cycletest.NewMemory())
	for _, state := range []string{"Rules State", "Off State", otherState} {
		loc.Location = farm.Location{State: state, Country: "India", CountryCode: "IN"}
		f, err := svc.Create(ctx, farm.Draft{Name: state, Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.plan.SetProfile(ctx, f.ID, agro.Profile{Soil: "black", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}, PreviousCrop: "maize"}); err != nil {
			t.Fatal(err)
		}
		r.plots[state] = f.ID
	}
	return r
}

func overlayFile(state, body string) string {
	return `{"region": {"country": "IN", "state": "` + state + `"},
		"sources": [{"id": "t", "name": "Test", "publisher": "us", "url": "https://example.org", "read": "2026-11-15"}], ` + body + `}`
}

func lineOf(t *testing.T, s *cycle.Service, id string) cycle.Line {
	t.Helper()
	l, err := s.Line(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func suggested(l cycle.Line) []string {
	var out []string
	for _, p := range l.Cycles {
		if p.Suggested != nil {
			for _, a := range p.Suggested.Areas {
				out = append(out, a.Crop)
			}
		}
	}
	return out
}

// A rules overlay changes the suggested line where it holds and nowhere
// else; a crop an overlay switches off is never suggested there, and still
// is elsewhere; a plot with no overlay gets exactly the line it got before
// there were packs.
func TestRegionPacksChangeTheLineThereOnly(t *testing.T) {
	t.Parallel()
	// The crop the base suggests first here, to switch off.
	probe := setupRegions(t, nil)
	first := lineOf(t, probe.plain, probe.plots[otherState]).Cycles[0].Suggested.MainCrop()

	r := setupRegions(t, map[string]string{
		"rules.json": overlayFile("Rules State", `"rules": {"turnaround_days": 60, "ref": {"source": "t", "where": "p. 1"}}`),
		"off.json":   overlayFile("Off State", `"crops": {"`+first+`": {"off": true, "ref": {"source": "t", "where": "p. 2"}}}`),
	})

	// No overlay: byte for byte the line without packs.
	other := r.plots[otherState]
	a, _ := json.Marshal(lineOf(t, r.packed, other))
	b, _ := json.Marshal(lineOf(t, r.plain, other))
	if string(a) != string(b) {
		t.Errorf("a plot with no overlay got another line:\n%s\n%s", a, b)
	}

	// The rules overlay: 60 days to turn the land round, there only.
	ruled := lineOf(t, r.packed, r.plots["Rules State"])
	if len(ruled.Cycles) < 2 || ruled.Cycles[0].Planned == nil {
		t.Fatalf("rules state line %+v", ruled)
	}
	if want := plus(ruled.Cycles[0].Planned.Harvest, 60); ruled.Cycles[1].From != want {
		t.Errorf("rules state: cycle 2 free from %s, want %s", ruled.Cycles[1].From, want)
	}
	base := lineOf(t, r.plain, r.plots["Rules State"])
	if want := plus(base.Cycles[0].Planned.Harvest, cycle.Turnaround); base.Cycles[1].From != want {
		t.Errorf("without packs: cycle 2 free from %s, want %s", base.Cycles[1].From, want)
	}

	// The switched-off crop: never suggested there, still elsewhere.
	if got := suggested(lineOf(t, r.packed, r.plots["Off State"])); slices.Contains(got, first) || len(got) == 0 {
		t.Errorf("off state suggests %v; %s is off there", got, first)
	}
	if got := suggested(lineOf(t, r.packed, other)); !slices.Contains(got, first) {
		t.Errorf("other state suggests %v; want %s still", got, first)
	}
	rec, err := r.plan.Recommend(ctx, r.plots["Off State"], planner.Ask{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range rec.Options {
		if o.Crop == first {
			t.Errorf("the recommender offers %s where it is off", first)
		}
	}
	// Asked for by name, it is still scored there.
	named, err := r.plan.Recommend(ctx, r.plots["Off State"], planner.Ask{Crops: []string{first}})
	if err != nil || len(named.Options) != 1 {
		t.Errorf("asked for %s by name: %v, %+v", first, err, named.Options)
	}
	// A plan that has it still works there: the crop stays known.
	f, _ := r.plan.Farms().Get(ctx, r.plots["Off State"])
	if _, err := r.plan.LibraryFor(f).Crop(first); err != nil {
		t.Errorf("%s is unknown where it is off: %v", first, err)
	}
}

// The base rules are the constants the line was built with.
func TestBaseRulesAreTheConstants(t *testing.T) {
	t.Parallel()
	if r := region.DefaultRules(); r.TurnaroundDays != cycle.Turnaround || r.MaxWaitDays != cycle.MaxWait {
		t.Errorf("base rules %+v; constants %d, %d", r, cycle.Turnaround, cycle.MaxWait)
	}
}
