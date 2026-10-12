package toolsettest

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest/internal/toy"
)

// scanCassettes returns, per file, what looks like a secret in every
// testdata/cassettes directory under root.
func scanCassettes(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "bin" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.Contains(filepath.ToSlash(path), "/testdata/cassettes/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if f := findSecrets(data); len(f) > 0 {
			rel, _ := filepath.Rel(root, path)
			found[rel] = f
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// TestNoSecretsInCassettes scans every cassette in the repository for
// key-shaped strings and cookies.
func TestNoSecretsInCassettes(t *testing.T) {
	t.Parallel()
	for file, found := range scanCassettes(t, repoRoot(t)) {
		t.Errorf("%s holds %s: re-record it with Recording.Scrub, or fix it by hand", file, strings.Join(found, "; "))
	}
}

// TestSecretScanFindsSecrets: the scan flags a cassette holding a key shape
// or a cookie, and passes ordinary content.
func TestSecretScanFindsSecrets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "connectors", "x", "testdata", "cassettes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"anthropic.res.http": "HTTP/1.1 200 OK\n\n{\"note\": \"sk-ant-api03-abcdefghijklmnop\"}",
		"tavily.res.http":    "HTTP/1.1 200 OK\n\n{\"note\": \"tvly-dev-0123456789abcdef\"}",
		"field.res.http":     "HTTP/1.1 200 OK\n\n{\"api_key\": \"q8Zr2LmX0vB7nT4kW9yP3sDf\"}",
		"cookie.res.http":    "HTTP/1.1 200 OK\nSet-Cookie: session=abc\n\n{}",
		"bearer.req.json":    `{"method": "GET", "path": "/x", "body": {"auth": "Bearer eyJhbGciOiJIUzI1NiJ9.abc"}}`,
		"clean.res.http":     "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"title\": \"Go tokens and keys\", \"token_count\": 1234, \"next_page_token\": \"CAEQAhoQx8k2Lw9pZr4mB1vT0yN5\", \"api_key_env\": \"TAVILY_API_KEY\"}",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found := scanCassettes(t, root)
	for name := range files {
		rel := filepath.Join("connectors", "x", "testdata", "cassettes", name)
		if got, want := len(found[rel]) > 0, name != "clean.res.http"; got != want {
			t.Errorf("%s: flagged = %v (%v), want %v", name, got, found[rel], want)
		}
	}
}

// TestSkips: Connector.Skip must name a check that may be skipped, with a
// reason.
func TestSkips(t *testing.T) {
	t.Parallel()
	newToy := func(Env) toolset.Factory { return toy.New() }
	for _, tc := range []struct {
		skip map[CheckID]string
		want string
	}{
		{map[CheckID]string{"errors.nope": "why"}, `"errors.nope", which is not a check`},
		{map[CheckID]string{"canary": "why"}, `"canary", which no connector may skip`},
		{map[CheckID]string{"errors.500": " "}, `Skip["errors.500"] gives no reason`},
	} {
		spy := NewSpy(t)
		spy.Run(func(tb testing.TB) { normalize(tb, Connector{New: newToy, Skip: tc.skip}) })
		if !spy.HasErr(tc.want) {
			t.Errorf("Skip %v: errors %q, want one containing %q", tc.skip, spy.Errs(), tc.want)
		}
	}
}

// TestEnable: Connector.Enable must name an opt-in check.
func TestEnable(t *testing.T) {
	t.Parallel()
	newToy := func(Env) toolset.Factory { return toy.New() }
	for _, tc := range []struct {
		enable CheckID
		want   string
	}{
		{"schema_honesty.nope", `"schema_honesty.nope", which is not a check`},
		{"errors.500", `"errors.500", which is not opt-in`},
		{"canary", `"canary", which is not opt-in`},
	} {
		spy := NewSpy(t)
		spy.Run(func(tb testing.TB) { normalize(tb, Connector{New: newToy, Enable: []CheckID{tc.enable}}) })
		if !spy.HasErr(tc.want) {
			t.Errorf("Enable %s: errors %q, want one containing %q", tc.enable, spy.Errs(), tc.want)
		}
	}
}

// TestOpenAPICheckNeedsTheSpec: check cassettes.openapi, enabled without
// Connector.OpenAPI, fails rather than passing on nothing.
func TestOpenAPICheckNeedsTheSpec(t *testing.T) {
	t.Parallel()
	c := Connector{
		New:     func(Env) toolset.Factory { return toy.New() },
		Calls:   []Call{{Name: "c", Tool: "toy_search", Cassettes: []string{"search_go"}}},
		Invalid: []Call{{Name: "i", Tool: "toy_search"}},
		Enable:  []CheckID{"cassettes.openapi"},
	}
	spy := NewSpy(t)
	spy.Run(func(tb testing.TB) { newSuite(tb, c, false).openapiCheck(tb) })
	if !spy.HasErr("check cassettes.openapi needs Connector.OpenAPI") {
		t.Errorf("errors %q, want one asking for Connector.OpenAPI", spy.Errs())
	}
}

// TestChecksTable: every check has a unique ID, and docs/CONNECTORS.md
// lists each one with its stage.
func TestChecksTable(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "CONNECTORS.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[CheckID]bool{}
	for _, c := range checks {
		if seen[c.id] {
			t.Errorf("check %s is listed twice", c.id)
		}
		seen[c.id] = true
		st := map[stage]string{optIn: "opt-in", optOut: "opt-out", mandatory: "mandatory"}[c.stage]
		if row := "| `" + string(c.id) + "` | " + st + " |"; !strings.Contains(string(doc), row) {
			t.Errorf("docs/CONNECTORS.md has no row %q", row)
		}
	}
}

// TestSyntheticCassettesAreListed: the suite lists the synthetic cassettes.
func TestSyntheticCassettesAreListed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join("testdata", "cassettes")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() == "search_nothing"+reqSuffix {
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				t.Fatal(err)
			}
			req["synthetic"] = true
			b, _ = json.Marshal(req)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := Connector{
		New:       func(Env) toolset.Factory { return toy.New() },
		Cassettes: dir,
		Calls:     []Call{{Name: "search_nothing", Tool: "toy_search", Input: json.RawMessage(`{"query": "cobol"}`), Cassettes: []string{"search_nothing"}}},
		Invalid:   []Call{{Name: "no_query", Tool: "toy_search", Input: json.RawMessage(`{}`)}},
	}
	spy := NewSpy(t)
	spy.Run(func(tb testing.TB) { newSuite(tb, c, false).reportSynthetic(tb) })
	if log := spy.LogText(); !containsAll(log, "synthetic cassettes", "search_nothing") || strings.Contains(log, "search_go") {
		t.Errorf("logged %q, want the synthetic cassette search_nothing listed, and only it", log)
	}
	if errs := spy.Errs(); len(errs) > 0 {
		t.Errorf("errors: %q", errs)
	}
}

func TestShapeDiff(t *testing.T) {
	t.Parallel()
	was := shapeOf([]byte(`{"hits": [{"title": "a", "score": 1, "tags": null}], "took_ms": 3}`))
	now := shapeOf([]byte(`{"hits": [{"title": "b", "score": "1", "tags": ["x"], "lang": "en"}], "took_ms": 4}`))
	got := shapeDiff(was, now)
	want := []string{"retyped $.hits[].score: number → string", "added $.hits[].lang", "added $.hits[].tags[]"}
	if !slices.Equal(got, want) {
		t.Errorf("shapeDiff = %q, want %q", got, want)
	}
	if d := shapeDiff(was, was); len(d) != 0 {
		t.Errorf("shapeDiff of a shape with itself = %q, want none", d)
	}
}

// OptOutChecks lists the checks a connector may skip, for the external
// tests.
func OptOutChecks() []CheckID { return checksAt(optOut) }

// OptInChecks lists the checks a connector must enable, for the external
// tests.
func OptInChecks() []CheckID { return checksAt(optIn) }

func checksAt(st stage) []CheckID {
	var ids []CheckID
	for _, c := range checks {
		if c.stage == st {
			ids = append(ids, c.id)
		}
	}
	return ids
}
