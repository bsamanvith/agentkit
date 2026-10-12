package toolsettest

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// MaxCassetteAge is how old a cassette may get before [Drift] warns that it
// should be recorded again.
const MaxCassetteAge = 90 * 24 * time.Hour

// Drift checks that the real API still answers the way the cassettes say. It
// makes each happy call against the real API, as [Record] does, writing
// nothing, and compares the shape of every response with its cassette's:
// JSON paths and their types, not values. An added, removed or retyped field,
// or a different status, fails the test; re-record and review the diff. It
// warns (in the test log) about cassettes recorded over [MaxCassetteAge] ago.
//
// A connector's live test calls it (make test-live):
//
//	//go:build live
//	func TestDrift(t *testing.T) {
//		toolsettest.Drift(t, connector(), toolsettest.Recording{BaseURL: "https://api.tavily.com", LookupEnv: os.LookupEnv})
//	}
func Drift(tb testing.TB, c Connector, rec Recording) {
	tb.Helper()
	c = normalize(tb, c)
	cs, err := loadCassettes(c.Cassettes)
	if err != nil {
		tb.Fatalf("toolsettest: Drift: %v", err)
	}
	byName := map[string]cassette{}
	for _, cas := range cs {
		byName[cas.name] = cas
	}
	l := runLive(tb, c, rec, "Drift")
	for _, lc := range l.calls {
		for i, ex := range lc.exchanges {
			name := lc.call.Cassettes[i]
			cas, ok := byName[name]
			if !ok {
				tb.Errorf("[drift] cassette %s of %s is missing from %s", name, lc.call.Name, c.Cassettes)
				continue
			}
			warnAge(tb, cas)
			status, body, err := cassetteResponse(filepath.Join(c.Cassettes, name+resSuffix))
			if err != nil {
				tb.Errorf("[drift] cassette %s: %v", name, err)
				continue
			}
			if status != ex.status {
				tb.Errorf("[drift] cassette %s: the API answers %d, the cassette %d", name, ex.status, status)
			}
			if diffs := shapeDiff(shapeOf(body), shapeOf(ex.body)); len(diffs) > 0 {
				tb.Errorf("[drift] cassette %s: the response's shape changed (re-record it and review the diff):\n\t%s", name, strings.Join(diffs, "\n\t"))
			}
		}
	}
}

func warnAge(tb testing.TB, c cassette) {
	tb.Helper()
	if c.req.Synthetic {
		tb.Logf("[drift] WARNING: cassette %s is synthetic: record it", c.name)
		return
	}
	day, err := time.Parse(time.DateOnly, c.req.RecordedAt)
	switch {
	case err != nil:
		tb.Logf("[drift] WARNING: cassette %s has no recorded_at: record it again", c.name)
	case time.Since(day) > MaxCassetteAge:
		tb.Logf("[drift] WARNING: cassette %s was recorded %s, over %d days ago: record it again", c.name, c.req.RecordedAt, int(MaxCassetteAge.Hours()/24))
	}
}

// shape maps each JSON path of a value to the types found there: "hits[].title"
// → {"string"}. Nulls are not types: an optional field may be null.
type shape map[string]map[string]bool

func shapeOf(body []byte) shape {
	s := shape{}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		s.add("$", "not JSON")
		return s
	}
	s.walk(v, "$")
	return s
}

func (s shape) add(path, typ string) {
	if s[path] == nil {
		s[path] = map[string]bool{}
	}
	if typ != "" {
		s[path][typ] = true
	}
}

func (s shape) walk(v any, path string) {
	switch v := v.(type) {
	case map[string]any:
		s.add(path, "object")
		for k, fv := range v {
			s.walk(fv, path+"."+k)
		}
	case []any:
		s.add(path, "array")
		for _, e := range v {
			s.walk(e, path+"[]")
		}
	case string:
		s.add(path, "string")
	case float64:
		s.add(path, "number")
	case bool:
		s.add(path, "boolean")
	case nil:
		s.add(path, "")
	}
}

// shapeDiff lists the fields added, removed and retyped from was to now.
func shapeDiff(was, now shape) []string {
	var out []string
	for _, p := range slices.Sorted(maps.Keys(was)) {
		nt, ok := now[p]
		switch {
		case !ok:
			out = append(out, "removed "+p)
		case len(was[p]) > 0 && len(nt) > 0 && !maps.Equal(was[p], nt):
			out = append(out, fmt.Sprintf("retyped %s: %s → %s", p, types(was[p]), types(nt)))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(now)) {
		if _, ok := was[p]; !ok {
			out = append(out, "added "+p)
		}
	}
	return out
}

func types(m map[string]bool) string { return strings.Join(slices.Sorted(maps.Keys(m)), "|") }
