package inputs

import (
	"fmt"
	"strings"
)

// Value is one value of the data, where it is used and where it was read,
// for a review desk to list and check.
type Value struct {
	// Crop is the crop id it is used for; empty for a product's value.
	Crop string `json:"crop,omitempty"`
	// What names the value: "irrigated N-P2O5-K2O", "seed rate",
	// "leaf_miner: conventional", "product urea".
	What  string `json:"what"`
	Value string `json:"value"`
	// Default marks a value a crop takes from another crop of its kind.
	Default bool   `json:"default,omitempty"`
	From    string `json:"from,omitempty"` // the crop a default is taken from
	Ref     Ref    `json:"ref"`
	Source  Source `json:"source"`
}

// Sources returns every source of the data and of the banned list.
func (b *Book) Sources() []Source {
	out := make([]Source, len(b.data.Sources))
	copy(out, b.data.Sources)
	return out
}

// Values lists every value of the book with its source: the products, then
// each crop of the library in turn (its own values or its kind's default),
// then the banned list.
func (b *Book) Values() []Value {
	var out []Value
	add := func(crop, what, value string, r Ref, dflt bool, from string) {
		out = append(out, Value{Crop: crop, What: what, Value: value, Default: dflt, From: from, Ref: r, Source: b.sources[r.Source]})
	}
	for _, p := range b.Products() {
		v := []string{"sold in " + p.Unit}
		for _, n := range []struct {
			name string
			pct  float64
		}{{"N", p.N}, {"P2O5", p.P}, {"K2O", p.K}, {"S", p.S}} {
			if n.pct > 0 {
				v = append(v, fmt.Sprintf("%s %g %%", n.name, n.pct))
			}
		}
		if p.Active != "" {
			v = append(v, "active: "+p.Active)
		}
		add("", "product "+p.ID, p.Name+": "+strings.Join(v, ", "), p.Ref, false, "")
		if p.PackRef != nil {
			add("", "product "+p.ID+" pack", p.PackName, *p.PackRef, false, "")
		}
		if p.NPOPRef != nil {
			add("", "product "+p.ID+" organic standard", "NPOP: "+p.NPOP, *p.NPOPRef, false, "")
		}
	}
	for _, lc := range b.lib.Crops() {
		cd, dflt := b.resolve(lc)
		from := ""
		if dflt {
			from = cd.Crop
		}
		per := "kg/ha"
		if cd.PerPlant {
			per = "g per plant, first year"
		}
		for _, s := range []struct {
			what string
			r    *SeedRate
		}{{"seed rate", cd.Seed}, {"seed rate, rainfed", cd.SeedRainfed}} {
			if s.r != nil {
				add(lc.ID, s.what, fmt.Sprintf("%g %s per ha: %s", s.r.Qty, s.r.Unit, s.r.What), s.r.Ref, dflt, from)
			}
		}
		for _, n := range []struct {
			what string
			v    *NPK
		}{{"rainfed N-P2O5-K2O", cd.Rainfed}, {"irrigated N-P2O5-K2O", cd.Irrigated}, {"grown N-P2O5-K2O", cd.Mature}} {
			if n.v == nil {
				continue
			}
			v := fmt.Sprintf("%g-%g-%g %s", n.v.N, n.v.P, n.v.K, per)
			if n.v.S > 0 {
				v += fmt.Sprintf(", S %g", n.v.S)
			}
			add(lc.ID, n.what, v, n.v.Ref, dflt, from)
		}
		for _, u := range append(append([]Use{}, cd.Add...), cd.Organic...) {
			add(lc.ID, u.Product+" ("+cmpOr(u.Route, RouteBoth)+")", fmt.Sprintf("%g %s at %s", u.Qty, cmpOr(u.Unit, b.products[u.Product].Unit), whenText(u.At, u.DAS)), u.Ref, dflt, from)
		}
		for _, s := range cd.SeedTreatment {
			add(lc.ID, "seed treatment "+s.Product+" ("+s.Route+")", fmt.Sprintf("%g %s per kg of seed", s.PerKgSeed, b.products[s.Product].Unit), s.Ref, false, "")
		}
		for _, pr := range cd.Protect {
			add(lc.ID, pr.ID+": signs", pr.Signs, pr.SignsRef, false, "")
			if pr.ThresholdRef != nil {
				add(lc.ID, pr.ID+": threshold", pr.Threshold, *pr.ThresholdRef, false, "")
			}
			for _, p := range pr.Prevent {
				v := p.Text
				if p.Product != "" {
					v = fmt.Sprintf("%s %g per %s at %s. %s", p.Product, p.Qty, p.Per, whenText(p.At, p.DAS), p.Note)
				}
				add(lc.ID, pr.ID+": prevent", strings.TrimSpace(v), p.Ref, false, "")
			}
			for _, rr := range []struct {
				route string
				rs    []Remedy
			}{{RouteOrganic, pr.Organic}, {RouteConventional, pr.Conventional}} {
				for _, r := range rr.rs {
					v := r.Text
					if r.Product != "" {
						p := b.products[r.Product]
						v = strings.TrimSpace(p.Name + ": " + rate(r, p.Unit) + " " + r.Text)
						if w := water(r); w != "" {
							v += ", " + w + " of water per acre"
						}
						if r.PHIDays != nil {
							v += fmt.Sprintf(", PHI %d days", *r.PHIDays)
						}
					}
					add(lc.ID, pr.ID+": "+rr.route, v, r.Ref, false, "")
				}
			}
		}
	}
	j := b.data.Jeevamrutham
	add("", "jeevamrutham", fmt.Sprintf("%g litres per acre every %d days; %s", j.LitresPerAcre, j.EveryDays, j.Note), j.Ref, false, "")
	for _, u := range b.data.DefaultOrganic {
		add("", "default organic "+u.Product, fmt.Sprintf("%g %s per ha. %s", u.Qty, b.products[u.Product].Unit, u.Note), u.Ref, true, "")
	}
	for _, a := range b.banned.Actives {
		add("", "kept out: "+a.Name, a.Status+". "+a.Note, a.Ref, false, "")
	}
	return out
}

func whenText(at string, das *int) string {
	if das != nil {
		return fmt.Sprintf("%d days after sowing", *das)
	}
	return at
}
