package region

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

// ErrInvalid is an overlay that cannot be laid on the base: an unknown
// key, a value of the wrong type, a value without a reference, or a pack
// that agro.Load or inputs.Load refuses. The error names the file and the
// key.
var ErrInvalid = errors.New("region: invalid")

// Place is where a plot is, as the plot's location names it.
type Place struct {
	// Country is the ISO 3166-1 alpha-2 code ("IN").
	Country  string `json:"country"`
	State    string `json:"state,omitempty"`
	District string `json:"district,omitempty"`
}

// The base cycle rules.
const (
	// BaseTurnaroundDays is the days after a harvest to prepare the land
	// before the next cycle can be sown.
	BaseTurnaroundDays = 15
	// BaseMaxWaitDays is how many days after the land is free a suggested
	// sowing may be: a rainfed plot waits through the dry months for the
	// rains.
	BaseMaxWaitDays = 240
)

// Rotation is what a crop scores for the crop before it.
type Rotation struct {
	// SameFamily is added for the same botanical family twice.
	SameFamily int `json:"same_family"`
	// LegumeAfterOther is added for a legume after a non-legume.
	LegumeAfterOther int `json:"legume_after_other"`
	// CerealAfterLegume is added for a cereal after a legume.
	CerealAfterLegume int `json:"cereal_after_legume"`
}

// SeasonSpan is a season's usual sowing months, From to To (To may be in
// the next year: rabi runs October to February).
type SeasonSpan struct {
	Season string `json:"season"`
	From   int    `json:"from"` // month, 1 to 12
	To     int    `json:"to"`
}

// Contains reports whether month m is in the span.
func (s SeasonSpan) Contains(m time.Month) bool {
	if s.From <= s.To {
		return s.From <= int(m) && int(m) <= s.To
	}
	return int(m) >= s.From || int(m) <= s.To
}

// Rules are the crop cycle rules (package cycle): the turnaround, how
// long a suggestion may wait, what a day's wait costs, the rotation
// adjustments and the seasons. The zero value is "not set": callers use
// [DefaultRules] for it.
type Rules struct {
	TurnaroundDays int `json:"turnaround_days"`
	MaxWaitDays    int `json:"max_wait_days"`
	// WaitDaysPerPoint is how many days of waiting cost a suggestion one
	// point of score.
	WaitDaysPerPoint int          `json:"wait_days_per_point"`
	Rotation         Rotation     `json:"rotation"`
	Seasons          []SeasonSpan `json:"seasons"`
}

// DefaultRules returns the base rules: the numbers the app was built with.
func DefaultRules() Rules {
	return Rules{
		TurnaroundDays:   BaseTurnaroundDays,
		MaxWaitDays:      BaseMaxWaitDays,
		WaitDaysPerPoint: 10,
		Rotation:         Rotation{SameFamily: -20, LegumeAfterOther: 10, CerealAfterLegume: 5},
		Seasons:          []SeasonSpan{{agro.Kharif, 6, 9}, {agro.Rabi, 10, 2}, {agro.Zaid, 3, 5}},
	}
}

// IsZero reports whether no rule is set.
func (r Rules) IsZero() bool {
	return r.TurnaroundDays == 0 && r.MaxWaitDays == 0 && r.WaitDaysPerPoint == 0 && r.Rotation == (Rotation{}) && len(r.Seasons) == 0
}

// Or returns r, or [DefaultRules] when r is zero.
func (r Rules) Or() Rules {
	if r.IsZero() {
		return DefaultRules()
	}
	return r
}

// SeasonOf returns the season a sowing on t falls in: the first span that
// has its month, else agro.SeasonOf.
func (r Rules) SeasonOf(t time.Time) string {
	for _, s := range r.Seasons {
		if s.Contains(t.Month()) {
			return s.Season
		}
	}
	return agro.SeasonOf(t)
}

func (r Rules) clone() Rules {
	r.Seasons = slices.Clone(r.Seasons)
	return r
}

// Ref says where a value was read: a source's id and the page or section.
type Ref struct {
	Source string `json:"source"`
	Where  string `json:"where"`
}

// Source is a document an overlay's values were read from.
type Source struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
	URL       string `json:"url"`
	// Read is the day it was read, YYYY-MM-DD.
	Read string `json:"read"`
}

// Cutoff is a scheme's last day for something in a season: enrolment, or
// sowing to be covered. By is MM-DD.
type Cutoff struct {
	Season string `json:"season"`
	What   string `json:"what"` // enrolment or sowing
	By     string `json:"by"`
	Note   string `json:"note,omitempty"`
}

// Scheme is a government scheme as data (crop insurance first): what it
// covers and its dates. No feature reads it yet; it is kept so a feature
// can, and so a review desk can show it with its source.
type Scheme struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // insurance, ...
	// Crops are the crop ids covered; empty when the source does not name
	// them.
	Crops   []string `json:"crops,omitempty"`
	Cutoffs []Cutoff `json:"cutoffs,omitempty"`
	// LossNoticeHours is how long after a loss the farmer has to report it.
	LossNoticeHours int    `json:"loss_notice_hours,omitempty"`
	Note            string `json:"note,omitempty"`
	Ref             Ref    `json:"ref"`
}

func (s Scheme) clone() Scheme {
	s.Crops, s.Cutoffs = slices.Clone(s.Crops), slices.Clone(s.Cutoffs)
	return s
}

// Base is the layer name of the base set.
const Base = "base"

// Origin is where a value of a pack came from: the layer that set it
// (Base, or an overlay's name), its file, and the reference it gave.
type Origin struct {
	Layer string `json:"layer"`
	File  string `json:"file,omitempty"`
	Ref   *Ref   `json:"ref,omitempty"`
}

// Pack is the data that holds at a place. It is read-only and safe for
// concurrent use; what its methods return is a copy or immutable.
type Pack struct {
	key     string
	name    string
	layers  []string
	lib     *agro.Library
	book    *inputs.Book
	rules   Rules
	storage map[string]prices.Storage
	msp     []prices.SupportPrice
	schemes []Scheme
	sources []Source
	origins map[string]Origin
}

// Key is the pack's region key: "base", "IN", "IN/andhrapradesh" or
// "IN/andhrapradesh/ysrkadapa" (names folded as places match).
func (p *Pack) Key() string { return p.key }

// Name is the most specific layer's name: "base", "Andhra Pradesh".
func (p *Pack) Name() string { return p.name }

// Layers are the layers laid on each other, base first.
func (p *Pack) Layers() []string { return slices.Clone(p.layers) }

// Library is the crop library here.
func (p *Pack) Library() *agro.Library { return p.lib }

// Book is the inputs book here, checked against [Pack.Library].
func (p *Pack) Book() *inputs.Book { return p.book }

// Rules are the cycle rules here.
func (p *Pack) Rules() Rules { return p.rules.clone() }

// Storage returns what holding a commodity (a crop id or a mandi name)
// costs here, by its storage class.
func (p *Pack) Storage(commodity string) prices.Storage {
	return p.storage[prices.StorageClassOf(commodity)]
}

// MSP returns the support prices of a commodity here, as prices.MSP does.
func (p *Pack) MSP(commodity string) []prices.SupportPrice {
	c := prices.Commodity(commodity)
	var out []prices.SupportPrice
	for _, s := range p.msp {
		if strings.EqualFold(s.Commodity, c) {
			out = append(out, s)
		}
	}
	return out
}

// Schemes are the schemes here, by id.
func (p *Pack) Schemes() []Scheme {
	out := make([]Scheme, len(p.schemes))
	for i, s := range p.schemes {
		out[i] = s.clone()
	}
	return out
}

// Sources are the documents the overlays of this pack cite (the base's
// values cite theirs in the library and the book).
func (p *Pack) Sources() []Source { return slices.Clone(p.sources) }

// Origin returns the layer a value came from, by its path:
// "crops.groundnut.windows", "inputs.crops.groundnut.rainfed",
// "rules.turnaround_days", "rules.rotation.same_family",
// "storage.pulses.cost_per_q_month", "schemes.pmfby.crops", "msp.<commodity>". A path
// no overlay set (or a parent of one an overlay added whole) is
// Origin{Layer: Base}.
func (p *Pack) Origin(path string) Origin {
	for k := path; k != ""; k = parent(k) {
		if o, ok := p.origins[k]; ok {
			if o.Ref != nil {
				r := *o.Ref
				o.Ref = &r
			}
			return o
		}
	}
	return Origin{Layer: Base}
}

// Origins returns every path an overlay set, with its origin.
func (p *Pack) Origins() map[string]Origin { return maps.Clone(p.origins) }

func parent(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	return path[:i]
}
