package toolsettest

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/internal/testkit"
)

// honored: each setting call's requests are golden, and differ from the
// requests the same input sends without the call's settings, so a setting
// that is accepted and ignored fails even on a golden written from it.
func (s *suite) honored(t *testing.T) {
	if len(s.c.SettingCalls) == 0 {
		t.Fatal("Connector.SettingCalls is empty: add a call per setting that changes the request (a search depth, a mode), or skip settings.honored with a reason")
	}
	for _, call := range s.c.SettingCalls {
		t.Run(call.Name, func(t *testing.T) {
			t.Parallel()
			with := s.requestsSent(t, call, s.settingsFor(call))
			without := s.requestsSent(t, call, s.c.Settings)
			got, err := json.MarshalIndent(with, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			testkit.Golden(t, requestGoldenPath(call), append(got, '\n'))
			if jsonEqual(mustMarshal(t, with), mustMarshal(t, without)) {
				t.Errorf("[settings] %s sent the same request with and without its settings %s: the setting is accepted and ignored", call.Name, call.Settings)
			}
		})
	}
}

// requestsSent makes call with the tools built from settings, answering each
// request with the tool's first cassette, and returns the requests it sent
// (method, path, query and body; the body decoded, so key order is not part
// of it).
func (s *suite) requestsSent(t *testing.T, call Call, settings json.RawMessage) []map[string]any {
	t.Helper()
	r := s.newRig(t, modeKeyed)
	tools, err := r.build(settings)
	if err != nil {
		t.Fatalf("Build(%s) for %s: %v", settings, call.Name, err)
	}
	status, body := s.firstCassette(t, call.Tool)
	for range 8 {
		r.up.Script(Response{Status: status, Header: jsonHeader(), Body: body})
	}
	if _, err := r.invoke(t.Context(), tools, call.Tool, call.Input); err != nil {
		t.Logf("%s with settings %s: %v (only its requests are checked)", call.Name, settings, err)
	}
	var reqs []map[string]any
	for _, req := range r.up.Requests() {
		reqs = append(reqs, comparable(stored(req)))
	}
	if len(reqs) == 0 {
		t.Fatalf("%s with settings %s sent no request", call.Name, settings)
	}
	return reqs
}

// firstCassette returns the status and body of the first cassette of the
// first happy call of tool.
func (s *suite) firstCassette(t *testing.T, tool string) (int, []byte) {
	t.Helper()
	for _, c := range s.c.Calls {
		if c.Tool == tool {
			status, body, err := cassetteResponse(filepath.Join(s.c.Cassettes, c.Cassettes[0]+resSuffix))
			if err != nil {
				t.Fatalf("toolsettest: %v", err)
			}
			return status, body
		}
	}
	t.Fatalf("no happy call of %s to answer a setting call with", tool)
	return 0, nil
}

func requestGoldenPath(call Call) string {
	return filepath.Join("testdata", "golden", call.Name+".request.json")
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// runeUnit is the text limits.rune_boundary fills content with: a 2-, a 3-
// and a 4-byte character. A cut by bytes keeps about a third as many
// characters, and splits one wherever it falls.
const runeUnit = "é語😀"

// limits runs the limits checks.
func (s *suite) limits(t *testing.T) {
	s.run(t, "limits.rune_boundary", s.runeBoundary)
}

// runeBoundary: content over the limit is cut at a character boundary, keeps
// exactly Limit characters, and ends with the marker.
func (s *suite) runeBoundary(t *testing.T) {
	t.Parallel()
	tr := s.c.Truncation
	if tr == nil {
		t.Fatal("Connector.Truncation is not set: say where a call's content is and how to set its limit, or skip limits.rune_boundary with a reason")
	}
	i := slices.IndexFunc(s.c.Calls, func(c Call) bool { return c.Name == tr.Call })
	if i < 0 {
		t.Fatalf("Truncation.Call %q is not a happy call", tr.Call)
	}
	call := s.c.Calls[i]
	content := strings.Repeat(runeUnit, tr.Limit+64)

	r := s.newRig(t, modeKeyed)
	settings := mergeSettings(s.settingsFor(call), tr.Settings)
	tools, err := r.build(settings)
	if err != nil {
		t.Fatalf("Build(%s): %v", settings, err)
	}
	for i, name := range call.Cassettes {
		status, body, err := cassetteResponse(filepath.Join(s.c.Cassettes, name+resSuffix))
		if err != nil {
			t.Fatalf("toolsettest: %v", err)
		}
		if i == 0 {
			if body, err = setPointer(body, tr.Field, content); err != nil {
				t.Fatalf("Truncation.Field %s in cassette %s: %v", tr.Field, name, err)
			}
		}
		r.up.Script(Response{Status: status, Header: jsonHeader(), Body: body})
	}
	out, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
	if err != nil {
		t.Fatalf("%s with %d characters of content at %s: %v", call.Name, utf8.RuneCountInString(content), tr.Field, err)
	}
	var found bool
	for _, str := range outputStrings(out) {
		if !strings.HasPrefix(str, runeUnit[:2]) { // é: the content's first character
			continue
		}
		found = true
		if err := checkCut(str, content, tr); err != nil {
			t.Errorf("[limits] %s: %v\n%q", call.Name, err, str)
		}
	}
	if !found {
		t.Errorf("[limits] no string in the output of %s starts with the content put at %s:\n%s", call.Name, tr.Field, out)
	}
}

// checkCut checks one cut content: the first Limit characters of content,
// whole, then the marker.
func checkCut(got, content string, tr *Truncation) error {
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		return fmt.Errorf("the cut content is not valid UTF-8 (a U+FFFD shows where JSON encoding replaced a split character): cut at a character boundary")
	}
	kept := 0 // bytes of got that match content
	for kept < len(got) && kept < len(content) && got[kept] == content[kept] {
		kept++
	}
	for kept > 0 && kept < len(content) && !utf8.RuneStart(content[kept]) {
		kept-- // back to the last whole character
	}
	if n := utf8.RuneCountInString(got[:kept]); n != tr.Limit {
		return fmt.Errorf("the cut content keeps %d characters, want the limit, %d", n, tr.Limit)
	}
	if rest := got[kept:]; !tr.Marker.MatchString(rest) {
		return fmt.Errorf("the cut content ends with %q, want the marker matching %s", rest, tr.Marker)
	}
	return nil
}

// outputStrings returns every string in a JSON value.
func outputStrings(out []byte) []string {
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil
	}
	var strs []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			strs = append(strs, v)
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(v)
	return strs
}

// setPointer sets the value at a JSON pointer (RFC 6901) in a JSON document.
// The value's parent must exist.
func setPointer(doc []byte, pointer string, value any) ([]byte, error) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("the body is not JSON: %w", err)
	}
	tokens := strings.Split(pointer, "/")[1:]
	for i, tok := range tokens {
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
	}
	parent := root
	for i, tok := range tokens {
		last := i == len(tokens)-1
		switch p := parent.(type) {
		case map[string]any:
			if last {
				p[tok] = value
				break
			}
			parent = p[tok]
		case []any:
			n, err := strconv.Atoi(tok)
			if err != nil || n < 0 || n >= len(p) {
				return nil, fmt.Errorf("no element %q in an array of %d", tok, len(p))
			}
			if last {
				p[n] = value
				break
			}
			parent = p[n]
		default:
			return nil, fmt.Errorf("%q is not inside an object or an array", "/"+strings.Join(tokens[:i+1], "/"))
		}
	}
	return json.Marshal(root)
}
