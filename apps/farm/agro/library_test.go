package agro_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
)

func library(t *testing.T) *agro.Library {
	t.Helper()
	l, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestDefaultLibrary(t *testing.T) {
	t.Parallel()
	l := library(t)
	if len(l.Soils()) < 10 || len(l.Crops()) < 30 {
		t.Fatalf("%d soils and %d crops, want at least 10 and 30", len(l.Soils()), len(l.Crops()))
	}
	if l.SoilsSource == "" || l.CropsSource == "" {
		t.Error("the library does not say where its values come from")
	}
	// Every soil has crops that suit its texture, every crop a Telugu and
	// Hindi name, and some crops are climate analogs from abroad.
	analogs := 0
	for _, s := range l.Soils() {
		n := 0
		for _, c := range l.Crops() {
			if c.Suits(s.Texture) {
				n++
			}
		}
		if n < 3 {
			t.Errorf("soil %s: only %d crops suit %s", s.ID, n, s.Texture)
		}
		if s.Names["te"] == "" || s.Signs == "" {
			t.Errorf("soil %s: no Telugu name or signs", s.ID)
		}
	}
	for _, c := range l.Crops() {
		if c.NameIn("te") == c.Name || c.NameIn("hi") == c.Name {
			t.Errorf("crop %s: no Telugu or Hindi name", c.ID)
		}
		if c.ClimateAnalog {
			analogs++
			if len(c.GrownIn) == 0 {
				t.Errorf("crop %s: an analog without where it is grown", c.ID)
			}
		}
	}
	if analogs < 3 {
		t.Errorf("%d climate analogs, want a few", analogs)
	}
	if _, err := l.Crop("rice"); err != nil {
		t.Error(err)
	}
	if _, err := l.Crop("unobtainium"); !errors.Is(err, agro.ErrUnknown) {
		t.Errorf("err %v, want ErrUnknown", err)
	}
	if _, err := l.Soil("moon_dust"); !errors.Is(err, agro.ErrUnknown) {
		t.Errorf("err %v, want ErrUnknown", err)
	}
}

func TestLoadRefusesInconsistentData(t *testing.T) {
	t.Parallel()
	soil := map[string]any{"id": "loam", "name": "Loam", "texture": "loam", "ph": []float64{6, 7}}
	crop := func(id string, edit func(map[string]any)) map[string]any {
		c := map[string]any{
			"id": id, "name": id, "family": "Poaceae", "kind": "cereal", "days": []float64{90, 120},
			"temp_opt": []float64{20, 30}, "temp_abs": []float64{10, 35}, "water_mm": []float64{400, 600},
			"ph": []float64{5.5, 7.5}, "textures": []string{"loam"}, "row_cm": 60, "plant_cm": 20,
			"seasons": []string{"kharif"},
		}
		if edit != nil {
			edit(c)
		}
		return c
	}
	load := func(soils []any, crops []any) error {
		s, _ := json.Marshal(map[string]any{"soils": soils})
		c, _ := json.Marshal(map[string]any{"crops": crops})
		_, err := agro.Load(s, c)
		return err
	}
	if err := load([]any{soil}, []any{crop("a", nil), crop("b", nil)}); err != nil {
		t.Fatalf("a good library: %v", err)
	}
	for name, crops := range map[string][]any{
		"repeated id":       {crop("a", nil), crop("a", nil)},
		"range reversed":    {crop("a", func(c map[string]any) { c["ph"] = []float64{8, 6} })},
		"opt outside abs":   {crop("a", func(c map[string]any) { c["temp_opt"] = []float64{5, 30} })},
		"unknown texture":   {crop("a", func(c map[string]any) { c["textures"] = []string{"mud"} })},
		"unknown season":    {crop("a", func(c map[string]any) { c["seasons"] = []string{"winter"} })},
		"unknown tolerance": {crop("a", func(c map[string]any) { c["tolerates"] = []string{"hail"} })},
		"no spacing":        {crop("a", func(c map[string]any) { c["row_cm"] = 0 })},
		"missing intercrop": {crop("a", func(c map[string]any) { c["intercrops"] = []string{"zz"} })},
		"one-way intercrop": {crop("a", func(c map[string]any) { c["intercrops"] = []string{"b"} }), crop("b", nil)},
	} {
		if err := load([]any{soil}, crops); !errors.Is(err, agro.ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", name, err)
		}
	}
	bad := map[string]any{"id": "x", "texture": "goo", "ph": []float64{6, 7}}
	if err := load([]any{bad}, []any{crop("a", nil)}); !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("a soil of unknown texture: err %v, want ErrInvalid", err)
	}
	if _, err := agro.Load([]byte("{"), []byte("{}")); !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("bad JSON: err %v, want ErrInvalid", err)
	}
}

func TestProfileCheck(t *testing.T) {
	t.Parallel()
	l := library(t)
	good := agro.Profile{Soil: "black", PH: 7.8, Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}, PreviousCrop: "cotton"}
	if err := good.Check(l); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]agro.Profile{
		"unknown soil":  {Soil: "moon_dust"},
		"pH off":        {PH: 14},
		"unknown water": {Water: []agro.Water{{Kind: "rain_dance"}}},
		"unknown crop":  {PreviousCrop: "unobtainium"},
	} {
		if err := p.Check(l); !errors.Is(err, agro.ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", name, err)
		}
	}
}

func TestIrrigation(t *testing.T) {
	t.Parallel()
	rainfed := agro.Profile{}
	well := agro.Profile{Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}}
	shaky := agro.Profile{Water: []agro.Water{{Kind: agro.Borewell}}}
	drip := agro.Profile{Water: well.Water, Drip: true}
	if !rainfed.Rainfed() || rainfed.IrrigationMM() != 0 || well.Rainfed() {
		t.Error("a plot without sources is rainfed, with none")
	}
	if !(shaky.IrrigationMM() < well.IrrigationMM() && well.IrrigationMM() < drip.IrrigationMM()) {
		t.Errorf("irrigation: unreliable %v, reliable %v, drip %v: want increasing", shaky.IrrigationMM(), well.IrrigationMM(), drip.IrrigationMM())
	}
	all := agro.Profile{Drip: true}
	for _, k := range agro.WaterKinds() {
		all.Water = append(all.Water, agro.Water{Kind: k, Reliable: true})
	}
	if all.IrrigationMM() > 250 {
		t.Errorf("%v mm a month: want a cap", all.IrrigationMM())
	}
}
