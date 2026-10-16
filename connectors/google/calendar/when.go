package calendar

import (
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/connectors/google/common"
)

const (
	dateLayout  = "2006-01-02"
	maxSpan     = 366 * 24 * time.Hour
	defaultSpan = 7 * 24 * time.Hour
)

// when is a start or an end: an instant, or a calendar date for an all-day
// event.
type when struct {
	allDay bool
	t      time.Time
}

// parseWhen reads an RFC 3339 date-time with an offset, or a YYYY-MM-DD date.
func parseWhen(field, s string) (when, error) {
	s = strings.TrimSpace(s)
	layout, allDay := time.RFC3339, false
	if len(s) == len(dateLayout) {
		layout, allDay = dateLayout, true
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return when{}, common.Invalid("%s %q: want an RFC 3339 date-time with an offset (2026-10-20T14:00:00+05:30), or a date (2026-10-20) for an all-day event",
			field, common.Truncate(s, 40))
	}
	return when{allDay: allDay, t: t}, nil
}

func (w when) date() string { return w.t.Format(dateLayout) }

// timeOut is the API's form of a start or end.
type timeOut struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

func (w when) api(zone string) *timeOut {
	if w.allDay {
		return &timeOut{Date: w.date()}
	}
	return &timeOut{DateTime: w.t.Format(time.RFC3339), TimeZone: zone}
}

// span parses and orders a start and an end.
func span(start, end string) (s, e when, err error) {
	if s, err = parseWhen("start", start); err != nil {
		return
	}
	if e, err = parseWhen("end", end); err != nil {
		return
	}
	err = checkOrder(s, e)
	return
}

func checkOrder(s, e when) error {
	switch {
	case s.allDay != e.allDay:
		return common.Invalid("start and end must both be dates (all-day) or both be date-times")
	case s.allDay && !e.t.After(s.t):
		return common.Invalid("end %s must be after start %s; for an all-day event end is the day after its last day", e.date(), s.date())
	case e.t.Before(s.t):
		return common.Invalid("end is before start")
	}
	return nil
}

// window resolves an optional time_min and time_max: now to a week later
// when both are left out, a week after time_min when only that is given.
func window(now time.Time, min, max string) (string, string, error) {
	lo := now
	if strings.TrimSpace(min) != "" {
		w, err := parseWhen("time_min", min)
		if err != nil {
			return "", "", err
		}
		lo = w.t
	}
	hi := lo.Add(defaultSpan)
	if strings.TrimSpace(max) != "" {
		w, err := parseWhen("time_max", max)
		if err != nil {
			return "", "", err
		}
		hi = w.t
	}
	switch {
	case !hi.After(lo):
		return "", "", common.Invalid("time_max must be after time_min")
	case hi.Sub(lo) > maxSpan:
		return "", "", common.Invalid("the range from time_min to time_max is over one year; ask for a shorter one")
	}
	return lo.Format(time.RFC3339), hi.Format(time.RFC3339), nil
}
