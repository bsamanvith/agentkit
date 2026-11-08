// Package prices gives the farmer crop prices: what a crop sells for in the
// mandis (regulated wholesale markets, APMCs) near them today, the history
// of those prices, and an estimate of what it may fetch in the month they
// expect to sell.
//
// Prices come from providers ([Source], a [feeds.Provider]) chosen by
// configuration and asked in order, so one going away (the space is
// fragmented, and the government sites refuse some servers) does not take
// the feature with it:
//
//   - [DataGov] reads Agmarknet's daily mandi prices for all of India from
//     the Open Government Data platform (api.data.gov.in), with a free key.
//     One source for every state: Karnataka, Andhra Pradesh, Telangana and
//     Tamil Nadu first, and the rest at no extra cost.
//   - [Files] reads price files someone downloaded (Agmarknet's report
//     export, data.gov.in's CSV, CEDA's Agri Market data) from a folder:
//     years of history in one go, and a way in when no API answers.
//   - The [Store] keeps every price any provider gave, so the history grows
//     day by day and the last known price is there when every provider is
//     down.
//
// Prices are in rupees per quintal (100 kg), as the mandis quote them:
// minimum, maximum and modal (the most common trading price) of a day's
// trade in one market for one variety and grade.
//
// Estimates ([Estimate]) are arithmetic on that history (the recent level,
// the crop's usual swing from month to month, the spread) with the minimum
// support price ([MSP]) beside them, never a model's guess.
package prices

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
)

// ErrInvalid is a query that cannot be asked.
var ErrInvalid = errors.New("prices: invalid query")

// Quote is one day's prices of one commodity (variety, grade) in one
// market, in rupees per quintal.
type Quote struct {
	Date      Day     `json:"date"`
	State     string  `json:"state"`
	District  string  `json:"district"`
	Market    string  `json:"market"`
	Commodity string  `json:"commodity"`
	Variety   string  `json:"variety,omitempty"`
	Grade     string  `json:"grade,omitempty"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	Modal     float64 `json:"modal"`
	// Source names the provider the quote came from.
	Source string `json:"source"`
}

// Valid reports whether the quote can be kept: a date, a market, a
// commodity and prices that make sense (0 < min <= modal <= max, under a
// crore a quintal).
func (q Quote) Valid() error {
	switch {
	case q.Date.IsZero():
		return fmt.Errorf("%w: no date", ErrInvalid)
	case strings.TrimSpace(q.Market) == "" || strings.TrimSpace(q.Commodity) == "":
		return fmt.Errorf("%w: no market or commodity", ErrInvalid)
	case !(q.Modal > 0) || q.Modal > 1e7:
		return fmt.Errorf("%w: modal price %v", ErrInvalid, q.Modal)
	case q.Min > 0 && q.Min > q.Modal, q.Max > 0 && q.Max < q.Modal:
		return fmt.Errorf("%w: min %v, modal %v, max %v out of order", ErrInvalid, q.Min, q.Modal, q.Max)
	}
	return nil
}

// Query asks for prices. Empty fields match everything; names match
// without regard to case, spaces or punctuation. Commodity may be one of
// the app's crop ids ("pigeon_pea") or a common name ("tur"): [Commodity]
// turns it into Agmarknet's name.
type Query struct {
	State     string `json:"state,omitempty"`
	District  string `json:"district,omitempty"`
	Market    string `json:"market,omitempty"`
	Commodity string `json:"commodity,omitempty"`
	// From and To bound the dates (both included); zero for no bound.
	// Live providers may only have the latest days.
	From Day `json:"from,omitzero"`
	To   Day `json:"to,omitzero"`
	// Limit caps the quotes returned (0 for [DefaultLimit]).
	Limit int `json:"limit,omitempty"`
}

// DefaultLimit and MaxLimit bound a query's answer.
const (
	DefaultLimit = 200
	MaxLimit     = 50000
)

// Normalized returns q with its commodity in Agmarknet's name and its
// limit set, or an error if it asks for nothing or for dates backwards.
func (q Query) Normalized() (Query, error) {
	q.State = stateName(strings.TrimSpace(q.State))
	q.District = strings.TrimSpace(q.District)
	q.Market = strings.TrimSpace(q.Market)
	q.Commodity = Commodity(q.Commodity)
	if q.State == "" && q.Commodity == "" && q.Market == "" {
		return q, fmt.Errorf("%w: name a state, a market or a commodity", ErrInvalid)
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.To.Before(q.From) {
		return q, fmt.Errorf("%w: to %s is before from %s", ErrInvalid, q.To, q.From)
	}
	switch {
	case q.Limit <= 0:
		q.Limit = DefaultLimit
	case q.Limit > MaxLimit:
		q.Limit = MaxLimit
	}
	return q, nil
}

// Matches reports whether quote x answers q (dates and names; not the
// limit).
func (q Query) Matches(x Quote) bool {
	if !q.From.IsZero() && x.Date.Before(q.From) || !q.To.IsZero() && q.To.Before(x.Date) {
		return false
	}
	return nameMatch(q.State, x.State) && nameMatch(q.District, x.District) &&
		nameMatch(q.Market, x.Market) && (q.Commodity == "" || SameCommodity(q.Commodity, x.Commodity))
}

// Key is a cache key for q.
func (q Query) Key() string {
	return strings.Join([]string{Fold(q.State), Fold(q.District), Fold(q.Market), Fold(q.Commodity), q.From.String(), q.To.String(), fmt.Sprint(q.Limit)}, "|")
}

// Source gives quotes for a query, newest first. A source with nothing for
// the query returns [feeds.ErrNoData].
type Source = feeds.Provider[Query, []Quote]

// nameMatch reports whether want (empty for any) names got. A district or
// market is often spelled two ways (Rangareddy, Rangareddi; "Bangalore",
// "Binny Mill (F&V), Bangalore"), so a folded name that starts the other
// one, either way round, matches too.
func nameMatch(want, got string) bool {
	if want == "" {
		return true
	}
	w, g := Fold(want), Fold(got)
	if w == "" || g == "" {
		return w == g
	}
	return w == g || strings.HasPrefix(g, w) || strings.HasPrefix(w, g) || len(w) >= 5 && strings.Contains(g, w) || spellingVariant(w, g)
}

// spellingVariant reports whether a and b differ only in their last
// letter or two (Rangareddy, Rangareddi; Tumkur, Tumakuru is not caught).
func spellingVariant(a, b string) bool {
	if len(a) < 6 || len(b) < 6 {
		return false
	}
	n := min(len(a), len(b)) - 1
	return a[:n] == b[:n] && abs(len(a)-len(b)) <= 1
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Fold lowers a name and keeps only its letters and digits, for matching.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Day is a calendar day, as JSON "2006-01-02". Mandi prices are per day,
// with no time zone that matters.
type Day struct{ t time.Time }

// DayOf returns the day of t, in t's location.
func DayOf(t time.Time) Day {
	y, m, d := t.Date()
	return Day{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// NewDay returns the day y-m-d.
func NewDay(y int, m time.Month, d int) Day { return Day{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)} }

// ParseDay reads a day in the forms the sources use: 2006-01-02,
// 02/01/2006, 02-01-2006, 02-Jan-2006, 02 Jan 2006, 2 January 2006.
func ParseDay(s string) (Day, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006", "02-01-2006", "02-Jan-2006", "02 Jan 2006", "2 January 2006", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return DayOf(t), nil
		}
	}
	return Day{}, fmt.Errorf("%w: date %q", ErrInvalid, s)
}

// Time is the day's midnight, UTC.
func (d Day) Time() time.Time { return d.t }

// IsZero reports whether d is unset.
func (d Day) IsZero() bool { return d.t.IsZero() }

// Before reports whether d is before e.
func (d Day) Before(e Day) bool { return d.t.Before(e.t) }

// AddDays returns d plus n days.
func (d Day) AddDays(n int) Day { return Day{d.t.AddDate(0, 0, n)} }

// DaysSince is the number of days from e to d.
func (d Day) DaysSince(e Day) int { return int(d.t.Sub(e.t).Hours() / 24) }

// String is 2006-01-02, or "" for the zero day.
func (d Day) String() string {
	if d.IsZero() {
		return ""
	}
	return d.t.Format("2006-01-02")
}

// MarshalText implements encoding.TextMarshaler.
func (d Day) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Day) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Day{}
		return nil
	}
	x, err := ParseDay(string(b))
	if err != nil {
		return err
	}
	*d = x
	return nil
}

// newestFirst orders quotes by date, newest first, then by market.
func newestFirst(a, b Quote) int {
	if c := b.Date.t.Compare(a.Date.t); c != 0 {
		return c
	}
	if c := strings.Compare(a.Market, b.Market); c != 0 {
		return c
	}
	return strings.Compare(a.Variety, b.Variety)
}
