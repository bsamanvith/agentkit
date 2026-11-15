package addon_test

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/region"
)

// packsByID gives the overlay's pack to one plot and the base to others.
type packsByID struct {
	set *region.Set
	in  string
}

func (p packsByID) Pack(f farm.Farm) *region.Pack {
	if f.ID == p.in {
		return p.set.For(region.Place{Country: "IN", State: "Bihar"})
	}
	return p.set.Base()
}

// The inputs addon plans from the book of the plot's region pack: an
// overlay's groundnut dose changes the shopping list there, and a plot
// elsewhere gets what the addon without packs gives.
func TestInputsFromTheRegionPack(t *testing.T) {
	t.Parallel()
	set, err := region.Load(fstest.MapFS{"bihar.json": {Data: []byte(`{
		"region": {"country": "IN", "state": "Bihar"},
		"sources": [{"id": "t", "name": "Test", "publisher": "us", "url": "https://example.org", "read": "2026-11-15"}],
		"inputs": {"crops": {"groundnut": {"rainfed": {"n": 30, "p": 60, "k": 30, "ref": {"source": "t", "where": "p. 1"}}, "ref": {"source": "t", "where": "p. 1"}}}}
	}`)}})
	if err != nil {
		t.Fatal(err)
	}
	base := set.Base()
	plain := addon.NewInputs(base.Book(), base.Library())
	packed := addon.NewInputs(base.Book(), base.Library(), addon.WithPacks(packsByID{set: set, in: "bihar"}))
	c := cycle.Cycle{Method: cycle.Conventional, Areas: []cycle.Area{{Zone: -1, Crop: "groundnut", AreaHa: 1, Sow: "2026-07-01"}}}
	plan := func(a *addon.Inputs, id string) string {
		t.Helper()
		v, err := a.For(ctx, farm.Farm{ID: id}, c)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	if plan(packed, "elsewhere") != plan(plain, "elsewhere") {
		t.Error("a plot with no overlay got another plan")
	}
	if plan(packed, "bihar") == plan(plain, "bihar") {
		t.Error("the overlay's dose did not change the plan where it holds")
	}
}
