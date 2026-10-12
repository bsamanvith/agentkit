package exa_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
)

// Cassettes in T049's format: testdata/cassettes/<name>.req.json is the
// request, <name>.res.http the response as `curl -i` prints it. This file is
// a stand-in for toolsettest.Upstream until T049 lands; delete it then.

// cassetteReq is a cassette's .req.json. Headers are not stored: the rig
// checks the key header on every request itself.
type cassetteReq struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Query  string          `json:"query,omitempty"`
	Body   json.RawMessage `json:"body"`
	// Synthetic marks a cassette built by hand from Exa's documented
	// response schema, not recorded; the task stays in review until it is.
	Synthetic bool `json:"synthetic,omitempty"`
}

func cassettePath(name, ext string) string {
	return filepath.Join("testdata", "cassettes", name+ext)
}

func loadCassette(t testing.TB, name string) (cassetteReq, response) {
	t.Helper()
	var req cassetteReq
	b, err := os.ReadFile(cassettePath(name, ".req.json"))
	if err == nil {
		err = json.Unmarshal(b, &req)
	}
	if err != nil {
		t.Fatalf("cassette %s: %v", name, err)
	}
	raw, err := os.ReadFile(cassettePath(name, ".res.http"))
	if err != nil {
		t.Fatalf("cassette %s: %v", name, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatalf("cassette %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("cassette %s: %v", name, err)
	}
	header := map[string]string{}
	for k := range resp.Header {
		header[k] = resp.Header.Get(k)
	}
	return req, response{status: resp.StatusCode, header: header, body: body}
}

// cassetteBody is a cassette's response body, for fuzz seeds.
func cassetteBody(t testing.TB, name string) []byte {
	t.Helper()
	_, resp := loadCassette(t, name)
	return resp.body
}

// replay makes the upstream answer from the named cassettes. A request
// matches a cassette when method, path and query are equal and the body is
// JSON-equal; anything else fails the test with both bodies. Each cassette
// must be requested exactly once. With `make golden`, a synthetic cassette's
// request body is rewritten instead (a recorded one changes only by
// re-recording).
func (u *upstream) replay(t *testing.T, names ...string) {
	t.Helper()
	type entry struct {
		name string
		req  cassetteReq
		resp response
		used int
	}
	var mu sync.Mutex
	entries := make([]*entry, len(names))
	for i, n := range names {
		req, resp := loadCassette(t, n)
		entries[i] = &entry{name: n, req: req, resp: resp}
	}
	update := os.Getenv(testkit.UpdateGoldenEnv) == "1"
	u.mu.Lock()
	u.respond = func(got recorded) response {
		mu.Lock()
		defer mu.Unlock()
		var closest *entry
		for _, e := range entries {
			if e.req.Method != got.method || e.req.Path != got.path || e.req.Query != got.query {
				continue
			}
			if closest == nil {
				closest = e
			}
			if jsonEqual(e.req.Body, got.body) {
				e.used++
				return e.resp
			}
		}
		if closest != nil && update && closest.req.Synthetic && len(entries) == 1 {
			closest.req.Body = append(json.RawMessage(nil), got.body...)
			writeCassetteReq(t, closest.name, closest.req)
			closest.used++
			return closest.resp
		}
		want := "(no cassette for this method and path)"
		if closest != nil {
			want = indent(closest.req.Body)
		}
		t.Errorf("upstream: %s %s?%s matches no cassette of %v\ngot body:\n%s\nclosest cassette body:\n%s",
			got.method, got.path, got.query, names, indent(got.body), want)
		return response{status: http.StatusTeapot}
	}
	u.mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range entries {
			if e.used != 1 {
				t.Errorf("cassette %s was requested %d times, want once", e.name, e.used)
			}
		}
	})
}

func writeCassetteReq(t testing.TB, name string, req cassetteReq) {
	t.Helper()
	b, err := json.MarshalIndent(req, "", "  ") // keeps the body's key order
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cassettePath(name, ".req.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func indent(b []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, b, "", "  ") != nil {
		return string(b)
	}
	return buf.String()
}

// Every cassette is used by some test, so none goes stale unnoticed.
func TestEveryCassetteIsReplayed(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(cassettePath("*", ".req.json"))
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, f := range files {
		have = append(have, filepath.Base(f[:len(f)-len(".req.json")]))
	}
	var want []string
	for name := range searchCases {
		want = append(want, name)
	}
	for name := range extractCases {
		want = append(want, name)
	}
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		t.Errorf("cassettes %v, want exactly the mapping cases %v", have, want)
	}
}
