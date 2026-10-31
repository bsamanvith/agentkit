package agro_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
)

var today = time.Date(2026, 10, 31, 9, 30, 0, 0, time.UTC)

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(agro.DateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func soil(t *testing.T, l *agro.Library, id string) *agro.Soil {
	t.Helper()
	s, err := l.Soil(id)
	if err != nil {
		t.Fatal(err)
	}
	return &s
}

func recommend(t *testing.T, l *agro.Library, r agro.Request) agro.Result {
	t.Helper()
	if r.Climate == (agro.Climate{}) {
		r.Climate = agrotest.Hyderabad()
	}
	if r.Today.IsZero() {
		r.Today = today
	}
	res, err := agro.Recommend(l, r)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func option(t *testing.T, res agro.Result, crop string) agro.Option {
	t.Helper()
	for _, o := range res.Options {
		if o.Crop == crop {
			return o
		}
	}
	t.Fatalf("no option for %s", crop)
	return agro.Option{}
}

func ids(notes []agro.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.ID
	}
	return out
}

// The Deccan's black soil, rainfed: chickpea now on the moisture the
// monsoon left (rabi), the kharif crops of black soil when the rains come.
func TestBlackSoilRainfedAtHyderabad(t *testing.T) {
	t.Parallel()
	l := library(t)
	res := recommend(t, l, agro.Request{Soil: soil(t, l, "black")})
	chickpea := option(t, res, "chickpea")
	if chickpea.Score < agro.Good || chickpea.Sow > "2026-11-30" {
		t.Errorf("chickpea: score %d sowing %s, want good this rabi", chickpea.Score, chickpea.Sow)
	}
	for _, c := range []string{"cotton", "soybean", "pigeon_pea", "sorghum"} {
		o := option(t, res, c)
		if o.Next == nil || o.Next.Best < "2027-05-15" || o.Next.Best > "2027-07-31" || o.Season.Season != agro.Kharif {
			t.Errorf("%s: next window %+v, season %s: want kharif 2027", c, o.Next, o.Season.Season)
		}
	}
	// Rice needs more water than the rain gives without irrigation.
	rice := option(t, res, "rice")
	if !slices.Contains(ids(rice.Risks), "water.short") {
		t.Errorf("rainfed rice: risks %v, want water.short", ids(rice.Risks))
	}
	if !slices.Contains(ids(res.Notes), "water.rainfed") {
		t.Errorf("notes %v, want water.rainfed", ids(res.Notes))
	}
}

func TestResultShape(t *testing.T) {
	t.Parallel()
	l := library(t)
	res := recommend(t, l, agro.Request{Soil: soil(t, l, "red_loam")})
	if len(res.Options) != len(l.Crops()) || res.Today != "2026-10-31" {
		t.Fatalf("%d options for %d crops, today %s", len(res.Options), len(l.Crops()), res.Today)
	}
	for i, o := range res.Options {
		if o.Score < 0 || o.Score > 100 {
			t.Errorf("%s: score %d", o.Crop, o.Score)
		}
		if i > 0 && o.Score > res.Options[i-1].Score {
			t.Errorf("%s (%d) after %s (%d): not best first", o.Crop, o.Score, res.Options[i-1].Crop, res.Options[i-1].Score)
		}
		if o.Sow < res.Today || o.Harvest <= o.Sow {
			t.Errorf("%s: sow %s harvest %s, today %s", o.Crop, o.Sow, o.Harvest, res.Today)
		}
		for _, w := range []*agro.Window{o.Next, o.Window} {
			if w != nil && (w.From > w.Best || w.Best > w.To || w.Score < agro.Good || w.From < res.Today) {
				t.Errorf("%s: window %+v", o.Crop, *w)
			}
		}
		if o.Window != nil && (o.Sow < o.Window.From || o.Sow > o.Window.To) {
			t.Errorf("%s: sowing %s outside its window %+v", o.Crop, o.Sow, *o.Window)
		}
		if o.Analog != slices.Contains(ids(o.Risks), "analog") {
			t.Errorf("%s: analog %v but risks %v", o.Crop, o.Analog, ids(o.Risks))
		}
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	l := library(t)
	r := agro.Request{Soil: soil(t, l, "alluvial_loam"), Profile: agro.Profile{Water: []agro.Water{{Kind: agro.Canal}}}, Around: date(t, "2027-06-20")}
	if a, b := recommend(t, l, r), recommend(t, l, r); !reflect.DeepEqual(a, b) {
		t.Error("the same request gave two answers")
	}
}

// "Around" is a range, not a day: every option in it sows within the
// spread, and options in range come first.
func TestAround(t *testing.T) {
	t.Parallel()
	l := library(t)
	res := recommend(t, l, agro.Request{Soil: soil(t, l, "black"), Around: date(t, "2027-06-20"), Spread: 20})
	if res.From != "2027-05-31" || res.To != "2027-07-10" {
		t.Errorf("range %s to %s, want 2027-05-31 to 2027-07-10", res.From, res.To)
	}
	in := true
	for _, o := range res.Options {
		if o.InRange && !in {
			t.Errorf("%s in range after one out of it", o.Crop)
		}
		in = o.InRange
		if o.InRange && (o.Sow < res.From || o.Sow > res.To) {
			t.Errorf("%s: sowing %s outside the range", o.Crop, o.Sow)
		}
	}
	// A range that starts in the past starts today.
	res = recommend(t, l, agro.Request{Around: date(t, "2026-10-20")})
	if res.From != "2026-10-31" {
		t.Errorf("range from %s, want today", res.From)
	}
}

// Water never hurts a crop's score: adding a reliable source and drip to a
// plot only raises scores.
func TestIrrigationNeverLowersAScore(t *testing.T) {
	t.Parallel()
	l := library(t)
	c := agrotest.Hyderabad()
	s := soil(t, l, "red_sandy")
	wet := agro.Profile{Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}, Drip: true}
	for _, crop := range l.Crops() {
		for d := 0; d < 365; d += 30 {
			sow := today.AddDate(0, 0, d)
			dry := agro.Simulate(crop, agro.Request{Climate: c, Soil: s, Today: today}, sow)
			irrigated := agro.Simulate(crop, agro.Request{Climate: c, Soil: s, Profile: wet, Today: today}, sow)
			if irrigated.Score < dry.Score {
				t.Errorf("%s sown %s: %d rainfed, %d irrigated", crop.ID, dry.Sow, dry.Score, irrigated.Score)
			}
			if irrigated.Water.ShortMM > dry.Water.ShortMM {
				t.Errorf("%s sown %s: shorter of water with irrigation", crop.ID, dry.Sow)
			}
		}
	}
}

// A cool, wet place: wheat and potato fit, rice and pearl millet are too
// cold, and frost-tender crops are at risk when nights freeze.
func TestCoolClimate(t *testing.T) {
	t.Parallel()
	l := library(t)
	var cool agro.Climate
	for i := range cool.Months {
		cool.Months[i] = agro.Month{Temp: 17, TempMax: 23, TempMin: 1, RainMM: 70, Humidity: 70}
	}
	res := recommend(t, l, agro.Request{Climate: cool, Soil: soil(t, l, "alluvial_loam")})
	wheat, potato, rice, millet := option(t, res, "wheat"), option(t, res, "potato"), option(t, res, "rice"), option(t, res, "pearl_millet")
	if wheat.Score < agro.Good || potato.Score <= rice.Score || millet.Score >= wheat.Score {
		t.Errorf("wheat %d, potato %d, rice %d, pearl millet %d", wheat.Score, potato.Score, rice.Score, millet.Score)
	}
	if !slices.Contains(ids(millet.Risks), "temp.cold") || !slices.Contains(ids(rice.Risks), "frost") {
		t.Errorf("pearl millet risks %v, rice risks %v: want temp.cold and frost", ids(millet.Risks), ids(rice.Risks))
	}
	if slices.Contains(ids(wheat.Risks), "frost") {
		t.Error("wheat stands frost")
	}
}

func TestSoilRules(t *testing.T) {
	t.Parallel()
	l := library(t)
	irrigated := agro.Profile{Water: []agro.Water{{Kind: agro.Canal, Reliable: true}}}
	salty := recommend(t, l, agro.Request{Soil: soil(t, l, "saline"), Profile: irrigated})
	if !slices.Contains(ids(option(t, salty, "chickpea").Risks), "soil.salt") ||
		slices.Contains(ids(option(t, salty, "mustard").Risks), "soil.salt") {
		t.Error("salt: want a risk for chickpea and none for mustard, which stands it")
	}
	// Salt alone: the same soil, salty or not.
	plain := soil(t, l, "alluvial_loam")
	briny := *plain
	briny.Salty = true
	fresh := recommend(t, l, agro.Request{Soil: plain, Profile: irrigated, Crops: []string{"chickpea", "mustard"}})
	brine := recommend(t, l, agro.Request{Soil: &briny, Profile: irrigated, Crops: []string{"chickpea", "mustard"}})
	if option(t, brine, "chickpea").Score >= option(t, fresh, "chickpea").Score || option(t, brine, "mustard").Score != option(t, fresh, "mustard").Score {
		t.Error("salt: want a lower score for chickpea and the same for mustard")
	}
	acid := recommend(t, l, agro.Request{Profile: agro.Profile{PH: 4.5}, Crops: []string{"chickpea"}})
	neutral := recommend(t, l, agro.Request{Profile: agro.Profile{PH: 7}, Crops: []string{"chickpea"}})
	if !slices.Contains(ids(acid.Options[0].Risks), "soil.ph") || acid.Options[0].Score >= neutral.Options[0].Score {
		t.Errorf("pH 4.5: score %d risks %v; pH 7: score %d", acid.Options[0].Score, ids(acid.Options[0].Risks), neutral.Options[0].Score)
	}
	unknown := recommend(t, l, agro.Request{})
	if !slices.Contains(ids(unknown.Notes), "soil.unknown") {
		t.Errorf("notes %v, want soil.unknown", ids(unknown.Notes))
	}
	// Sand does not suit rice: a texture risk.
	sand := recommend(t, l, agro.Request{Soil: soil(t, l, "desert"), Profile: irrigated, Crops: []string{"rice"}})
	if !slices.Contains(ids(sand.Options[0].Risks), "soil.texture") {
		t.Errorf("rice on sand: risks %v", ids(sand.Options[0].Risks))
	}
}

func TestOnlyTheCropsAskedFor(t *testing.T) {
	t.Parallel()
	l := library(t)
	res := recommend(t, l, agro.Request{Crops: []string{"mango", "dragon_fruit"}})
	if len(res.Options) != 2 {
		t.Fatalf("%d options, want 2", len(res.Options))
	}
}

func TestRecommendRefuses(t *testing.T) {
	t.Parallel()
	l := library(t)
	bad := agrotest.Hyderabad()
	bad.Months[3].RainMM = -5
	for name, tc := range map[string]struct {
		r    agro.Request
		want error
	}{
		"unknown crop":   {agro.Request{Climate: agrotest.Hyderabad(), Today: today, Crops: []string{"unobtainium"}}, agro.ErrUnknown},
		"past":           {agro.Request{Climate: agrotest.Hyderabad(), Today: today, Around: today.AddDate(0, -3, 0)}, agro.ErrInvalid},
		"spread":         {agro.Request{Climate: agrotest.Hyderabad(), Today: today, Around: today, Spread: 400}, agro.ErrInvalid},
		"no today":       {agro.Request{Climate: agrotest.Hyderabad()}, agro.ErrInvalid},
		"broken climate": {agro.Request{Climate: bad, Today: today}, agro.ErrInvalid},
	} {
		if _, err := agro.Recommend(l, tc.r); !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", name, err, tc.want)
		}
	}
}

func TestSeasonOf(t *testing.T) {
	t.Parallel()
	for d, want := range map[string]string{"2027-06-01": agro.Kharif, "2027-09-30": agro.Kharif, "2027-10-01": agro.Rabi, "2027-02-28": agro.Rabi, "2027-03-01": agro.Zaid, "2027-05-31": agro.Zaid} {
		if got := agro.SeasonOf(date(t, d)); got != want {
			t.Errorf("%s: %s, want %s", d, got, want)
		}
	}
}
