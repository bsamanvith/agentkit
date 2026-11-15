package inputs

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/calendar"
)

// Amount says a quantity as it is bought and shown everywhere (the
// schedule, the shopping list's need, the season's work): grams and
// millilitres from a thousand on in kilograms and litres, whole numbers
// from a hundred on, one decimal below (none when it is whole). The
// shopping list rounds up to what is sold and says so beside it.
func Amount(v float64, unit string) string {
	switch {
	case unit == "g" && v >= 1000:
		v, unit = v/1000, "kg"
	case unit == "ml" && v >= 1000:
		v, unit = v/1000, "l"
	}
	var n string
	if v >= 100 {
		n = strconv.FormatFloat(math.Round(v), 'f', -1, 64)
	} else {
		n = strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
	}
	one := n == "1"
	switch unit {
	case "piece":
		unit = plural(one, "piece", "pieces")
	case "t":
		unit = plural(one, "tonne", "tonnes")
	case "l":
		unit = plural(one, "litre", "litres")
	}
	return n + " " + unit
}

func plural(one bool, a, b string) string {
	if one {
		return a
	}
	return b
}

// short is a product's name as said in a title: without what is in
// brackets, and in lower case unless it is an abbreviation ("urea",
// "DAP", "farmyard manure").
func short(name string) string {
	if i := strings.Index(name, " ("); i > 0 {
		name = name[:i]
	}
	if len(name) > 1 && name[1] >= 'a' && name[1] <= 'z' {
		name = strings.ToLower(name[:1]) + name[1:]
	}
	return name
}

// what lists a task's doses for its title: "urea, 21.7 kg" for one,
// "urea 21.7 kg, MOP 15 kg" for more.
func what(ds []Dose) string {
	var parts []string
	for _, d := range ds {
		if d.Qty <= 0 {
			parts = append(parts, short(d.Name))
			continue
		}
		sep := " "
		if len(ds) == 1 {
			sep = ", "
		}
		parts = append(parts, short(d.Name)+sep+Amount(d.Qty, d.Unit))
	}
	return strings.Join(parts, ", ")
}

var ordinals = []string{"First", "Second", "Third", "Fourth", "Fifth", "Sixth"}

// feedTitles names each feeding that is not the sowing's or the land
// preparation's by what it feeds and with what, in plain words: "First
// nitrogen feed: urea, 21.7 kg", "Sulphur feed: gypsum, 400 kg". The
// tasks are in day order; feeds of the same nutrients are counted.
func (x *fieldCtx) feedTitles(tasks []Task) {
	label := make([]string, len(tasks))
	count := map[string]int{}
	for i, t := range tasks {
		if t.Kind != "feed" || len(t.Doses) == 0 || !x.retitle(t.Key) {
			continue
		}
		var words []string
		add := func(w string) {
			for _, x := range words {
				if x == w {
					return
				}
			}
			words = append(words, w)
		}
		for _, d := range t.Doses {
			p := x.b.products[d.Product]
			if p.N > 0 {
				add("nitrogen")
			}
			if p.P > 0 {
				add("phosphorus")
			}
			if p.K > 0 {
				add("potash")
			}
			if p.S > 0 {
				add("sulphur")
			}
			if p.N == 0 && p.P == 0 && p.K == 0 && p.S == 0 {
				add("organic")
			}
		}
		label[i] = strings.Join(words, " and ")
		count[label[i]]++
	}
	seen := map[string]int{}
	for i := range tasks {
		l := label[i]
		if l == "" {
			continue
		}
		title := strings.ToUpper(l[:1]) + l[1:] + " feed"
		if count[l] > 1 {
			if k := seen[l]; k < len(ordinals) {
				title = ordinals[k] + " " + l + " feed"
			}
			seen[l]++
		}
		tasks[i].Title = fmt.Sprintf("%s: %s", title, what(tasks[i].Doses))
	}
}

// retitle: a feeding at key is named by its doses, unless the calendar
// has a task of another kind there (the sowing, the land preparation)
// whose title says more.
func (x *fieldCtx) retitle(key string) bool {
	if strings.HasPrefix(key, "jeevamrutham-") {
		return false
	}
	for _, c := range x.cal {
		if c.Key == key {
			return c.Kind == calendar.Fertilise
		}
	}
	return true
}

// preventTitles names each preventive step by the pests it is against
// and what it takes: "Against fall armyworm: pheromone trap with lure,
// 50 pieces".
func (x *fieldCtx) preventTitles(tasks []Task) {
	for i, t := range tasks {
		if ps := x.pests[t.Key]; t.Kind == "prevent" && len(ps) > 0 && len(t.Doses) > 0 {
			tasks[i].Title = fmt.Sprintf("Against %s: %s", strings.Join(ps, " and "), what(t.Doses))
		}
	}
}
