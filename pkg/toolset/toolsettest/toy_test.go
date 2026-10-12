package toolsettest_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest/internal/toy"
)

// bugEnv selects a planted bug in the toy connector for TestToyConnector and
// TestToyConnectorAuto; TestPlantedBugs sets it in a subprocess.
const bugEnv = "TOOLSETTEST_TOY_BUG"

// toyKey is the key of the toy reference server the cassettes are recorded
// from. It never appears in them.
const toyKey = "toy-reference-key-0123456789"

func newToy(bug toy.Bug) func(toolsettest.Env) toolset.Factory {
	return func(e toolsettest.Env) toolset.Factory {
		return toy.New(toy.WithBaseURL(e.BaseURL), toy.WithHTTPClient(e.HTTPClient), toy.WithLookupEnv(e.LookupEnv),
			toy.WithClock(e.Clock), toy.WithLogger(e.Logger), toy.WithBug(bug))
	}
}

// toyConnector is the toy as a connector's test describes itself to the suite.
func toyConnector(bug toy.Bug) toolsettest.Connector {
	return toolsettest.Connector{
		New:      newToy(bug),
		Settings: json.RawMessage(`{"api_key_env": "TOY_API_KEY"}`),
		KeyEnv:   "TOY_API_KEY",
		Calls: []toolsettest.Call{
			{Name: "search_go", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 2}`), Cassettes: []string{"search_go"}},
			{Name: "search_nothing", Tool: "toy_search", Input: json.RawMessage(`{"query": "cobol"}`), Cassettes: []string{"search_nothing"}},
			{Name: "get_go1", Tool: "toy_get", Input: json.RawMessage(`{"id": "go1"}`), Cassettes: []string{"get_go1"}},
			// summary is filled only under with_summary.
			{Name: "search_summary", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 2}`), Cassettes: []string{"search_summary"}, Settings: json.RawMessage(`{"with_summary": true}`)},
		},
		Invalid: []toolsettest.Call{
			{Name: "max_results_too_big", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 99}`)},
			{Name: "bad_id", Tool: "toy_get", Input: json.RawMessage(`{"id": "../admin"}`)},
			{Name: "no_query", Tool: "toy_search", Input: json.RawMessage(`{}`)},
		},
		ErrorBody: toy.ErrorBody,
		OpenAPI:   "testdata/openapi.json",
		Enable:    []toolsettest.CheckID{"schema_honesty.dead_fields", "classified"},
		SettingCalls: []toolsettest.Call{
			{Name: "with_summary", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 2}`), Settings: json.RawMessage(`{"with_summary": true}`)},
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
		Truncation: &toolsettest.Truncation{
			Call: "search_go", Field: "/hits/0/snippet", Settings: json.RawMessage(`{"max_content_chars": 40}`), Limit: 40,
		},
	}
}

// toyAuto is the toy in auto mode: keyed when the key is set, else keyless.
func toyAuto(bug toy.Bug) toolsettest.Connector {
	c := toyConnector(bug)
	c.Settings = json.RawMessage(`{"api_key_env": "TOY_API_KEY", "auth": "auto"}`)
	c.KeyOptional = true
	c.KeylessMarker = http.Header{toy.KeylessHeader: {"keyless"}}
	return c
}

// TestToyConnector: the toy, a correct connector, passes the suite.
func TestToyConnector(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, withVariant(toyConnector(toy.Bug(os.Getenv(bugEnv)))))
}

// variantEnv changes the toy's test, for TestCallSettings.
const variantEnv = "TOOLSETTEST_TOY_VARIANT"

// withVariant drops the search_summary call ("no_summary_call") or its own
// settings ("no_call_settings").
func withVariant(c toolsettest.Connector) toolsettest.Connector {
	v := os.Getenv(variantEnv)
	var calls []toolsettest.Call
	for _, call := range c.Calls {
		switch {
		case call.Name != "search_summary":
		case v == "no_summary_call":
			continue
		case v == "no_call_settings":
			call.Settings = nil
		}
		calls = append(calls, call)
	}
	c.Calls = calls
	return c
}

// TestCallSettings: summary, filled only under with_summary, is covered by
// the call with its own settings: without that call dead_fields names it, and
// without the call's settings the call no longer matches its cassette.
func TestCallSettings(t *testing.T) {
	t.Parallel()
	if os.Getenv(variantEnv) != "" {
		t.Skip("running as a variant subprocess")
	}
	for _, tc := range []struct{ variant, check, want string }{
		{"no_summary_call", "TestToyConnector/schema_honesty/dead_fields", "no cassette output has a non-null summary"},
		{"no_call_settings", "TestToyConnector/calls/search_summary", "search_summary"},
	} {
		t.Run(tc.variant, func(t *testing.T) {
			t.Parallel()
			run := "-test.run=^" + strings.ReplaceAll(tc.check, "/", "$/^") + "$"
			out, err := runTest(t, []string{variantEnv + "=" + tc.variant}, run)
			if err == nil || !strings.Contains(out, "--- FAIL: "+tc.check+" ") || !strings.Contains(out, tc.want) {
				t.Errorf("with %s, %s must fail mentioning %q (err %v):\n%s", tc.variant, tc.check, tc.want, err, out)
			}
		})
	}
}

// TestToyConnectorAuto: in auto mode it passes too, keyless calls included.
func TestToyConnectorAuto(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, toyAuto(toy.Bug(os.Getenv(bugEnv))))
}

func FuzzToySearchResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, toyConnector(toy.BugNone), "toy_search")
}

func FuzzToyGetResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, toyConnector(toy.BugNone), "toy_get")
}

// TestRecordCassettes records the toy's cassettes from its reference server
// (make record-cassettes).
func TestRecordCassettes(t *testing.T) {
	toolsettest.SkipUnlessRecording(t)
	srv := httptest.NewServer(toy.NewServer(toyKey))
	t.Cleanup(srv.Close)
	toolsettest.Record(t, toyConnector(toy.BugNone), toolsettest.Recording{BaseURL: srv.URL, LookupEnv: toyEnv})
}

func toyEnv(name string) (string, bool) {
	if name == "TOY_API_KEY" {
		return toyKey, true
	}
	return "", false
}

// plantedBugs maps each planted bug to the check meant to catch it.
var plantedBugs = map[toy.Bug]string{
	toy.BugLeakKeyInError:        "TestToyConnector/canary",
	toy.BugLogKey:                "TestToyConnector/canary",
	toy.BugRetry500:              "TestToyConnector/errors/500",
	toy.BugIgnoreHTTPClient:      "TestToyConnector/hermetic",
	toy.BugAcceptUnknownSettings: "TestToyConnector/settings/unknown_field",
	toy.BugNoOutputSchema:        "TestToyConnector/definitions",
	toy.BugRequestOnInvalid:      "TestToyConnector/invalid/max_results_too_big",
	toy.BugUnboundedBody:         "TestToyConnector/errors/oversized_body",
	toy.BugIgnoreCtx:             "TestToyConnector/errors/hang",
	toy.BugKeyInQuery:            "TestToyConnector/calls/search_go",
	toy.BugRequestInBuild:        "TestToyConnector/definitions",
	toy.BugRetryIgnoresAfter:     "TestToyConnector/errors/429_retry_after",
	toy.Bug400AsFailure:          "TestToyConnector/errors/400",
	toy.BugIgnoreBaseURLSetting:  "TestToyConnector/settings/base_url",
	toy.BugKeylessNoMarker:       "TestToyConnectorAuto/keyless/search_go",
	toy.BugAcceptPlainHTTP:       "TestToyConnector/settings/plain_http_base_url",
	toy.BugDeadOutputField:       "TestToyConnector/schema_honesty/dead_fields",
	toy.BugUndeclaredOutputField: "TestToyConnector/schema_honesty/undeclared",
	// Only on a retry, so only the log check run after every subtest sees it.
	toy.BugLogKeyOnRetry: "TestToyConnector/errors/429_then_200",
	// The retry waits on the clock, which errors/500 never advances: there
	// the call sends one request and fails only at the watchdog, and a
	// connector whose own timeout ends the wait first passes errors/500.
	toy.BugRetry5xxAfterWait:    "TestToyConnector/errors/no_retry_5xx",
	toy.BugWaitIgnoresCtx:       "TestToyConnector/errors/retry_wait_ctx",
	toy.BugIgnoreTimeout:        "TestToyConnector/errors/attempt_timeout",
	toy.BugCutBytes:             "TestToyConnector/limits/rune_boundary",
	toy.BugIgnoreSummarySetting: "TestToyConnector/settings/honored",
	toy.BugNoRisk:               "TestToyConnector/classified",
	// Only under with_summary, which only the call search_summary uses.
	toy.BugNoKindUnderSetting: "TestToyConnector/classified",
}

// TestPlantedBugs: each planted bug makes the suite fail, on the check meant
// to catch it. Each runs the suite in a subprocess (this test binary, with
// the bug selected by bugEnv), restricted to that check, so a check that
// passed a broken connector would fail this test.
func TestPlantedBugs(t *testing.T) {
	t.Parallel()
	if os.Getenv(bugEnv) != "" {
		t.Skip("running as a planted-bug subprocess")
	}
	for bug, check := range plantedBugs {
		t.Run(string(bug), func(t *testing.T) {
			t.Parallel()
			out, err := runSuite(t, bug, check)
			if err == nil {
				t.Fatalf("with bug %s, %s passed; it must fail:\n%s", bug, check, out)
			}
			if !strings.Contains(out, "--- FAIL: "+check+" ") {
				t.Fatalf("with bug %s, %s did not fail:\n%s", bug, check, out)
			}
		})
	}
}

func TestEveryPlantedBugIsChecked(t *testing.T) {
	t.Parallel()
	for _, b := range toy.Bugs {
		if _, ok := plantedBugs[b]; !ok {
			t.Errorf("bug %s has no check in plantedBugs", b)
		}
	}
}

// runSuite runs check (a test/subtest path) in a subprocess with bug planted.
func runSuite(t *testing.T, bug toy.Bug, check string) (string, error) {
	t.Helper()
	parts := strings.Split(check, "/")
	for i, p := range parts {
		parts[i] = "^" + p + "$"
	}
	return runTest(t, []string{bugEnv + "=" + string(bug)}, "-test.run="+strings.Join(parts, "/"))
}

// runTest runs this test binary in a subprocess with env added and args.
func runTest(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	return runTestIn(t, "", env, args...)
}

// runTestIn is runTest in directory dir ("" for this one).
func runTestIn(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.count=1", "-test.timeout=2m"}, args...)...)
	cmd.Dir = dir
	// The subprocess must never rewrite golden files or cassettes.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, testkit.UpdateGoldenEnv+"=") && !strings.HasPrefix(kv, toolsettest.RecordEnv+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestGoldenFirstRun: make golden on a connector with no golden files yet
// writes them all in one run, though settings.base_url reads one before calls
// writes it.
func TestGoldenFirstRun(t *testing.T) {
	t.Parallel()
	if os.Getenv(testkit.UpdateGoldenEnv) == "1" {
		t.Skip("under make golden, TestToyConnector rewrites the committed goldens this test compares with")
	}
	dir := t.TempDir()
	// All of testdata but the goldens, which the run must write.
	if err := os.CopyFS(filepath.Join(dir, "testdata"), os.DirFS("testdata")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "testdata", "golden")); err != nil {
		t.Fatal(err)
	}
	out, err := runTestIn(t, dir, []string{testkit.UpdateGoldenEnv + "=1"}, "-test.run=^TestToyConnector$")
	if err != nil {
		t.Fatalf("the first make golden failed: %v\n%s", err, out)
	}
	want, err := os.ReadDir(filepath.Join("testdata", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range want {
		w, _ := os.ReadFile(filepath.Join("testdata", "golden", e.Name()))
		got, err := os.ReadFile(filepath.Join(dir, "testdata", "golden", e.Name()))
		if err != nil || !bytes.Equal(got, w) {
			t.Errorf("golden %s after the first make golden = %q, %v; want the committed file", e.Name(), got, err)
		}
	}
}

// skipEnv makes TestToyConnectorSkips run; TestSkipsAreReported sets it in a
// subprocess.
const skipEnv = "TOOLSETTEST_TOY_SKIP"

// TestToyConnectorSkips runs the suite with every check that may be skipped
// skipped, and no opt-in check enabled.
func TestToyConnectorSkips(t *testing.T) {
	if os.Getenv(skipEnv) == "" {
		t.Skip("run by TestSkipsAreReported")
	}
	c := toyConnector(toy.BugNone)
	c.Enable = nil
	c.Skip = map[toolsettest.CheckID]string{}
	for _, id := range toolsettest.OptOutChecks() {
		c.Skip[id] = "toy: the reason for " + string(id)
	}
	toolsettest.RunConnector(t, c)
}

// TestSkipsAreReported: each skipped check is reported, as its subtest, with
// the connector's reason; so is each opt-in check left out.
func TestSkipsAreReported(t *testing.T) {
	t.Parallel()
	out, err := runTest(t, []string{skipEnv + "=1"}, "-test.run=^TestToyConnectorSkips$", "-test.v")
	if err != nil {
		t.Fatalf("the suite with skips failed: %v\n%s", err, out)
	}
	for _, id := range toolsettest.OptOutChecks() {
		path := "TestToyConnectorSkips/" + strings.ReplaceAll(string(id), ".", "/")
		if !strings.Contains(out, "--- SKIP: "+path+" ") || !strings.Contains(out, "skipped by Connector.Skip: toy: the reason for "+string(id)) {
			t.Errorf("check %s is not reported skipped with its reason (as %s)", id, path)
		}
	}
	for _, id := range toolsettest.OptInChecks() {
		path := "TestToyConnectorSkips/" + strings.ReplaceAll(string(id), ".", "/")
		if !strings.Contains(out, "--- SKIP: "+path+" ") || !strings.Contains(out, "check "+string(id)+" is opt-in") {
			t.Errorf("opt-in check %s is not reported skipped (as %s)", id, path)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
}
