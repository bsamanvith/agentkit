package sheets_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/google/sheets"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

func TestRangeValidation(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"Sheet1", "Sheet1!A1:D20", "'Q3 costs'!A:C", "'Bob''s tab'", "A1:B2", "Summary!3:9", "Data!B2"} {
		if err := sheets.CheckRange(ok); err != nil {
			t.Errorf("%q: %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "Bob's tab!A1", "'unclosed!A1", "'tab'A1", "Sheet1!A1:B2:C3", "Sheet1!$A$1", "a\nb", strings.Repeat("x", 201)} {
		if err := sheets.CheckRange(bad); err == nil {
			t.Errorf("%q: ok, want refused", bad)
		}
	}
}

func TestNextRange(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		kept int
		want string
	}{
		{"Summary!A1:C400", 100, "'Summary'!A101:C400"},
		{"'Q3 costs'!B5:H", 10, "'Q3 costs'!B15:H"},
		{"A1:C9", 3, "A4:C9"},
		{"Summary", 3, ""},
	} {
		if got := sheets.NextRange(c.in, c.kept); got != c.want {
			t.Errorf("NextRange(%q, %d) = %q, want %q", c.in, c.kept, got, c.want)
		}
	}
}

// fake is a Sheets API that answers reads with rows of cells and records
// the requests and their tokens.
type fake struct {
	mu     sync.Mutex
	tokens []string
	reqs   []*http.Request
	bodies []string
}

func (f *fake) serve(t *testing.T, rows int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.reqs = append(f.reqs, r)
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			_, _ = w.Write([]byte(`{"updatedRange":"Data!A1:B1","updatedRows":1,"updatedCells":2,"updates":{"updatedRange":"Data!A9:B9","updatedRows":1,"updatedCells":2}}`))
			return
		}
		var vals [][]string
		for i := range rows {
			vals = append(vals, []string{fmt.Sprint("a", i+1), fmt.Sprint("b", i+1)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"range": fmt.Sprintf("Data!A1:B%d", rows), "values": vals})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func build(t *testing.T, base, settings string, src credentials.Source) map[string]tool.Tool {
	t.Helper()
	f := sheets.Factory(sheets.WithBaseURL(base), sheets.WithLookupEnv(func(string) (string, bool) { return "env-token", true }))
	tools, err := f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings), Credentials: src})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]tool.Tool{}
	for _, tl := range tools {
		out[tl.Definition().Name] = tl
	}
	return out
}

// A read over max_cells is cut at a whole row, with the range that reads on.
func TestReadCutsAtMaxCells(t *testing.T) {
	t.Parallel()
	srv := (&fake{}).serve(t, 30)
	tools := build(t, srv.URL, `{"max_cells": 21}`, nil)
	raw, err := tools["sheets_read_range"].Invoke(t.Context(), json.RawMessage(`{"spreadsheet_id": "`+budget+`", "range": "Data"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows      [][]string `json:"rows"`
		Truncated bool       `json:"truncated"`
		NextRange string     `json:"next_range"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 10 || !out.Truncated || out.NextRange != "'Data'!A11:B30" {
		t.Errorf("got %d rows, truncated %v, next %q; want 10, true, 'Data'!A11:B30", len(out.Rows), out.Truncated, out.NextRange)
	}
}

// Default settings have only the read tools; writes are medium risk.
func TestGatingFollowsSettings(t *testing.T) {
	t.Parallel()
	names := func(m map[string]tool.Tool) []string {
		var out []string
		for n := range m {
			out = append(out, n)
		}
		slices.Sort(out)
		return out
	}
	if got := names(build(t, "https://sheets.example", `{}`, nil)); !slices.Equal(got, []string{"sheets_get_spreadsheet", "sheets_read_range"}) {
		t.Errorf("default tools = %v", got)
	}
	w := build(t, "https://sheets.example", writeSettings, nil)
	for _, n := range []string{"sheets_append_rows", "sheets_update_range", "sheets_create_spreadsheet"} {
		m := w[n].Definition().Metadata
		if m.Kind != tool.KindWrite || m.Risk != tool.RiskMedium {
			t.Errorf("%s: %s/%s, want write/medium", n, m.Kind, m.Risk)
		}
	}
	scopes, err := sheets.Scopes(json.RawMessage(writeSettings))
	if err != nil || !slices.Equal(scopes, []string{sheets.Scope}) {
		t.Errorf("Scopes(write) = %v, %v", scopes, err)
	}
}

// Append and update send USER_ENTERED and the rows as given.
func TestWritesSendUserEnteredValues(t *testing.T) {
	t.Parallel()
	f := &fake{}
	tools := build(t, f.serve(t, 1).URL, writeSettings, nil)
	in := json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "Data!A1", "rows": [["=1+1", "x"]]}`)
	for _, n := range []string{"sheets_append_rows", "sheets_update_range"} {
		if _, err := tools[n].Invoke(t.Context(), in); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	for i, r := range f.reqs {
		if r.URL.Query().Get("valueInputOption") != "USER_ENTERED" || !strings.Contains(f.bodies[i], `"values":[["=1+1","x"]]`) {
			t.Errorf("request %d: %s %s %s", i, r.Method, r.URL, f.bodies[i])
		}
	}
	if f.reqs[0].Method != http.MethodPost || !strings.HasSuffix(f.reqs[0].URL.Path, ":append") || f.reqs[1].Method != http.MethodPut {
		t.Errorf("methods %s %s, %s", f.reqs[0].Method, f.reqs[0].URL.Path, f.reqs[1].Method)
	}
}

// With a credential slot, each call sends the caller's token.
func TestUsesSlotToken(t *testing.T) {
	t.Parallel()
	f := &fake{}
	src := toolsettest.NewKeySource("sheets")
	src.Set("ann", credentials.OAuth2{AccessToken: credentials.NewSecret("ya29.ann")})
	tools := build(t, f.serve(t, 1).URL, `{}`, src)
	in := json.RawMessage(`{"spreadsheet_id": "` + budget + `"}`)
	if _, err := tools["sheets_get_spreadsheet"].Invoke(toolsettest.AsUser(t, "ann"), in); err != nil {
		t.Fatal(err)
	}
	if _, err := tools["sheets_get_spreadsheet"].Invoke(toolsettest.AsUser(t, "bob"), in); err == nil || !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("bob: %v, want a not-connected failure", err)
	}
	if len(f.tokens) != 1 || f.tokens[0] != "Bearer ya29.ann" {
		t.Errorf("tokens sent %v, want only ann's", f.tokens)
	}
}
