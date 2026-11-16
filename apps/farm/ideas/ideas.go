package ideas

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/region"
)

// The verdicts.
const (
	// Realistic: it scores well in the open field on this plot.
	Realistic = "realistic"
	// Stretch: it can work, with a growing method or with care.
	Stretch = "stretch"
	// Unrealistic: nothing the app knows makes it work here.
	Unrealistic = "unrealistic"
)

// The bands an idea is shown in.
const (
	// BandCommon is the usual crops of the place and season.
	BandCommon = "common"
	// BandNew is what fits but is not usual here: crops new to India, crops
	// out of their local season, experimental plants.
	BandNew = "new"
	// BandMethod is what needs a growing method to fit.
	BandMethod = "method"
	// BandNotHere is what does not fit, with the reason.
	BandNotHere = "unrealistic"
)

// DefaultLimit is how many ideas a band shows when the farmer did not
// search for one.
const DefaultLimit = 6

// Ask is what the farmer wants checked.
type Ask struct {
	// Search is a plant's name (any language the library has, or its
	// botanical name); empty for a spread of ideas.
	Search string `json:"search,omitempty"`
	// Limit caps each band (0: DefaultLimit).
	Limit int `json:"limit,omitempty"`
}

// Fit is how a crop does on the plot one way: in the open, or under a
// method.
type Fit struct {
	Score int `json:"score"`
	// Sow is the best sowing date, and Harvest about when it is cut.
	Sow     string `json:"sow"`
	Harvest string `json:"harvest"`
	// GoodWeeks is on how many of the year's weeks it can be sown well.
	GoodWeeks int `json:"good_weeks"`
	// Risks are what is against it, the gravest first.
	Risks []agro.Note `json:"risks,omitempty"`
}

// Gain is the sowing time a method adds to the open field's: the longest
// run of weeks that are good under it and not in the open.
type Gain struct {
	Weeks int    `json:"weeks"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// MethodFit is a crop under a growing method.
type MethodFit struct {
	Method string `json:"method"`
	Name   string `json:"name"`
	What   string `json:"what"`
	Skill  string `json:"skill"`
	Fit
	// Gain is the off-season sowing it opens, when that is its use.
	Gain *Gain `json:"gain,omitempty"`
	// Cost is the scheme's cost norm on the plot (or the largest area it
	// assists); nil when the book has none.
	Cost *Cost `json:"cost,omitempty"`
}

// Price is what the app knows of a crop's price, in rupees a quintal.
type Price struct {
	Commodity string `json:"commodity"`
	// Likely is the estimate from mandi history (0 when there is none),
	// with the area it is from and how far to trust it.
	Likely     float64 `json:"likely,omitempty"`
	Low        float64 `json:"low,omitempty"`
	High       float64 `json:"high,omitempty"`
	Area       string  `json:"area,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
	// MSP is the minimum support price, when the crop has one.
	MSP       float64 `json:"msp,omitempty"`
	MSPSeason string  `json:"msp_season,omitempty"`
	// Markets are the newest prices of markets near the plot, best first.
	Markets []prices.Quote `json:"markets,omitempty"`
}

// Idea is one crop checked against the plot.
type Idea struct {
	Crop  string            `json:"crop"`
	Name  string            `json:"name"`
	Names map[string]string `json:"names,omitempty"`
	// Common are an experimental plant's other names.
	Common       []string `json:"common,omitempty"`
	Kind         string   `json:"kind"`
	Experimental bool     `json:"experimental,omitempty"`
	Verdict      string   `json:"verdict"`
	Band         string   `json:"band"`
	// Why is the verdict's reason, in a sentence.
	Why string `json:"why"`
	// Open is the crop in the open field; With the best method tried for
	// it, when the open field was not good enough and a method fits it.
	Open Fit        `json:"open"`
	With *MethodFit `json:"with,omitempty"`
	// Price is nil when the app has no price for it at all.
	Price *Price `json:"price,omitempty"`
	// Sell are the ways to sell it, the surest first.
	Sell []Route `json:"sell"`

	local bool // a crop with the district's own sowing window
}

// Report is the ideas for a plot.
type Report struct {
	Farm  string       `json:"farm"`
	Place region.Place `json:"place"`
	Today string       `json:"today"`
	Ideas []Idea       `json:"ideas"`
	// Notes say what the answer rests on and what it leaves out.
	Notes []agro.Note `json:"notes"`
}

// Service checks ideas for the plots of a planner.
type Service struct {
	plan   *planner.Planner
	prices *prices.Service
	book   *Book
}

// New returns a Service. market may be nil: ideas then carry no price.
func New(plan *planner.Planner, market *prices.Service, book *Book) *Service {
	return &Service{plan: plan, prices: market, book: book}
}

// Book returns the service's book.
func (s *Service) Book() *Book { return s.book }

// For checks ideas for a plot: the farmer's own (ask.Search) or a spread
// across the bands.
func (s *Service) For(ctx context.Context, id string, ask Ask) (Report, error) {
	if ask.Limit < 0 || ask.Limit > 50 {
		return Report{}, fmt.Errorf("%w: limit %d: want 0 to 50", agro.ErrInvalid, ask.Limit)
	}
	limit := cmp.Or(ask.Limit, DefaultLimit)
	f, err := s.plan.Farms().Get(ctx, id)
	if err != nil {
		return Report{}, err
	}
	clim, err := s.plan.Climate(ctx, id)
	if err != nil {
		return Report{}, err
	}
	lib := s.plan.LibraryFor(f)
	wide, err := agro.Wide()
	if err != nil {
		return Report{}, err
	}
	r := agro.Request{Climate: clim, Today: s.plan.Today()}
	if f.Profile != nil {
		r.Profile = *f.Profile
		if soil, err := lib.Soil(f.Profile.Soil); err == nil {
			r.Soil = &soil
		}
	}
	if sl := planner.SlopeOf(f); sl != nil {
		r.Slope = *sl
	}
	place := planner.PlaceOf(f)
	out := Report{Farm: f.ID, Place: place, Today: r.Today.Format(agro.DateLayout), Ideas: []Idea{}}

	crops, err := s.candidates(ctx, id, lib, wide, strings.TrimSpace(ask.Search))
	if err != nil {
		return Report{}, err
	}
	if len(crops) == 0 {
		out.Notes = append(out.Notes, agro.Note{ID: "ideas.none", Message: "No plant in the library or the experimental list goes by that name."})
		return out, nil
	}
	r.Crops = make([]string, len(crops))
	byID := map[string]agro.Crop{}
	for i, c := range crops {
		r.Crops[i], byID[c.ID] = c.ID, c
	}
	r.Calendar = true
	open, err := agro.Score(crops, r)
	if err != nil {
		return Report{}, err
	}
	sqm := geo.Area(f.Boundary.Ring())
	for _, o := range open.Options {
		c := byID[o.Crop]
		idea := Idea{Crop: c.ID, Name: c.Name, Names: c.Names, Common: c.Common, Kind: c.Kind, Experimental: c.Experimental, Open: fitOf(o), local: len(c.Windows) > 0}
		if !c.Invasive {
			idea.With = s.bestMethod(c, r, sqm, o)
		}
		off := s.judge(&idea, c, o)
		switch {
		case off == nil:
			out.Ideas = append(out.Ideas, idea)
		case ask.Search != "":
			// One answer for the plant asked about: the open field's
			// verdict, with what the method adds.
			idea.Why = off.Why
			out.Ideas = append(out.Ideas, idea)
		default:
			idea.With = nil
			out.Ideas = append(out.Ideas, idea, *off)
		}
	}
	out.Ideas = pick(out.Ideas, s.book.Asked, limit, ask.Search != "")
	for i := range out.Ideas {
		idea := &out.Ideas[i]
		idea.Price = s.price(ctx, idea.Crop, place)
		idea.Sell = s.routes(*idea, place)
	}
	out.Notes = append(out.Notes,
		agro.Note{ID: "ideas.methods", Message: s.book.Assumed},
		agro.Note{ID: "ideas.experimental", Message: "A verdict on an experimental plant rests on FAO's climate and soil ranges alone, which treat some crops grown here as one-season crops as standing all year: take it as a first look, not a ruling."},
		agro.Note{ID: "ideas.money", Message: "Yield, running cost and profit an acre are not estimated: the app has no reviewed figures for them yet. A method's cost is the horticulture mission's cost norm, the ceiling its subsidy is worked on, not a quotation."},
	)
	if r.Profile.Rainfed() {
		out.Notes = append(out.Notes, agro.Note{ID: "ideas.rainfed", Message: "The plot is rainfed, so a shade net, a polyhouse or drip cannot run on it: they need a water source first."})
	}
	return out, nil
}

// candidates are the crops to check: those the farmer named, or the
// library with the plants people often ask about and the experimental
// tier's best for the plot.
func (s *Service) candidates(ctx context.Context, id string, lib, wide *agro.Library, search string) ([]agro.Crop, error) {
	var out []agro.Crop
	if search != "" {
		for _, c := range lib.Crops() {
			if c.Matches(search) {
				out = append(out, c)
			}
		}
		for _, c := range wide.Crops() {
			if c.Matches(search) && len(out) < 12 {
				out = append(out, c)
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, c := range lib.Crops() {
		if !c.Off {
			out, seen[c.ID] = append(out, c), true
		}
	}
	more := slices.Clone(s.book.Asked)
	exp, err := s.plan.Recommend(ctx, id, planner.Ask{Experimental: true})
	if err != nil {
		return nil, err
	}
	for _, o := range exp.Options {
		more = append(more, o.Crop)
	}
	for _, cid := range more {
		if c, err := wide.Crop(cid); err == nil && !seen[cid] {
			out, seen[cid] = append(out, c), true
		}
	}
	return out, nil
}

// MinGainWeeks is the least off-season sowing time a method must open
// to be worth suggesting for a crop that already does well in the open.
const MinGainWeeks = 8

// MaxOpenWeeks is the most weeks a crop can be sown well in the open
// and still be given an off-season idea: past it the crop is on the
// market most of the year, and a structure earns nothing extra.
const MaxOpenWeeks = 26

// MinLift is how much better a crop that already does well in the open
// must score under a method for the method to be worth its cost.
const MinLift = 15

// bestMethod scores the crop under every method that fits it and the
// plot, and returns the one that helps most, or nil when none helps
// enough. For a crop the open field does not suit that is the best score.
// For one it does, it is the method that opens the most off-season sowing
// time (its Gain is set), or failing that one that lifts the score by
// MinLift, as growing without soil does on a soil the crop dislikes.
func (s *Service) bestMethod(c agro.Crop, r agro.Request, sqm float64, open agro.Option) *MethodFit {
	good := open.Score >= agro.Good
	// Methods are tried cheapest first: a dearer, harder one must be
	// clearly better to be the one suggested.
	var best, off *MethodFit
	for _, m := range s.book.Methods {
		if !m.Fits(c) || m.NeedsWater && r.Profile.Rainfed() || m.Drains && (r.Soil == nil || !r.Soil.DrainsPoorly()) {
			continue
		}
		mc, mr := m.Apply(c, r)
		mr.Crops = []string{c.ID}
		res, err := agro.Score([]agro.Crop{mc}, mr)
		if err != nil || len(res.Options) == 0 {
			continue
		}
		o := res.Options[0]
		fit := &MethodFit{Method: m.ID, Name: m.Name, What: m.What, Skill: m.Skill, Fit: fitOf(o), Cost: m.CostOn(sqm)}
		if best == nil || fit.Score >= best.Score+5 {
			best = fit
		}
		// Growing without soil answers a soil the crop cannot take: it is
		// not the way to an off-season crop the soil suits.
		if good && !m.NoSoil {
			if g := gain(open.Calendar, o.Calendar); weeks(g) >= weeks(off.gain())+4 {
				f := *fit
				f.Gain = g
				off = &f
			}
		}
	}
	switch {
	case !good:
		return best
	case weeks(off.gain()) >= MinGainWeeks && fitOf(open).GoodWeeks <= MaxOpenWeeks:
		return off
	case best != nil && best.Score >= open.Score+MinLift:
		return best
	}
	return nil
}

func weeks(g *Gain) int {
	if g == nil {
		return 0
	}
	return g.Weeks
}

// gain returns the longest run of sowing dates good under the method and
// not in the open, or nil when there is none.
func gain(open, with []agro.Sowing) *Gain {
	was := map[string]bool{}
	for _, s := range open {
		was[s.Sow] = s.Score >= agro.Good
	}
	var best, cur *Gain
	for _, s := range with {
		if s.Score < agro.Good || was[s.Sow] {
			cur = nil
			continue
		}
		if cur == nil {
			cur = &Gain{From: s.Sow}
		}
		cur.Weeks, cur.To = cur.Weeks+1, s.Sow
		if best == nil || cur.Weeks > best.Weeks {
			g := *cur
			best = &g
		}
	}
	return best
}

func fitOf(o agro.Option) Fit {
	f := Fit{Score: o.Score, Sow: o.Sow, Harvest: o.Harvest, Risks: o.Risks}
	for _, s := range o.Calendar {
		if s.Score >= agro.Good {
			f.GoodWeeks++
		}
	}
	return f
}

// blockers are the risks that say why a crop does not fit, gravest first.
var blockers = []string{"invasive", "temp.hot", "temp.cold", "frost", "water.short", "climate.zone", "water.excess", "waterlogging", "soil.salt", "soil.ph", "soil.texture", "sun.low", "sun.strong", "temp.off", "day.short"}

func mainRisk(risks []agro.Note) string {
	for _, id := range blockers {
		for _, n := range risks {
			if n.ID == id {
				return n.Message
			}
		}
	}
	return ""
}

func has(risks []agro.Note, ids ...string) bool {
	return slices.ContainsFunc(risks, func(n agro.Note) bool { return slices.Contains(ids, n.ID) })
}

// climate are the risks no care on the farmer's side takes away.
var climate = []string{"temp.hot", "temp.cold", "frost", "climate.zone"}

// judge sets the idea's verdict, band and reason. For a crop that does
// well in the open and that a method still helps, it also returns the
// crop under that method as an idea of its own, for the methods band.
func (s *Service) judge(idea *Idea, c agro.Crop, o agro.Option) (off *Idea) {
	risk := mainRisk(o.Risks)
	with := idea.With
	switch {
	case c.Invasive:
		idea.Verdict, idea.Band, idea.Why = Unrealistic, BandNotHere, risk
	case o.Score >= agro.Good:
		idea.Verdict = Realistic
		switch {
		case c.Experimental:
			idea.Band, idea.Why = BandNew, "The climate and soil suit it, but it is outside the reviewed library: there is no local season, dose or trial behind it. Try a few plants first."
		case c.ClimateAnalog:
			idea.Band, idea.Why = BandNew, "The climate here matches where it is grown, but it is new to India: little local experience and few buyers. Try a small area and find the buyer first."
		case len(c.Windows) > 0:
			idea.Band, idea.Why = BandCommon, "A crop of this district, with its own local sowing window, and the plot suits it."
		case has(o.Risks, "season.unusual"):
			idea.Band, idea.Why = BandNew, "It fits the plot, but not in its usual season: a crop out of season can meet a better price, and more pests."
		default:
			idea.Band, idea.Why = BandCommon, "A usual crop for this season, and the plot suits it."
		}
		if with != nil {
			o := *idea
			o.Verdict, o.Band = Stretch, BandMethod
			if g := with.Gain; g != nil {
				o.Why = fmt.Sprintf("In the open it can be sown well on %d of the year's weeks. With the method %q, sowings from %s to %s work too, %d more weeks: an off-season crop, when fewer farmers have it to sell.", idea.Open.GoodWeeks, with.Name, dayMonth(g.From), dayMonth(g.To), g.Weeks)
			} else {
				o.Why = fmt.Sprintf("It grows in the open here, but something holds it back at %d of 100: %s With the method %q it scores %d.", o.Open.Score, lower(risk), with.Name, with.Score)
			}
			o.Why += costly(with)
			off = &o
		}
	case with != nil && with.Score >= agro.Good:
		idea.Verdict, idea.Band = Stretch, BandMethod
		idea.Why = fmt.Sprintf("In the open field it scores %d of 100: %s With the method %q it scores %d.", o.Score, lower(risk), with.Name, with.Score)
		idea.Why += costly(with)
	case o.Score >= agro.Fair && !has(o.Risks, climate...):
		idea.Verdict, idea.Band = Stretch, BandNew
		idea.Why = "It can work here with care, but something is against it: " + lower(risk)
		if with != nil && with.Score < o.Score+5 {
			idea.With = nil
		}
	default:
		idea.Verdict, idea.Band = Unrealistic, BandNotHere
		switch {
		case with != nil && with.Score >= o.Score+5:
			idea.Why = fmt.Sprintf("%s Even with the method %q it scores only %d of 100: %s", risk, with.Name, with.Score, lower(cmp.Or(mainRisk(with.Risks), "it still does not fit.")))
		case risk != "":
			idea.With = nil
			idea.Why = risk + " No growing method in the app changes that for this crop."
		default:
			idea.With = nil
		}
	}
	if idea.Why == "" {
		idea.Why = "It does not fit this plot's climate, water and soil."
	}
	return off
}

// costly is the warning a dear, skilled method carries.
func costly(m *MethodFit) string {
	if m.Skill != "high" {
		return ""
	}
	return " It is a costly structure and a skilled crop: visit a farmer who runs one before you build."
}

func (m *MethodFit) gain() *Gain {
	if m == nil {
		return nil
	}
	return m.Gain
}

// dayMonth writes a date as "1 November".
func dayMonth(d string) string {
	t, err := time.Parse(agro.DateLayout, d)
	if err != nil {
		return d
	}
	return t.Format("2 January")
}

// lower puts a sentence mid-sentence: its first letter small.
func lower(s string) string {
	if s == "" {
		return "it does not fit well."
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// pick keeps limit ideas a band, the best first; the plants people ask
// about come first among those that do not fit, since saying why not is
// what they are there for. With a search, everything found is kept.
func pick(all []Idea, asked []string, limit int, searched bool) []Idea {
	order := []string{BandCommon, BandNew, BandMethod, BandNotHere}
	score := func(i Idea) int {
		if i.Band == BandMethod && i.With != nil {
			return i.With.Score
		}
		return i.Open.Score
	}
	slices.SortStableFunc(all, func(a, b Idea) int {
		if d := slices.Index(order, a.Band) - slices.Index(order, b.Band); d != 0 {
			return d
		}
		if a.Band == BandNotHere {
			// Library crops and the asked-about first, then the nearest misses.
			ka, kb := slices.Index(asked, a.Crop), slices.Index(asked, b.Crop)
			if (ka >= 0) != (kb >= 0) {
				return cmp.Compare(btoi(kb >= 0), btoi(ka >= 0))
			}
			if ka >= 0 && ka != kb {
				return ka - kb
			}
		}
		if a.Band == BandMethod {
			// The most off-season time first, then the lift in score.
			if d := weeks(b.With.gain()) - weeks(a.With.gain()); d != 0 {
				return d
			}
		}
		return cmp.Or(btoi(b.local)-btoi(a.local), score(b)-score(a), strings.Compare(a.Open.Sow, b.Open.Sow), strings.Compare(a.Crop, b.Crop))
	})
	if searched {
		return all
	}
	n := map[string]int{}
	out := all[:0]
	for _, i := range all {
		if n[i.Band] < limit {
			n[i.Band]++
			out = append(out, i)
		}
	}
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// price is what the app knows of the crop's price near the place; nil
// when it knows nothing.
func (s *Service) price(ctx context.Context, cropID string, place region.Place) *Price {
	commodity, ok := prices.CropCommodity(cropID)
	if !ok {
		return nil
	}
	p := &Price{Commodity: commodity}
	for _, m := range prices.MSP(commodity) {
		if m.Price > p.MSP {
			p.MSP, p.MSPSeason = m.Price, m.Season
		}
	}
	if s.prices != nil {
		if e, err := s.prices.Estimate(ctx, prices.EstimateQuery{Commodity: commodity, State: place.State, District: place.District}); err == nil && e.Likely > 0 {
			p.Likely, p.Low, p.High, p.Area, p.Confidence = e.Likely, e.Low, e.High, e.Area, e.Confidence
		}
		if qs, err := s.prices.History(ctx, prices.Query{Commodity: commodity, State: place.State, District: place.District, Limit: 500}); err == nil {
			p.Markets = prices.Latest(qs)
			if len(p.Markets) > 3 {
				p.Markets = p.Markets[:3]
			}
		}
	}
	if p.MSP == 0 && p.Likely == 0 && len(p.Markets) == 0 {
		return nil
	}
	return p
}

// routes are the ways to sell the idea's crop from the place.
func (s *Service) routes(idea Idea, place region.Place) []Route {
	out := []Route{}
	for _, r := range s.book.Routes {
		switch {
		case r.MSPOnly && (idea.Price == nil || idea.Price.MSP == 0):
		case len(r.Kinds) > 0 && !slices.Contains(r.Kinds, idea.Kind):
		case len(r.States) > 0 && !slices.Contains(r.States, place.State):
		// The yard trades what it has a price for: a plant with none is
		// sold to a buyer found beforehand, or through a group.
		case (r.ID == "mandi" || r.ID == "enam" || r.ID == "rythu_bazaar") && idea.Experimental:
		default:
			out = append(out, r)
		}
	}
	return out
}
