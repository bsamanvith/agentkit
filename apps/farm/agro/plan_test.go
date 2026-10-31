package agro_test

import (
	"math"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// box is a rectangle with its south-west corner at lat, lng, about w metres
// east-west and h metres north-south.
func box(lat, lng, w, h float64) geo.Ring {
	dlat := h / 110_600
	dlng := w / (111_320 * math.Cos(lat*math.Pi/180))
	return geo.Ring{{Lat: lat, Lng: lng}, {Lat: lat, Lng: lng + dlng}, {Lat: lat + dlat, Lng: lng + dlng}, {Lat: lat + dlat, Lng: lng}}
}

// plot is a 200 m by 100 m plot of black soil at Farooqnagar, two hectares,
// with a reliable borewell.
func plot(t *testing.T, l *agro.Library) agro.Plot {
	t.Helper()
	c := agrotest.Hyderabad()
	return agro.Plot{
		Ring: box(17.07, 78.20, 200, 100), Climate: &c, Soil: soil(t, l, "black"),
		Profile: agro.Profile{Soil: "black", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}},
	}
}

func checkIDs(rep agro.PlanReport, zone int) []string {
	var out []string
	for _, c := range rep.Checks {
		if c.Zone == zone || c.Other == zone {
			out = append(out, c.ID)
		}
	}
	return out
}

func TestLayoutAndCheck(t *testing.T) {
	t.Parallel()
	l := library(t)
	p := plot(t, l)
	plan, err := agro.Layout(l, p.Ring, []agro.Part{
		{Crop: "cotton", With: "green_gram", Share: 0.6, Sow: "2027-06-10"},
		{Crop: "soybean", Share: 0.4, Sow: "2027-06-20"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Zones) != 2 || plan.Zones[0].Pattern != [2]int{2, 1} || plan.Zones[1].RowBearing != 0 {
		t.Fatalf("zones %+v", plan.Zones)
	}
	rep := agro.CheckPlan(l, p, plan, today)
	if rep.Status == agro.PlanInvalid {
		t.Fatalf("a laid-out plan is invalid: %+v", rep.Checks)
	}
	if math.Abs(rep.UsedM2-rep.PlotM2) > 0.01*rep.PlotM2 || math.Abs(rep.Zones[0].Share-0.6) > 0.01 {
		t.Errorf("used %.0f of %.0f m²; zone 1 share %v", rep.UsedM2, rep.PlotM2, rep.Zones[0].Share)
	}
	if !slices.Contains(checkIDs(rep, 0), "intercrop.good") || !slices.Contains(checkIDs(rep, 0), "sow.good") {
		t.Errorf("zone 1 checks %v, want intercrop.good and sow.good", checkIDs(rep, 0))
	}
	// Soybean, 45 × 5 cm, on 0.8 ha: 8000 / (0.45 × 0.05) ≈ 355,000 plants.
	z := rep.Zones[1]
	soy, _ := l.Crop("soybean")
	if want := z.AreaM2 / (soy.RowCM / 100 * soy.PlantCM / 100); math.Abs(float64(z.Plants)-want) > 1 {
		t.Errorf("soybean plants %d, want %.0f", z.Plants, want)
	}
	// Rows run north to south across a strip ~40 m deep and 200 m wide.
	if z.Rows < 400 || z.Season == nil {
		t.Errorf("soybean: %d rows over %.0f m, season %v", z.Rows, z.WidthM, z.Season)
	}
	// Two thirds of the cotton zone's rows are cotton, a third green gram.
	cotton, _ := l.Crop("cotton")
	if a := rep.Zones[0]; math.Abs(float64(a.Plants)-a.AreaM2*2/3/(cotton.RowCM/100*cotton.PlantCM/100)) > 1 || a.WithPlants == 0 {
		t.Errorf("intercrop counts %d and %d, want two thirds of the zone in cotton", a.Plants, a.WithPlants)
	}
}

func TestCheckPlanFindsMistakes(t *testing.T) {
	t.Parallel()
	l := library(t)
	p := plot(t, l)
	south := box(17.07, 78.20, 200, 50)
	north := box(17.07+50/110_600.0, 78.20, 200, 50)
	zone := func(crop string, ring geo.Ring, edit ...func(*agro.Zone)) agro.Zone {
		z := agro.Zone{Crop: crop, Ring: ring, Sow: "2027-06-15"}
		for _, e := range edit {
			e(&z)
		}
		return z
	}
	tests := map[string]struct {
		plan    agro.Plan
		profile *agro.Profile
		zone    int
		want    string
		invalid bool
	}{
		"empty":          {plan: agro.Plan{}, zone: -1, want: "plan.empty", invalid: true},
		"unknown crop":   {plan: agro.Plan{Zones: []agro.Zone{zone("unobtainium", south)}}, want: "zone.crop", invalid: true},
		"outside":        {plan: agro.Plan{Zones: []agro.Zone{zone("soybean", box(17.07, 78.20, 300, 50))}}, want: "zone.outside", invalid: true},
		"overlap":        {plan: agro.Plan{Zones: []agro.Zone{zone("soybean", south), zone("cotton", box(17.07, 78.20, 100, 50))}}, want: "zone.overlap", invalid: true},
		"crossing edges": {plan: agro.Plan{Zones: []agro.Zone{zone("soybean", geo.Ring{south[0], south[2], south[1], south[3]})}}, want: "zone.shape", invalid: true},
		"bad date":       {plan: agro.Plan{Zones: []agro.Zone{zone("soybean", south, func(z *agro.Zone) { z.Sow = "June" })}}, want: "zone.sow", invalid: true},
		"no pattern": {
			plan: agro.Plan{Zones: []agro.Zone{zone("maize", south, func(z *agro.Zone) { z.With = "soybean" })}},
			want: "intercrop.pattern", invalid: true,
		},
		"cramped":        {plan: agro.Plan{Zones: []agro.Zone{zone("cotton", south, func(z *agro.Zone) { z.RowCM = 20 })}}, want: "spacing.row"},
		"sparse":         {plan: agro.Plan{Zones: []agro.Zone{zone("cotton", south, func(z *agro.Zone) { z.PlantCM = 200 })}}, want: "spacing.plant"},
		"tiny":           {plan: agro.Plan{Zones: []agro.Zone{zone("chilli", box(17.07, 78.20, 3, 3))}}, want: "zone.small"},
		"hot sowing":     {plan: agro.Plan{Zones: []agro.Zone{zone("wheat", south, func(z *agro.Zone) { z.Sow = "2027-04-01" })}}, want: "sow.poor"},
		"strange pair":   {plan: agro.Plan{Zones: []agro.Zone{zone("rice", south, func(z *agro.Zone) { z.With, z.Pattern = "cotton", [2]int{1, 1} })}}, want: "intercrop.unknown"},
		"one family":     {plan: agro.Plan{Zones: []agro.Zone{zone("sorghum", south, func(z *agro.Zone) { z.With, z.Pattern = "maize", [2]int{1, 1} })}}, want: "intercrop.family"},
		"rows east-west": {plan: agro.Plan{Zones: []agro.Zone{zone("maize", south, func(z *agro.Zone) { z.With, z.Pattern, z.RowBearing = "soybean", [2]int{1, 4}, 90 })}}, want: "rows.direction"},
		"same family": {
			plan: agro.Plan{Zones: []agro.Zone{zone("soybean", south)}}, profile: &agro.Profile{Soil: "black", PreviousCrop: "green_gram"},
			want: "rotation.same",
		},
		"pulse after cereal": {
			plan: agro.Plan{Zones: []agro.Zone{zone("pigeon_pea", south)}}, profile: &agro.Profile{Soil: "black", PreviousCrop: "sorghum"},
			want: "rotation.pulse",
		},
		// In the northern hemisphere tall maize to the south of short
		// groundnut shades it.
		"shade": {plan: agro.Plan{Zones: []agro.Zone{zone("maize", south), zone("groundnut", north)}}, want: "shade.tall"},
		"two seasons": {
			plan: agro.Plan{Zones: []agro.Zone{zone("chickpea", south, func(z *agro.Zone) { z.Sow = "2026-11-01" }), zone("groundnut", north)}},
			zone: -1, want: "plan.seasons",
		},
		"waterlogging": {
			plan:    agro.Plan{Zones: []agro.Zone{zone("onion", south, func(z *agro.Zone) { z.Sow = "2027-06-01" })}},
			profile: &agro.Profile{Soil: "peaty"}, want: "waterlogging",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pl := p
			if tc.profile != nil {
				pl.Profile = *tc.profile
				pl.Soil = soil(t, l, tc.profile.Soil)
			}
			if name == "waterlogging" {
				// A wet coast: 400 mm in the wettest months.
				c := *pl.Climate
				for _, m := range []int{6, 7} {
					c.Months[m].RainMM = 400
				}
				pl.Climate = &c
			}
			rep := agro.CheckPlan(l, pl, tc.plan, today)
			if got := checkIDs(rep, tc.zone); !slices.Contains(got, tc.want) {
				t.Errorf("checks %v, want %s", got, tc.want)
			}
			if (rep.Status == agro.PlanInvalid) != tc.invalid {
				t.Errorf("status %s, invalid want %v", rep.Status, tc.invalid)
			}
		})
	}
}

func TestShadeOnlyFromTheSunnySide(t *testing.T) {
	t.Parallel()
	l := library(t)
	p := plot(t, l)
	south := box(17.07, 78.20, 200, 50)
	north := box(17.07+50/110_600.0, 78.20, 200, 50)
	plan := agro.Plan{Zones: []agro.Zone{
		{Crop: "groundnut", Ring: south, Sow: "2027-06-15"},
		{Crop: "maize", Ring: north, Sow: "2027-06-15"},
	}}
	if slices.Contains(checkIDs(agro.CheckPlan(l, p, plan, today), 0), "shade.tall") {
		t.Error("tall maize north of groundnut flagged for shade")
	}
}

func TestCheckPlanWithoutClimate(t *testing.T) {
	t.Parallel()
	l := library(t)
	p := plot(t, l)
	p.Climate = nil
	rep := agro.CheckPlan(l, p, agro.Plan{Zones: []agro.Zone{{Crop: "wheat", Ring: p.Ring, Sow: "2027-04-01"}}}, today)
	for _, c := range rep.Checks {
		if c.ID == "sow.poor" || c.ID == "sow.good" {
			t.Errorf("sowing checked without a climate: %s", c.ID)
		}
	}
	if rep.Zones[0].Season != nil {
		t.Error("a season without a climate")
	}
}

func TestLayoutRefuses(t *testing.T) {
	t.Parallel()
	l := library(t)
	p := plot(t, l)
	for name, parts := range map[string][]agro.Part{
		"unknown crop":   {{Crop: "unobtainium", Share: 1}},
		"unknown second": {{Crop: "maize", With: "unobtainium", Share: 1}},
		"too much":       {{Crop: "maize", Share: 0.7}, {Crop: "soybean", Share: 0.7}},
		"nothing":        nil,
	} {
		if _, err := agro.Layout(l, p.Ring, parts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
