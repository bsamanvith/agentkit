package trigger_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/trigger"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no time zone data for %s: %v", name, err)
	}
	return l
}

// firings returns the first n times of spec after from.
func firings(t *testing.T, spec string, from time.Time, n int) []time.Time {
	t.Helper()
	s, err := trigger.ParseSchedule(spec)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", spec, err)
	}
	var out []time.Time
	for at := from; len(out) < n; {
		at = s.Next(at)
		if at.IsZero() {
			t.Fatalf("%q: no time after %v", spec, out)
		}
		out = append(out, at)
	}
	return out
}

func sameTimes(t *testing.T, name string, got, want []time.Time) {
	t.Helper()
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		var g, w []string
		for _, x := range got {
			g = append(g, x.Format(time.RFC3339))
		}
		for _, x := range want {
			w = append(w, x.Format(time.RFC3339))
		}
		t.Errorf("%s:\n got %v\nwant %v", name, g, w)
	}
}

// TestScheduleAcrossDaylightSaving is the schedules criterion's DST part:
// in New York, clocks go from 02:00 to 03:00 on 2026-03-08 and from 02:00
// back to 01:00 on 2026-11-01.
func TestScheduleAcrossDaylightSaving(t *testing.T) {
	t.Parallel()
	ny := loc(t, "America/New_York")
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, ny) }
	utc := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }

	// Weekdays at 9 stay at 9 by the wall clock: 14:00 UTC, then 13:00.
	sameTimes(t, "weekdays at 9 across the spring change",
		firings(t, "CRON_TZ=America/New_York 0 9 * * 1-5", at(2026, 3, 5, 12, 0), 4),
		[]time.Time{utc(2026, 3, 6, 14, 0), utc(2026, 3, 9, 13, 0), utc(2026, 3, 10, 13, 0), utc(2026, 3, 11, 13, 0)})

	// 02:30 does not exist on 03-08: it fires at the change, 03:00 EDT, once.
	sameTimes(t, "a skipped time",
		firings(t, "CRON_TZ=America/New_York 30 2 * * *", at(2026, 3, 7, 0, 0), 3),
		[]time.Time{at(2026, 3, 7, 2, 30), utc(2026, 3, 8, 7, 0), at(2026, 3, 9, 2, 30)})

	// 01:30 comes twice on 11-01: it fires the first time only (EDT, 05:30 UTC).
	sameTimes(t, "a repeated time",
		firings(t, "CRON_TZ=America/New_York 30 1 * * *", at(2026, 10, 31, 0, 0), 3),
		[]time.Time{utc(2026, 10, 31, 5, 30), utc(2026, 11, 1, 5, 30), utc(2026, 11, 2, 6, 30)})

	// Every 20 minutes of hour 1: the first run of the repeated hour only.
	sameTimes(t, "a repeated hour",
		firings(t, "CRON_TZ=America/New_York */20 1 * * *", at(2026, 11, 1, 0, 0), 4),
		[]time.Time{utc(2026, 11, 1, 5, 0), utc(2026, 11, 1, 5, 20), utc(2026, 11, 1, 5, 40), utc(2026, 11, 2, 6, 0)})

	// Hourly ("*" hours) runs by the clock's minutes: both runs of the
	// repeated hour, none of the skipped one.
	sameTimes(t, "hourly across the autumn change",
		firings(t, "CRON_TZ=America/New_York 0 * * * *", at(2026, 11, 1, 0, 0), 4),
		[]time.Time{utc(2026, 11, 1, 5, 0), utc(2026, 11, 1, 6, 0), utc(2026, 11, 1, 7, 0), utc(2026, 11, 1, 8, 0)})
	sameTimes(t, "hourly across the spring change",
		firings(t, "CRON_TZ=America/New_York 0 * * * *", at(2026, 3, 8, 0, 30), 3),
		[]time.Time{utc(2026, 3, 8, 6, 0), utc(2026, 3, 8, 7, 0), utc(2026, 3, 8, 8, 0)})

	// A zone whose change is at midnight (Santiago: 2026-09-06 00:00 → 01:00).
	scl := loc(t, "America/Santiago")
	sameTimes(t, "a skipped midnight",
		firings(t, "CRON_TZ=America/Santiago 0 0 * * *", time.Date(2026, 9, 5, 12, 0, 0, 0, scl), 2),
		[]time.Time{time.Date(2026, 9, 6, 1, 0, 0, 0, scl), time.Date(2026, 9, 7, 0, 0, 0, 0, scl)})
}

func TestScheduleFields(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // a Thursday
	u := func(m time.Month, d, h, min int) time.Time { return time.Date(2026, m, d, h, min, 0, 0, time.UTC) }
	for _, c := range []struct {
		spec string
		want []time.Time
	}{
		{"*/15 * * * *", []time.Time{u(1, 1, 0, 15), u(1, 1, 0, 30), u(1, 1, 0, 45)}},
		{"5,10-11 3 * * *", []time.Time{u(1, 1, 3, 5), u(1, 1, 3, 10), u(1, 1, 3, 11)}},
		{"0 9-17/4 * * *", []time.Time{u(1, 1, 9, 0), u(1, 1, 13, 0), u(1, 1, 17, 0)}},
		{"0 0 * * MON", []time.Time{u(1, 5, 0, 0), u(1, 12, 0, 0), u(1, 19, 0, 0)}},
		{"0 0 * * 7", []time.Time{u(1, 4, 0, 0), u(1, 11, 0, 0), u(1, 18, 0, 0)}},
		{"0 0 1 feb-mar *", []time.Time{u(2, 1, 0, 0), u(3, 1, 0, 0), time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)}},
		// Both days restricted: either matches (the 13th, and Fridays).
		{"0 0 13 * 5", []time.Time{u(1, 2, 0, 0), u(1, 9, 0, 0), u(1, 13, 0, 0)}},
		{"30 4 10/10 * *", []time.Time{u(1, 10, 4, 30), u(1, 20, 4, 30), u(1, 30, 4, 30)}},
		{"@hourly", []time.Time{u(1, 1, 1, 0), u(1, 1, 2, 0), u(1, 1, 3, 0)}},
		{"@daily", []time.Time{u(1, 2, 0, 0), u(1, 3, 0, 0), u(1, 4, 0, 0)}},
		{"@weekly", []time.Time{u(1, 4, 0, 0), u(1, 11, 0, 0), u(1, 18, 0, 0)}},
		{"@monthly", []time.Time{u(2, 1, 0, 0), u(3, 1, 0, 0), u(4, 1, 0, 0)}},
		{"@yearly", []time.Time{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"0 0 29 2 *", []time.Time{time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC), time.Date(2032, 2, 29, 0, 0, 0, 0, time.UTC), time.Date(2036, 2, 29, 0, 0, 0, 0, time.UTC)}},
		{"TZ=Asia/Kolkata 0 9 * * *", []time.Time{u(1, 1, 3, 30), u(1, 2, 3, 30), u(1, 3, 3, 30)}},
	} {
		sameTimes(t, c.spec, firings(t, c.spec, from, len(c.want)), c.want)
	}
	// The day of the week starts with "*": both must match. */5 over 0-6 is
	// Sunday and Friday: the 13ths of 2026 that are Fridays or Sundays.
	sameTimes(t, "both days, one starting with *",
		firings(t, "0 0 13 * */5", from, 3),
		[]time.Time{u(2, 13, 0, 0), u(3, 13, 0, 0), u(9, 13, 0, 0)})
}

func TestParseScheduleRefuses(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *",
		"* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "+5 * * * *",
		"0 0 30 2 *", "0 0 31 4,6,9,11 *", "@every 5m", "CRON_TZ=Nowhere/Land 0 9 * * *",
		"CRON_TZ= 0 9 * * *", "CRON_TZ=Local 0 9 * * *", "1,,2 * * * *", "*/61 * * * *",
	} {
		if _, err := trigger.ParseSchedule(spec); !errors.Is(err, trigger.ErrSchedule) {
			t.Errorf("ParseSchedule(%q) = %v, want ErrSchedule", spec, err)
		}
	}
	s, err := trigger.ParseSchedule("CRON_TZ=Europe/Paris 0 9 * * 1-5")
	if err != nil || s.Location().String() != "Europe/Paris" || s.String() != "CRON_TZ=Europe/Paris 0 9 * * 1-5" {
		t.Errorf("ParseSchedule = %v, %v", s, err)
	}
}

// FuzzSchedule: whatever the text, parsing does not panic, and a schedule
// that parses gives times that are after the time asked, on the minute, and
// increasing.
func FuzzSchedule(f *testing.F) {
	for _, s := range []string{"*/15 * * * *", "0 9 * * 1-5", "CRON_TZ=America/New_York 30 1 * * *", "0 0 29 2 *", "@hourly", "5/7 */3 1-31/2 * mon-fri"} {
		f.Add(s, int64(1_800_000_000))
	}
	f.Fuzz(func(t *testing.T, spec string, unix int64) {
		if len(spec) > 200 || strings.ContainsRune(spec, 0) {
			return
		}
		s, err := trigger.ParseSchedule(spec)
		if err != nil {
			return
		}
		at := time.Unix(unix%4_000_000_000, 0)
		for range 5 {
			next := s.Next(at)
			if next.IsZero() {
				return
			}
			if !next.After(at) || next.Second() != 0 || next.Nanosecond() != 0 {
				t.Fatalf("%q: Next(%v) = %v", spec, at, next)
			}
			at = next
		}
	})
}
