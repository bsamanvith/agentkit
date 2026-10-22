package trigger

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrSchedule means a schedule's text is not one [ParseSchedule] reads, or
// it never fires.
var ErrSchedule = errors.New("invalid schedule")

// Schedule is a cron schedule in a time zone. Make one with [ParseSchedule].
type Schedule struct {
	text                           string
	loc                            *time.Location
	minute, hour, dom, month, dow  uint64
	domStar, dowStar, hourWildcard bool
}

// horizon is how far ahead [Schedule.Next] looks: a schedule with no time in
// it (the 30th of February) never fires.
const horizon = 5 * 366 * 24 * time.Hour

// field is one of the five fields of a cron line.
type field struct {
	name     string
	min, max int
	names    []string // names[i] is the value min+i
}

var fields = [5]field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	{name: "day of week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

var descriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// ParseSchedule reads a cron schedule: five fields (minute, hour, day of
// month, month, day of week) or a descriptor (@hourly, @daily, @midnight,
// @weekly, @monthly, @yearly, @annually), optionally after "CRON_TZ=<IANA
// zone> " (or "TZ="), which sets the zone its times are read in; the default
// is UTC.
//
// A field is a comma-separated list of "*", a value, a range "a-b", and any
// of them with a step "/n" ("a/n" is "a-max/n"). Months and days of the week
// may be named (JAN-DEC, SUN-SAT, any case); Sunday is 0 or 7. As in cron,
// when both the day of the month and the day of the week are restricted
// (neither starts with "*"), a day matching either is chosen.
//
// It fails with an error matching [ErrSchedule], also for a schedule that
// never fires.
func ParseSchedule(text string) (*Schedule, error) {
	s := &Schedule{text: text, loc: time.UTC}
	rest := strings.TrimSpace(text)
	for _, prefix := range []string{"CRON_TZ=", "TZ="} {
		if v, ok := strings.CutPrefix(rest, prefix); ok {
			zone, after, _ := strings.Cut(v, " ")
			loc, err := time.LoadLocation(zone)
			if err != nil || zone == "" || strings.EqualFold(zone, "local") {
				return nil, fmt.Errorf("%w: %q: unknown time zone %q", ErrSchedule, text, zone)
			}
			s.loc, rest = loc, strings.TrimSpace(after)
			break
		}
	}
	if d, ok := descriptors[strings.ToLower(rest)]; ok {
		rest = d
	}
	parts := strings.Fields(rest)
	if len(parts) != len(fields) {
		return nil, fmt.Errorf("%w: %q: want five fields (minute hour day-of-month month day-of-week) or a descriptor such as @daily", ErrSchedule, text)
	}
	sets := make([]uint64, len(fields))
	for i, p := range parts {
		set, err := fields[i].parse(p)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %w", ErrSchedule, text, err)
		}
		sets[i] = set
	}
	s.minute, s.hour, s.dom, s.month, s.dow = sets[0], sets[1], sets[2], sets[3], sets[4]
	if s.dow&(1<<7) != 0 {
		s.dow |= 1 // 7 is Sunday too
	}
	s.domStar, s.dowStar = strings.HasPrefix(parts[2], "*"), strings.HasPrefix(parts[4], "*")
	s.hourWildcard = parts[1] == "*"
	ref := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if s.Next(ref).IsZero() && s.Next(ref.Add(horizon)).IsZero() {
		return nil, fmt.Errorf("%w: %q never fires", ErrSchedule, text)
	}
	return s, nil
}

func (f field) parse(text string) (uint64, error) {
	var set uint64
	for item := range strings.SplitSeq(text, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > f.max {
				return 0, fmt.Errorf("%s: bad step %q", f.name, stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch rng {
		case "*":
			if f.name == "day of week" {
				hi = 6
			}
		default:
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = f.value(a); err != nil {
				return 0, err
			}
			hi = lo
			switch {
			case isRange:
				if hi, err = f.value(b); err != nil {
					return 0, err
				}
				if hi < lo {
					return 0, fmt.Errorf("%s: range %q runs backwards", f.name, rng)
				}
			case hasStep:
				hi = f.max
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

func (f field) value(text string) (int, error) {
	for i, n := range f.names {
		if strings.EqualFold(text, n) {
			return f.min + i, nil
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil || v < f.min || v > f.max || strings.HasPrefix(text, "+") {
		return 0, fmt.Errorf("%s: %q is not a value from %d to %d", f.name, text, f.min, f.max)
	}
	return v, nil
}

// String returns the schedule's text as parsed.
func (s *Schedule) String() string { return s.text }

// Location is the time zone the schedule's times are read in.
func (s *Schedule) Location() *time.Location { return s.loc }

// Next returns the schedule's first time after t, or the zero time if it has
// none in the next five years. Times are wall-clock times in the schedule's
// zone, and a daylight-saving change moves them as cron does:
//
//   - A time a change skips (02:30 on the night clocks go from 02:00 to
//     03:00) comes at the change, once.
//   - A time a change repeats (01:30 on the night clocks go from 02:00 back
//     to 01:00) comes once, the first time.
//
// A schedule whose hour field is "*" runs by the clock's minutes instead: it
// fires in both runs of a repeated hour, and has no firings in a skipped one.
func (s *Schedule) Next(t time.Time) time.Time {
	c := t.Truncate(time.Minute).Add(time.Minute)
	end := t.Add(horizon)
	for c.Before(end) {
		w := c.In(s.loc)
		if !s.hourWildcard {
			if prev := c.Add(-time.Minute).In(s.loc); s.skipped(prev, w) {
				return c
			}
		}
		switch {
		case s.month&(1<<int(w.Month())) == 0:
			c = s.later(c, time.Date(w.Year(), w.Month()+1, 1, 0, 0, 0, 0, s.loc))
		case !s.day(w):
			c = s.later(c, time.Date(w.Year(), w.Month(), w.Day()+1, 0, 0, 0, 0, s.loc))
		case s.hour&(1<<w.Hour()) == 0:
			c = c.Add(time.Duration(60-w.Minute()) * time.Minute)
		case s.minute&(1<<w.Minute()) == 0:
			c = c.Add(time.Minute)
		case !s.hourWildcard && s.repeated(c, w):
			c = c.Add(time.Minute)
		default:
			return c
		}
	}
	return time.Time{}
}

// later is next, or one minute after c if next is not after c (a zone's
// midnight that a change skips can come out earlier).
func (s *Schedule) later(c, next time.Time) time.Time {
	if !next.After(c) {
		return c.Add(time.Minute)
	}
	return next
}

func (s *Schedule) day(w time.Time) bool {
	dom := s.dom&(1<<w.Day()) != 0
	dow := s.dow&(1<<int(w.Weekday())) != 0
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// matches reports whether the wall-clock time w (in any location: only its
// fields are read) is one of the schedule's.
func (s *Schedule) matches(w time.Time) bool {
	return s.month&(1<<int(w.Month())) != 0 && s.day(w) && s.hour&(1<<w.Hour()) != 0 && s.minute&(1<<w.Minute()) != 0
}

// skipped reports whether the clock jumped forward between the minutes prev
// and w, one minute apart, over a wall-clock time of the schedule's.
func (s *Schedule) skipped(prev, w time.Time) bool {
	from, to := wall(prev).Add(time.Minute), wall(w)
	for m := from; m.Before(to); m = m.Add(time.Minute) {
		if s.matches(m) {
			return true
		}
	}
	return false
}

// repeated reports whether the wall-clock time w of instant c already came
// before c: c is in the second run of an hour a change repeats.
func (s *Schedule) repeated(c, w time.Time) bool {
	for _, back := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour} {
		if wall(c.Add(-back).In(s.loc)).Equal(wall(w)) {
			return true
		}
	}
	return false
}

// wall is t's wall-clock time to the minute, as a UTC time with its fields.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
}
