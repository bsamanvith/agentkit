package schemes

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/region"
)

// SoonDays is how many days before a last date it counts as soon: the
// alert goes out then.
const SoonDays = 14

// The state of a scheme's last date for the season (Item.Status).
const (
	// Open is a last date ahead, more than SoonDays away.
	Open = "open"
	// Soon is a last date within SoonDays, today included.
	Soon = "soon"
	// Passed is a last date gone: shown as passed, never as overdue.
	Passed = "passed"
	// NoDate is a scheme whose source gives no last date for the season.
	NoDate = "no_date"
	// Stale is a notice for an earlier season: its dates are last year's,
	// this season's are not yet announced.
	Stale = "stale"
)

// How a scheme covers a crop of the cycle (Item.Cover).
const (
	// Named: the source names the crop.
	Named = "named"
	// Notified: the source names no crops; the state notifies them each
	// season, so it may cover this one.
	Notified = "notified"
	// NotNamed: the source names crops, not this one (its list may be
	// partial: ask).
	NotNamed = "not_named"
)

// Confirm is said with every answer.
const Confirm = "Confirm at the bank, the Rythu Seva Kendra or the insurer before you rely on this."

// Pack is the region data the schemes are read from; *region.Pack is one.
type Pack interface {
	Schemes() []region.Scheme
	Sources() []region.Source
	Origins() map[string]region.Origin
	Rules() region.Rules
}

// Query is a plot's cycle: its crops and its first sowing.
type Query struct {
	// Crops are the cycle's crop ids, intercrops included.
	Crops []string
	// Sow is the cycle's first sowing: the season (by the pack's rules)
	// and the year it starts in come from it.
	Sow time.Time
	// Season overrides the season Sow falls in, when the cycle names one.
	Season string
	Today  time.Time
}

// Source is where an item was read: the document, the place in it, the
// day it was read and the pack's layer that gave it.
type Source struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Publisher string `json:"publisher,omitempty"`
	URL       string `json:"url,omitempty"`
	Where     string `json:"where"`
	// Read is the day the source was read, YYYY-MM-DD.
	Read string `json:"read"`
	// Layer is "base" or the overlay's region ("Andhra Pradesh").
	Layer string `json:"layer"`
}

// Item is one scheme for the cycle.
type Item struct {
	Scheme string `json:"scheme"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	// Cover is Named, Notified or NotNamed; Crops the cycle's crops the
	// source names.
	Cover string   `json:"cover"`
	Crops []string `json:"crops,omitempty"`
	// ForSeason is the one season a notice is for ("kharif 2026"); empty
	// for rules that hold every season.
	ForSeason string `json:"for_season,omitempty"`
	Status    string `json:"status"`
	// What (enrolment or sowing) and By (YYYY-MM-DD) are the season's
	// last date; DaysLeft is set while it is ahead (0 is today).
	What       string `json:"what,omitempty"`
	By         string `json:"by,omitempty"`
	DaysLeft   *int   `json:"days_left,omitempty"`
	CutoffNote string `json:"cutoff_note,omitempty"`
	// LossNoticeHours is how soon after a loss it must be reported.
	LossNoticeHours int `json:"loss_notice_hours,omitempty"`
	// Says is the item in a line, in plain words.
	Says   string `json:"says"`
	Note   string `json:"note,omitempty"`
	Source Source `json:"source"`
}

// Covers reports whether the scheme may cover the cycle: it names one of
// its crops, or names none.
func (it Item) Covers() bool { return it.Cover == Named || it.Cover == Notified }

// Answer is what applies to a cycle.
type Answer struct {
	Crops  []string `json:"crops"`
	Season string   `json:"season"`
	// Year is the year the season starts in (rabi 2026 runs into 2027).
	Year    int    `json:"year"`
	Today   string `json:"today"`
	Items   []Item `json:"items"`
	Confirm string `json:"confirm"`
}

// For returns the schemes of p that apply to the cycle q: those for every
// season and those noticed for q's season (an earlier year's as Stale),
// in the pack's order. A cycle with no crops gets none.
func For(p Pack, q Query) Answer {
	rules := p.Rules().Or()
	season := q.Season
	if season == "" {
		season = rules.SeasonOf(q.Sow)
	}
	year := startYear(rules, season, q.Sow)
	crops := uniq(q.Crops)
	a := Answer{Crops: crops, Season: season, Year: year, Today: q.Today.Format(agro.DateLayout), Items: []Item{}, Confirm: Confirm}
	if len(crops) == 0 {
		return a
	}
	sources := p.Sources()
	origins := p.Origins()
	for _, s := range p.Schemes() {
		if s.Season != "" && (s.Season != season || s.Year > year) {
			continue // a notice for another season, or for a later one
		}
		it := Item{
			Scheme: s.ID, Name: s.Name, Kind: s.Kind, LossNoticeHours: s.LossNoticeHours, Note: s.Note,
			Source: sourceOf(s, sources, origins),
		}
		it.Cover, it.Crops = cover(s.Crops, crops)
		if s.Season != "" {
			it.ForSeason = fmt.Sprintf("%s %d", s.Season, s.Year)
		}
		c, ok := cutoff(s.Cutoffs, season)
		if ok {
			it.What, it.CutoffNote = c.What, c.Note
		}
		switch {
		case s.Season != "" && s.Year < year:
			it.Status = Stale
			if ok {
				it.By = date(rules, season, s.Year, c.By).Format(agro.DateLayout)
			}
		case !ok:
			it.Status = NoDate
		default:
			by := date(rules, season, year, c.By)
			it.By = by.Format(agro.DateLayout)
			left := int(by.Sub(day(q.Today)).Hours() / 24)
			switch {
			case left < 0:
				it.Status = Passed
			case left <= SoonDays:
				it.Status, it.DaysLeft = Soon, &left
			default:
				it.Status, it.DaysLeft = Open, &left
			}
		}
		it.Says = says(it)
		a.Items = append(a.Items, it)
	}
	return a
}

// startYear is the year season starts in for a sowing on sow: a season
// that runs over the new year (rabi, October to February) sown in
// January started the year before.
func startYear(r region.Rules, season string, sow time.Time) int {
	for _, s := range r.Seasons {
		if s.Season == season && s.From > s.To && int(sow.Month()) < s.From {
			return sow.Year() - 1
		}
	}
	return sow.Year()
}

// date is MM-DD in the season that starts in year: in the next year when
// the season runs over the new year and the month is before its start.
func date(r region.Rules, season string, year int, mmdd string) time.Time {
	t, _ := time.Parse("01-02", mmdd)
	for _, s := range r.Seasons {
		if s.Season == season && s.From > s.To && int(t.Month()) < s.From {
			year++
		}
	}
	return time.Date(year, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

// cutoff is the season's enrolment cut-off, else its sowing one.
func cutoff(cs []region.Cutoff, season string) (region.Cutoff, bool) {
	var found *region.Cutoff
	for i, c := range cs {
		if c.Season != season {
			continue
		}
		if c.What == "enrolment" {
			return c, true
		}
		if found == nil {
			found = &cs[i]
		}
	}
	if found == nil {
		return region.Cutoff{}, false
	}
	return *found, true
}

func cover(named, crops []string) (string, []string) {
	if len(named) == 0 {
		return Notified, nil
	}
	var in []string
	for _, c := range crops {
		if slices.Contains(named, c) {
			in = append(in, c)
		}
	}
	if len(in) == 0 {
		return NotNamed, nil
	}
	return Named, in
}

func sourceOf(s region.Scheme, sources []region.Source, origins map[string]region.Origin) Source {
	out := Source{ID: s.Ref.Source, Name: s.Ref.Source, Where: s.Ref.Where, Layer: region.Base}
	if i := slices.IndexFunc(sources, func(x region.Source) bool { return x.ID == s.Ref.Source }); i >= 0 {
		x := sources[i]
		out.Name, out.Publisher, out.URL, out.Read = x.Name, x.Publisher, x.URL, x.Read
	}
	// The layer is the overlay whose ref the scheme carries: the one that
	// added it, or the last to change it.
	pre := "schemes." + s.ID
	for _, k := range sortedKeys(origins) {
		o := origins[k]
		if (k == pre || strings.HasPrefix(k, pre+".")) && o.Ref != nil && *o.Ref == s.Ref {
			out.Layer = o.Layer
		}
	}
	return out
}

func sortedKeys(m map[string]region.Origin) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func uniq(ss []string) []string {
	out := []string{}
	for _, s := range ss {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Long says a date as the farmer reads it: "15 July 2027".
func Long(d string) string {
	t, err := time.Parse(agro.DateLayout, d)
	if err != nil {
		return d
	}
	return t.Format("2 January 2006")
}

func what(it Item) string {
	if it.What == "sowing" {
		return "sow to be covered"
	}
	return "enrol"
}

func says(it Item) string {
	var b strings.Builder
	switch it.Status {
	case Open, Soon:
		left := "today is the last day"
		switch n := *it.DaysLeft; {
		case n == 1:
			left = "1 day left"
		case n > 1:
			left = fmt.Sprintf("%d days left", n)
		}
		fmt.Fprintf(&b, "Last date to %s: %s, %s.", what(it), Long(it.By), left)
	case Passed:
		fmt.Fprintf(&b, "The last date to %s, %s, has passed.", what(it), Long(it.By))
	case NoDate:
		b.WriteString("The source gives no last date for this season: ask before you sow.")
	case Stale:
		fmt.Fprintf(&b, "Last year's dates (%s), not yet announced for this season.", it.ForSeason)
	}
	switch it.Cover {
	case Notified:
		b.WriteString(" It covers the crops the state notifies each season: ask whether yours is one.")
	case NotNamed:
		b.WriteString(" Your crop is not among the crops this source names: ask whether it is notified.")
	}
	if it.LossNoticeHours > 0 {
		fmt.Fprintf(&b, " If the crop is damaged, report it within %d hours.", it.LossNoticeHours)
	}
	if it.Source.Read != "" {
		fmt.Fprintf(&b, " From %s, read on %s.", it.Source.Name, Long(it.Source.Read))
	}
	return b.String()
}

// LossNotice is the shortest loss-notice rule among the items that may
// cover the cycle and are not stale, and the item it is from; 0 when none
// has one.
func (a Answer) LossNotice() (int, Item) {
	best, from := 0, Item{}
	for _, it := range a.Items {
		if it.Covers() && it.Status != Stale && it.LossNoticeHours > 0 && (best == 0 || it.LossNoticeHours < best) {
			best, from = it.LossNoticeHours, it
		}
	}
	return best, from
}
