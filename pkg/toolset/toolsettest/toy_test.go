package toolsettest_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

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
		},
		Invalid: []toolsettest.Call{
			{Name: "max_results_too_big", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 99}`)},
			{Name: "bad_id", Tool: "toy_get", Input: json.RawMessage(`{"id": "../admin"}`)},
			{Name: "no_query", Tool: "toy_search", Input: json.RawMessage(`{}`)},
		},
		ErrorBody: toy.ErrorBody,
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
	toolsettest.RunConnector(t, toyConnector(toy.Bug(os.Getenv(bugEnv))))
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
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run="+strings.Join(parts, "/"), "-test.count=1", "-test.timeout=2m")
	// The subprocess must never rewrite golden files or cassettes.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, testkit.UpdateGoldenEnv+"=") && !strings.HasPrefix(kv, toolsettest.RecordEnv+"=") {
			env = append(env, kv)
		}
	}
	env = append(env, bugEnv+"="+string(bug))
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
