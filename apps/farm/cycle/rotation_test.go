package cycle_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
)

// library returns the default library with only the crops named.
func library(t *testing.T, ids ...string) *agro.Library {
	t.Helper()
	def, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	var crops []agro.Crop
	for _, c := range def.Crops() {
		if slices.Contains(ids, c.ID) {
			c.Intercrops = nil
			crops = append(crops, c)
		}
	}
	soils, _ := json.Marshal(map[string]any{"soils": def.Soils()})
	cs, _ := json.Marshal(map[string]any{"crops": crops})
	l, err := agro.Load(soils, cs)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The rotation: of crops that score alike, the one of another family than
// the crop before is suggested; a legume after a cereal; and a crop of the
// same family only when nothing else fits.
func TestRotationPicksAmongEquals(t *testing.T) {
	t.Parallel()
	w := setup(t)
	clim := w.climate.Clim
	f, _ := w.svc.Get(ctx, w.plot)
	suggest := func(lib *agro.Library, prev string) string {
		t.Helper()
		soil, _ := lib.Soil("black")
		l, err := cycle.Compute(cycle.Input{
			Library: lib, Farm: w.plot, Ring: f.Boundary.Ring(), Climate: &clim, Soil: &soil,
			Profile: agro.Profile{Soil: "black", Water: f.Profile.Water, PreviousCrop: prev}, Today: now,
			Record: cycle.Record{FutureOff: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if l.Cycles[0].Suggested == nil {
			return ""
		}
		return l.Cycles[0].Suggested.MainCrop()
	}
	// Black gram and chilli both score 100 on 1 June at this plot.
	two := library(t, "black_gram", "chilli", "green_gram", "rice")
	if got := suggest(library(t, "black_gram", "chilli", "green_gram"), "green_gram"); got != "chilli" {
		t.Errorf("after green gram: %s; want chilli, not a legume again", got)
	}
	if got := suggest(two, "rice"); got != "black_gram" {
		t.Errorf("after rice: %s; want black gram, a legume after a cereal", got)
	}
	if got := suggest(library(t, "black_gram", "green_gram"), "green_gram"); got != "black_gram" {
		t.Errorf("only legumes fit: %s; want black gram all the same", got)
	}
}
