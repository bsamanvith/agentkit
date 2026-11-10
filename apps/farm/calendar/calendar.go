// Package calendar turns a season's plan into a crop calendar: each crop's
// stage on a day, and the work it needs week by week (prepare, sow, weed,
// fertilise, irrigate, scout, harvest), from its sowing date and days to
// harvest.
//
// The timings are general rules by kind of crop, as fractions of the
// crop's season (top-dress a cereal's nitrogen at about a quarter and half
// of it, weed at a fifth and two fifths, keep water up at flowering and
// grain fill). They follow the shape of the ICAR and state agricultural
// university packages of practice, not their numbers for a particular
// variety: starter values to review with an agronomist, like the crop
// library's.
package calendar

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
)

// Stage names, in order through an annual crop's season.
const (
	NotSown       = "not_sown"
	Establishment = "establishment" // up to ~15 % of the season
	Vegetative    = "vegetative"    // to ~45 %
	Flowering     = "flowering"     // to ~70 %: flowering and grain, pod or fruit set
	Maturing      = "maturing"      // to the first harvest day
	Harvest       = "harvest"       // the harvest window, and two weeks after it
	Harvested     = "harvested"     // gone from the field
	Growing       = "growing"       // a perennial after its first two months
)

// Stage is where a crop is on a day.
type Stage struct {
	Name string `json:"name"`
	// DAS is days after sowing (or planting); negative before it.
	DAS int `json:"das"`
	// InField is true from sowing until two weeks after the harvest
	// window closes; always after planting for a perennial.
	InField bool `json:"in_field"`
}

// season is a crop's usual days to (first) harvest.
func season(c agro.Crop) float64 { return (c.Days[0] + c.Days[1]) / 2 }

// StageAt returns the stage of crop c sown on sow, on day (both dates in
// the farmer's calendar; times of day are ignored).
func StageAt(c agro.Crop, sow, day time.Time) Stage {
	das := days(sow, day)
	s := Stage{DAS: das}
	switch {
	case das < 0:
		s.Name = NotSown
		return s
	case c.Perennial:
		s.InField = true
		s.Name = Growing
		if das < 60 {
			s.Name = Establishment
		}
		return s
	}
	d := float64(das)
	n := season(c)
	s.InField = true
	switch {
	case d < 0.15*n:
		s.Name = Establishment
	case d < 0.45*n:
		s.Name = Vegetative
	case d < 0.7*n:
		s.Name = Flowering
	case d < c.Days[0]:
		s.Name = Maturing
	case d <= c.Days[1]+14:
		s.Name = Harvest
	default:
		s.Name, s.InField = Harvested, false
	}
	return s
}

func days(from, to time.Time) int {
	a := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(math.Round(b.Sub(a).Hours() / 24))
}

// Task kinds.
const (
	Prepare   = "prepare"
	Sow       = "sow"
	Weed      = "weed"
	Fertilise = "fertilise"
	Irrigate  = "irrigate"
	Scout     = "scout"
	Protect   = "protect"
	Prune     = "prune"
	Reap      = "harvest"
)

// Task is one piece of work on a day.
type Task struct {
	// Key names the rule that made the task, unique within a zone's
	// calendar and the same whatever the sowing date ("weed-1",
	// "harvest-by", "y2-fertilise-jun"), so that work done on a task can
	// be kept against it when the dates move.
	Key   string `json:"key"`
	Date  string `json:"date"` // YYYY-MM-DD
	DAS   int    `json:"das"`
	Stage string `json:"stage"`
	Kind  string `json:"kind"`
	Crop  string `json:"crop"`
	// Zone is the plan's zone index.
	Zone   int    `json:"zone"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// Zone is one zone's calendar.
type Zone struct {
	Zone     int    `json:"zone"`
	Crop     string `json:"crop"`
	CropName string `json:"crop_name"`
	With     string `json:"with,omitempty"`
	Sow      string `json:"sow"`
	// HarvestFrom and HarvestBy are the harvest window of Crop (the
	// first harvest of a perennial).
	HarvestFrom string `json:"harvest_from"`
	HarvestBy   string `json:"harvest_by"`
	Tasks       []Task `json:"tasks"`
}

// Calendar is a plan's calendar, zone by zone.
type Calendar struct {
	Zones []Zone   `json:"zones"`
	Notes []string `json:"notes,omitempty"`
}

// Note says where the timings come from.
const Note = "Timings are general rules by kind of crop (starter values to review with an agronomist): check your seed packet or the local package of practices for your variety."

// ForPlan returns the calendar of a plan: every task of every zone, from a
// few weeks before sowing to the harvest (a perennial: its first two
// years). Irrigated is whether the plot has water to irrigate with.
func ForPlan(lib *agro.Library, plan agro.Plan, irrigated bool) (Calendar, error) {
	cal := Calendar{Notes: []string{Note}}
	for i, z := range plan.Zones {
		zc, err := ForZone(lib, i, z, irrigated)
		if err != nil {
			return Calendar{}, err
		}
		cal.Zones = append(cal.Zones, zc)
	}
	return cal, nil
}

// ForZone returns one zone's calendar.
func ForZone(lib *agro.Library, i int, z agro.Zone, irrigated bool) (Zone, error) {
	c, err := lib.Crop(z.Crop)
	if err != nil {
		return Zone{}, err
	}
	sow, err := time.Parse(agro.DateLayout, z.Sow)
	if err != nil {
		return Zone{}, fmt.Errorf("%w: zone %d sowing date %q: %w", agro.ErrInvalid, i, z.Sow, err)
	}
	out := Zone{
		Zone: i, Crop: c.ID, CropName: c.Name, With: z.With, Sow: z.Sow,
		HarvestFrom: sow.AddDate(0, 0, int(c.Days[0])).Format(agro.DateLayout),
		HarvestBy:   sow.AddDate(0, 0, int(c.Days[1])).Format(agro.DateLayout),
	}
	out.Tasks = tasks(c, sow, i, irrigated)
	if z.With != "" {
		w, err := lib.Crop(z.With)
		if err != nil {
			return Zone{}, err
		}
		for _, t := range tasks(w, sow, i, irrigated) {
			// The intercrop shares the ground work; add what is its own.
			if t.Kind == Reap || t.Kind == Fertilise {
				t.Key = "with-" + t.Key
				t.Title = w.Name + ": " + t.Title
				out.Tasks = append(out.Tasks, t)
			}
		}
	}
	slices.SortStableFunc(out.Tasks, func(a, b Task) int { return a.DAS - b.DAS })
	return out, nil
}

// Due returns the tasks of cal dated from from to to, both included, in
// date order.
func Due(cal Calendar, from, to time.Time) []Task {
	f, t := from.Format(agro.DateLayout), to.Format(agro.DateLayout)
	var out []Task
	for _, z := range cal.Zones {
		for _, k := range z.Tasks {
			if k.Date >= f && k.Date <= t {
				out = append(out, k)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Task) int {
		if a.Date != b.Date {
			if a.Date < b.Date {
				return -1
			}
			return 1
		}
		return a.Zone - b.Zone
	})
	return out
}

type rule struct {
	key    string
	at     float64 // fraction of the season; when 0, days is used
	days   int     // when at is 0: days after sowing (negative: before)
	kind   string
	title  string
	detail string
}

// nitrogenKinds get their nitrogen in splits; pulses fix their own.
var nitrogenKinds = map[string]bool{"cereal": true, "millet": true, "vegetable": true, "fibre": true, "cash": true, "spice": true}

func tasks(c agro.Crop, sow time.Time, zone int, irrigated bool) []Task {
	if c.Perennial {
		return perennial(c, sow, zone, irrigated)
	}
	n := season(c)
	var rs []rule
	add := func(key string, at float64, kind, title, detail string) {
		rs = append(rs, rule{key: key, at: at, kind: kind, title: title, detail: detail})
	}
	addDay := func(key string, d int, kind, title, detail string) {
		rs = append(rs, rule{key: key, days: d, kind: kind, title: title, detail: detail})
	}
	addDay("plough", -21, Prepare, "Plough and spread well-rotted manure", "Two ploughings; farmyard manure or compost worked in, with the basal fertiliser your soil test asks for.")
	seed := "Treat seed with a fungicide (or Trichoderma) a day before sowing."
	if c.Kind == "pulse" {
		seed = "Treat seed with a fungicide, then with Rhizobium culture (in that order) on the day before sowing."
	}
	addDay("treat-seed", -1, Prepare, "Treat the seed", seed)
	addDay("sow", 0, Sow, "Sow "+c.Name, fmt.Sprintf("Rows %.0f cm apart, plants %.0f cm apart, into moist soil.", c.RowCM, c.PlantCM))
	addDay("fill-gaps", max(7, int(0.1*n)), Scout, "Fill the gaps", "Where seeds did not come up, sow again or move seedlings in.")
	add("weed-1", 0.2, Weed, "First weeding", "Hoe or hand-weed while weeds are small; the crop's first weeks decide the yield.")
	if n >= 75 {
		add("weed-2", 0.4, Weed, "Second weeding", "Before the crop closes over the rows.")
	}
	switch {
	case nitrogenKinds[c.Kind]:
		add("nitrogen-1", 0.25, Fertilise, "First top-dressing of nitrogen", "Put urea (or the nitrogen your soil test asks for) beside the rows into moist soil, not before heavy rain.")
		add("nitrogen-2", 0.5, Fertilise, "Second top-dressing of nitrogen", "At the start of flowering or panicle initiation, into moist soil.")
	case c.Kind == "oilseed":
		add("nitrogen-sulphur", 0.3, Fertilise, "Top-dress nitrogen (and sulphur)", "Oilseeds need sulphur for oil: gypsum or a sulphur-bearing fertiliser.")
	case c.Kind == "pulse":
		add("pulse-nutrition", 0.3, Fertilise, "No urea needed", "Pulses fix their own nitrogen; a 2 % DAP spray at flowering helps pod set.")
	}
	for i, f := 1, 0.15; f < 0.86; i, f = i+1, f+0.14 {
		add(fmt.Sprintf("scout-%d", i), f, Scout, "Walk the field for pests and disease", "Look under leaves and at the growing tips; act early on what you find.")
	}
	if irrigated {
		add("water-flowering", 0.5, Irrigate, "Keep water up at flowering", "The most critical irrigation: do not let the soil dry out now.")
		add("water-filling", 0.7, Irrigate, "Keep water up while the grain, pods or fruit fill", "A dry spell now lowers the yield.")
		if c.Kind != "vegetable" {
			add("water-stop", 0.9, Irrigate, "Stop irrigating about ten days before harvest", "The crop dries down and harvests clean.")
		}
	} else {
		add("moisture-flowering", 0.5, Irrigate, "Flowering: save the soil's moisture", "Rainfed: mulch between rows and hoe lightly to keep moisture in; a protective irrigation now pays most if you can arrange one.")
	}
	rs = append(rs, rule{key: "harvest-from", days: int(c.Days[0]), kind: Reap, title: "Harvest window opens", detail: "Harvest when the crop is mature; check the grain, pods or fruit."})
	rs = append(rs, rule{key: "harvest-by", days: int(c.Days[1]), kind: Reap, title: "Harvest by now", detail: "Later and the crop loses yield in the field."})
	out := make([]Task, 0, len(rs))
	for _, r := range rs {
		d := r.days
		if r.at > 0 {
			d = int(math.Round(r.at * n))
		}
		day := sow.AddDate(0, 0, d)
		out = append(out, Task{Key: r.key, Date: day.Format(agro.DateLayout), DAS: d, Stage: StageAt(c, sow, day).Name, Kind: r.kind, Crop: c.ID, Zone: zone, Title: r.title, Detail: r.detail})
	}
	return out
}

// perennial is a tree or a long-lived crop's first two years from
// planting: weekly watering while it takes, then the year's round.
func perennial(c agro.Crop, sow time.Time, zone int, irrigated bool) []Task {
	var out []Task
	at := func(key string, day time.Time, kind, title, detail string) {
		out = append(out, Task{Key: key, Date: day.Format(agro.DateLayout), DAS: days(sow, day), Stage: StageAt(c, sow, day).Name, Kind: kind, Crop: c.ID, Zone: zone, Title: title, Detail: detail})
	}
	at("dig-pits", sow.AddDate(0, 0, -30), Prepare, "Dig the pits", "Pits a month ahead, filled with topsoil, manure and neem cake.")
	at("sow", sow, Sow, "Plant "+c.Name, fmt.Sprintf("%.0f m between rows, %.0f m between plants; stake and water at once.", c.RowCM/100, c.PlantCM/100))
	at("water-establish", sow.AddDate(0, 0, 3), Irrigate, "Water every two or three days for the first month", "Until new leaves show the plant has taken.")
	at("fill-gaps", sow.AddDate(0, 0, 30), Scout, "Replace plants that died", "Gap-fill in the first month or two.")
	end := sow.AddDate(2, 0, 0)
	for y := sow.Year(); y <= end.Year(); y++ {
		for _, m := range []struct {
			key                 string
			month               time.Month
			kind, title, detail string
		}{
			{"fertilise-1", time.June, Fertilise, "Manure and the first fertiliser split", "At the start of the rains, in a ring around the trunk under the canopy's edge."},
			{"fertilise-2", time.September, Fertilise, "Second fertiliser split", "While the soil is still moist."},
			{"prune", time.December, Prune, "Prune dead, diseased and crossing branches", "After the rains; paint large cuts."},
			{"summer-water", time.March, Irrigate, "Summer watering and mulch", "Mulch the basins; water weekly (more on sandy soil) until the rains."},
		} {
			d := time.Date(y, m.month, 1, 0, 0, 0, 0, time.UTC)
			if d.Before(sow.AddDate(0, 0, 45)) || d.After(end) {
				continue
			}
			if m.kind == Irrigate && !irrigated {
				m.detail = "Mulch the basins thickly; water young trees by hand or pot irrigation through the summer."
			}
			// The year counts from planting, so the key stays when the
			// planting date moves within its year.
			at(fmt.Sprintf("y%d-%s", d.Year()-sow.Year()+1, m.key), d, m.kind, m.title, m.detail)
		}
	}
	if first := sow.AddDate(0, 0, int(c.Days[0])); first.Before(end) {
		at("harvest-from", first, Reap, "First harvest may begin", "Young trees: thin the fruit so the tree keeps growing.")
	}
	slices.SortStableFunc(out, func(a, b Task) int { return a.DAS - b.DAS })
	return out
}
