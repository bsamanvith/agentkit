package agro_test

import (
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
)

// The wide tier loads, is large, is all experimental, and holds no
// species the library already has.
func TestWideTier(t *testing.T) {
	t.Parallel()
	w, err := agro.Wide()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(w.Crops()); n < 1000 {
		t.Fatalf("%d experimental plants, want a thousand or more", n)
	}
	have := map[string]bool{}
	for _, c := range library(t).Crops() {
		if c.Species == "" || c.Experimental {
			t.Errorf("library crop %s: no species, or marked experimental", c.ID)
		}
		have[c.Species] = true
	}
	for _, c := range w.Crops() {
		if !c.Experimental || c.Species == "" || have[c.Species] {
			t.Errorf("wide crop %s (%s): not experimental, no species, or already in the library", c.ID, c.Species)
		}
	}
	// The whole tier is scored for a place in well under a second or two,
	// best first, each option saying it is experimental.
	start := time.Now()
	res, err := agro.Recommend(w, agro.Request{Climate: agrotest.Hyderabad(), Today: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scored %d plants in %s; best: %s %d, %s %d, %s %d", len(res.Options), time.Since(start),
		res.Options[0].Species, res.Options[0].Score, res.Options[1].Species, res.Options[1].Score, res.Options[2].Species, res.Options[2].Score)
	o := res.Options[0]
	if !o.Experimental || o.Species == "" || o.Score < 60 || len(o.Risks) == 0 || o.Risks[len(o.Risks)-1].ID != "experimental" {
		t.Errorf("best option %+v", o)
	}
	if c, err := w.Crop("coffea_arabica"); err != nil || !c.Matches("coffee") || !c.Matches("Coffea") || c.Matches("tea") {
		t.Errorf("coffee: %+v, %v", c.Common, err)
	}
}
