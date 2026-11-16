package schemes_test

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/schemes"
)

const apNotice = "ap-crop-insurance-kharif-2026"

var (
	kadapa = region.Place{Country: "IN", State: "Andhra Pradesh", District: "YSR Kadapa"}
	bengal = region.Place{Country: "IN", State: "West Bengal"}
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// answer runs For and checks what holds for every answer: the confirm
// line; every item with its source, read date and layer, said in a line
// that names the read date; days left only for a date ahead.
func answer(t *testing.T, p schemes.Pack, q schemes.Query) schemes.Answer {
	t.Helper()
	a := schemes.For(p, q)
	if a.Confirm == "" || !strings.Contains(a.Confirm, "Rythu Seva Kendra") {
		t.Errorf("no confirm line: %q", a.Confirm)
	}
	for _, it := range a.Items {
		if it.Source.Read == "" || it.Source.Layer == "" || it.Source.Name == "" || it.Source.Where == "" {
			t.Errorf("%s: source incomplete %+v", it.Scheme, it.Source)
		}
		if !strings.Contains(it.Says, schemes.Long(it.Source.Read)) {
			t.Errorf("%s: %q does not name the day its source was read", it.Scheme, it.Says)
		}
		if (it.DaysLeft != nil) != (it.Status == schemes.Open || it.Status == schemes.Soon) {
			t.Errorf("%s: status %s with days left %v", it.Scheme, it.Status, it.DaysLeft)
		}
		if it.DaysLeft != nil && *it.DaysLeft < 0 {
			t.Errorf("%s: negative days left", it.Scheme)
		}
	}
	return a
}

func item(t *testing.T, a schemes.Answer, id string) schemes.Item {
	t.Helper()
	for _, it := range a.Items {
		if it.Scheme == id {
			return it
		}
	}
	t.Fatalf("no %s in %+v", id, a.Items)
	return schemes.Item{}
}

func ids(a schemes.Answer) []string {
	var out []string
	for _, it := range a.Items {
		out = append(out, it.Scheme)
	}
	return out
}

func embedded(t *testing.T) *region.Set {
	t.Helper()
	s, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The PMFBY cut-off of the cycle's season, before, inside the 14 days and
// after it; a passed date is passed, with no days left.
func TestCutoffBeforeSoonAndPassed(t *testing.T) {
	t.Parallel()
	p := embedded(t).Base()
	for _, tc := range []struct {
		name, sow, today, status, by string
		left                         int
	}{
		{"kharif, weeks ahead", "2027-06-20", "2027-06-10", schemes.Open, "2027-07-15", 35},
		{"kharif, 14 days", "2027-06-20", "2027-07-01", schemes.Soon, "2027-07-15", 14},
		{"kharif, last day", "2027-06-20", "2027-07-15", schemes.Soon, "2027-07-15", 0},
		{"kharif, passed", "2027-06-20", "2027-07-16", schemes.Passed, "2027-07-15", -1},
		{"rabi sown in January: the season began the year before", "2027-01-10", "2027-01-10", schemes.Passed, "2026-12-15", -1},
		{"rabi sown in November", "2026-11-20", "2026-11-16", schemes.Open, "2026-12-15", 29},
		{"zaid: no cut-off in the source", "2027-03-20", "2027-03-01", schemes.NoDate, "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := answer(t, p, schemes.Query{Crops: []string{"rice"}, Sow: d(tc.sow), Today: d(tc.today)})
			it := item(t, a, "pmfby")
			if it.Status != tc.status || it.By != tc.by {
				t.Errorf("status %s by %s, want %s by %s", it.Status, it.By, tc.status, tc.by)
			}
			if tc.left >= 0 && (it.DaysLeft == nil || *it.DaysLeft != tc.left) {
				t.Errorf("days left %v, want %d", it.DaysLeft, tc.left)
			}
			if it.LossNoticeHours != 72 || !strings.Contains(it.Says, "72 hours") {
				t.Errorf("loss notice %d, %q", it.LossNoticeHours, it.Says)
			}
		})
	}
}

// Outside Andhra Pradesh only the base PMFBY applies, from the base
// layer; it names no crops, so it may cover any crop (the state notifies).
func TestBasePMFBYOnlyOutsideAP(t *testing.T) {
	t.Parallel()
	a := answer(t, embedded(t).For(bengal), schemes.Query{Crops: []string{"rice"}, Sow: d("2026-06-20"), Today: d("2026-06-01")})
	if got := ids(a); !slices.Equal(got, []string{"pmfby"}) {
		t.Fatalf("schemes %v, want pmfby alone", got)
	}
	it := a.Items[0]
	if it.Cover != schemes.Notified || !it.Covers() || it.Source.Layer != region.Base || it.Source.ID != "pmfby-og-2020" || it.Source.Read != "2026-11-15" {
		t.Errorf("pmfby %+v", it)
	}
	if a.Season != "kharif" || a.Year != 2026 {
		t.Errorf("season %s %d", a.Season, a.Year)
	}
}

// In AP the state's kharif 2026 notice joins PMFBY: it names rice (covered)
// and not groundnut, has no last date, and comes from the AP layer.
func TestAPNoticeCoversNamedCrops(t *testing.T) {
	t.Parallel()
	p := embedded(t).For(kadapa)
	q := schemes.Query{Crops: []string{"rice", "rice", "pigeon_pea"}, Sow: d("2026-06-20"), Today: d("2026-06-01")}
	a := answer(t, p, q)
	if got := ids(a); !slices.Equal(got, []string{"pmfby", apNotice}) {
		t.Fatalf("schemes %v", got)
	}
	ap := item(t, a, apNotice)
	if ap.Cover != schemes.Named || !slices.Equal(ap.Crops, []string{"rice", "pigeon_pea"}) || ap.Status != schemes.NoDate || ap.ForSeason != "kharif 2026" {
		t.Errorf("AP notice %+v", ap)
	}
	if ap.Source.Layer != "Andhra Pradesh" || ap.Source.ID != "dc-ap-insurance-2026" {
		t.Errorf("AP notice source %+v", ap.Source)
	}
	q.Crops = []string{"groundnut"}
	ap = item(t, answer(t, p, q), apNotice)
	if ap.Cover != schemes.NotNamed || ap.Covers() || !strings.Contains(ap.Says, "not among") {
		t.Errorf("groundnut under the AP notice %+v", ap)
	}
}

// A notice for kharif 2026 read for kharif 2027 is last year's dates, not
// a live deadline; for rabi it does not apply at all.
func TestStaleNoticeIsNotALiveDeadline(t *testing.T) {
	t.Parallel()
	p := embedded(t).For(kadapa)
	a := answer(t, p, schemes.Query{Crops: []string{"rice"}, Sow: d("2027-06-20"), Today: d("2027-06-01")})
	ap := item(t, a, apNotice)
	if ap.Status != schemes.Stale || ap.DaysLeft != nil || !strings.Contains(ap.Says, "not yet announced for this season") {
		t.Errorf("stale notice %+v", ap)
	}
	if n, _ := a.LossNotice(); n != 72 {
		t.Errorf("loss notice %d, want PMFBY's 72", n)
	}
	a = answer(t, p, schemes.Query{Crops: []string{"rice"}, Sow: d("2026-11-20"), Today: d("2026-11-16")})
	if got := ids(a); !slices.Equal(got, []string{"pmfby"}) {
		t.Errorf("rabi schemes %v: the kharif notice does not apply", got)
	}
	// A cycle of a season before the notice's year does not get it either.
	a = answer(t, p, schemes.Query{Crops: []string{"rice"}, Sow: d("2025-06-20"), Today: d("2025-06-01")})
	if got := ids(a); !slices.Equal(got, []string{"pmfby"}) {
		t.Errorf("kharif 2025 schemes %v", got)
	}
}

// A stale notice with cut-offs gives them in its own year; an overlay
// that changes PMFBY's crops makes it not cover other crops, and its
// layer is the overlay's.
func TestOverlaySchemes(t *testing.T) {
	t.Parallel()
	set, err := region.Load(mapFS(map[string]string{"bihar.json": `{
		"region": {"country": "IN", "state": "Bihar"},
		"sources": [{"id": "t", "name": "Test notice", "publisher": "us", "url": "https://example.org", "read": "2026-10-01"}],
		"schemes": {
			"pmfby": {"crops": ["maize"], "ref": {"source": "t", "where": "p. 2"}},
			"bihar-2025": {"name": "Bihar kharif 2025", "kind": "insurance", "season": "kharif", "year": 2025,
				"cutoffs": [{"season": "kharif", "what": "enrolment", "by": "07-31"}], "ref": {"source": "t", "where": "p. 3"}}
		}
	}`}))
	if err != nil {
		t.Fatal(err)
	}
	p := set.For(region.Place{Country: "IN", State: "Bihar"})
	a := answer(t, p, schemes.Query{Crops: []string{"rice"}, Sow: d("2026-06-20"), Today: d("2026-06-01")})
	pm := item(t, a, "pmfby")
	if pm.Covers() || pm.Source.Layer != "Bihar" || pm.Source.Read != "2026-10-01" {
		t.Errorf("patched pmfby %+v", pm)
	}
	old := item(t, a, "bihar-2025")
	if old.Status != schemes.Stale || old.By != "2025-07-31" || old.DaysLeft != nil {
		t.Errorf("stale notice with a cut-off %+v", old)
	}
	if n, _ := a.LossNotice(); n != 0 {
		t.Errorf("no covering scheme has a loss rule here, got %d", n)
	}
}

// A cycle with no crops gets no schemes.
func TestNoCropsNoSchemes(t *testing.T) {
	t.Parallel()
	a := answer(t, embedded(t).For(kadapa), schemes.Query{Sow: d("2026-06-20"), Today: d("2026-06-01")})
	if len(a.Items) != 0 {
		t.Errorf("items %+v", a.Items)
	}
}

func mapFS(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range files {
		out[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return out
}
