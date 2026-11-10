package calendar_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
)

func lib(t *testing.T) *agro.Library {
	t.Helper()
	l, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func date(s string) time.Time {
	d, err := time.Parse(agro.DateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

// TestStages: maize (90 to 120 days, 105 on average) sown on 1 July goes
// through its stages at the fractions of its season.
func TestStages(t *testing.T) {
	t.Parallel()
	maize, _ := lib(t).Crop("maize")
	sow := date("2026-07-01")
	for _, c := range []struct {
		das     int
		stage   string
		inField bool
	}{
		{-1, calendar.NotSown, false},
		{0, calendar.Establishment, true},
		{15, calendar.Establishment, true},
		{16, calendar.Vegetative, true},
		{48, calendar.Flowering, true},
		{74, calendar.Maturing, true},
		{90, calendar.Harvest, true},
		{134, calendar.Harvest, true},
		{135, calendar.Harvested, false},
	} {
		got := calendar.StageAt(maize, sow, sow.AddDate(0, 0, c.das))
		if got.Name != c.stage || got.InField != c.inField || got.DAS != c.das {
			t.Errorf("day %d: %+v, want %s (in field %v)", c.das, got, c.stage, c.inField)
		}
	}
	banana, _ := lib(t).Crop("banana")
	if got := calendar.StageAt(banana, sow, sow.AddDate(3, 0, 0)); got.Name != calendar.Growing || !got.InField {
		t.Errorf("a banana after three years: %+v, want growing, in the field", got)
	}
}

func kinds(z calendar.Zone) map[string][]int {
	out := map[string][]int{}
	for _, t := range z.Tasks {
		out[t.Kind] = append(out[t.Kind], t.DAS)
	}
	return out
}

// TestAnnualCalendar: a cereal gets two nitrogen splits and its
// irrigations when irrigated; a pulse gets no urea; a rainfed crop is
// told to save moisture instead; tasks run in date order.
func TestAnnualCalendar(t *testing.T) {
	t.Parallel()
	l := lib(t)
	plan := agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "2026-07-01"}, {Crop: "green_gram", Sow: "2026-07-01"}}}
	cal, err := calendar.ForPlan(l, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	m := kinds(cal.Zones[0])
	if !slices.Equal(m[calendar.Fertilise], []int{26, 53}) {
		t.Errorf("maize top-dressing on days %v, want 26 and 53", m[calendar.Fertilise])
	}
	if len(m[calendar.Irrigate]) != 3 || m[calendar.Sow][0] != 0 || m[calendar.Prepare][0] != -21 {
		t.Errorf("maize tasks %v", m)
	}
	if cal.Zones[0].HarvestFrom != "2026-09-29" || cal.Zones[0].HarvestBy != "2026-10-29" {
		t.Errorf("maize harvest %s to %s", cal.Zones[0].HarvestFrom, cal.Zones[0].HarvestBy)
	}
	for _, task := range cal.Zones[1].Tasks {
		if task.Kind == calendar.Fertilise && task.Title != "No urea needed" {
			t.Errorf("green gram task %+v: pulses get no nitrogen top-dressing", task)
		}
	}
	for _, z := range cal.Zones {
		if !slices.IsSortedFunc(z.Tasks, func(a, b calendar.Task) int { return a.DAS - b.DAS }) {
			t.Errorf("%s tasks out of order", z.Crop)
		}
	}
	dry, _ := calendar.ForPlan(l, plan, false)
	if k := kinds(dry.Zones[0])[calendar.Irrigate]; len(k) != 1 {
		t.Errorf("rainfed maize irrigation tasks %v, want one (save moisture at flowering)", k)
	}
	due := calendar.Due(cal, date("2026-07-26"), date("2026-08-01"))
	if len(due) == 0 || due[0].Date < "2026-07-26" || due[len(due)-1].Date > "2026-08-01" {
		t.Errorf("due %+v: want the week's tasks only", due)
	}
	if _, err := calendar.ForPlan(l, agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "July"}}}, true); err == nil {
		t.Error("a bad sowing date passed")
	}
}

// TestPerennialCalendar: a tree gets its pits, planting, watering while it
// takes, and the year's round for two years.
func TestPerennialCalendar(t *testing.T) {
	t.Parallel()
	cal, err := calendar.ForPlan(lib(t), agro.Plan{Zones: []agro.Zone{{Crop: "mango", Sow: "2026-07-15"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	k := kinds(cal.Zones[0])
	if k[calendar.Prepare][0] != -30 || k[calendar.Sow][0] != 0 || len(k[calendar.Prune]) != 2 || len(k[calendar.Fertilise]) != 4 {
		t.Errorf("mango tasks %v", k)
	}
	if len(k[calendar.Reap]) != 0 {
		t.Errorf("mango (four years to fruit) has a harvest in its first two years: %v", k[calendar.Reap])
	}
}

// Keys are unique within a zone and do not change with the sowing date, so
// work recorded against a task survives the dates moving.
func TestKeysUniqueAndStable(t *testing.T) {
	t.Parallel()
	l := lib(t)
	for _, c := range l.Crops() {
		var keys [2][]string
		for i, sow := range []string{"2026-06-20", "2026-07-15"} {
			z, err := calendar.ForZone(l, 0, agro.Zone{Crop: c.ID, Sow: sow}, true)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, k := range z.Tasks {
				if k.Key == "" || seen[k.Key] {
					t.Fatalf("%s: key %q empty or repeated", c.ID, k.Key)
				}
				seen[k.Key] = true
				keys[i] = append(keys[i], k.Key)
			}
		}
		slices.Sort(keys[0])
		slices.Sort(keys[1])
		if !slices.Equal(keys[0], keys[1]) {
			t.Errorf("%s: keys change with the sowing date:\n%v\n%v", c.ID, keys[0], keys[1])
		}
	}
}
