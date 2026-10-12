package toolsettest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// DefaultMaxBodyBytes is the response body limit the suite assumes when
// [Connector.MaxBodyBytes] is zero: 8 MiB, the limit E10 sets for connectors.
const DefaultMaxBodyBytes = 8 << 20

// Env is what the suite wires a connector's factory to. A connector's test
// turns it into the factory's options:
//
//	New: func(e toolsettest.Env) toolset.Factory {
//		return tavily.New(tavily.WithBaseURL(e.BaseURL), tavily.WithHTTPClient(e.HTTPClient),
//			tavily.WithLookupEnv(e.LookupEnv), tavily.WithClock(e.Clock), tavily.WithLogger(e.Logger))
//	},
type Env struct {
	// BaseURL is the upstream's URL. It is "" when the suite checks the
	// base_url setting: the factory then uses its default unless the setting
	// says otherwise, so WithBaseURL("") must mean "the default".
	BaseURL string
	// HTTPClient reaches the upstream and refuses every other host.
	HTTPClient *http.Client
	// LookupEnv returns a canary value for [Connector.KeyEnv] (or nothing,
	// when the suite checks a missing key or keyless mode). It never reads
	// the process environment.
	LookupEnv func(string) (string, bool)
	// Clock is a fake clock the suite advances to test retries.
	Clock clock.Clock
	// Logger captures everything, debug level included, to check that the key
	// never appears in a log line.
	Logger *slog.Logger
}

// Call is one tool call the suite makes.
type Call struct {
	// Name names the call's subtest and its golden file,
	// testdata/golden/<Name>.json. Use lowercase letters, digits and _.
	Name string
	// Tool is the tool to call; Input is its input.
	Tool  string
	Input json.RawMessage
	// Cassettes are the exchanges the call must request, by name (the
	// cassette files are <name>.req.json and <name>.res.http). Empty for an
	// invalid call, which must make no request.
	Cassettes []string
	// Settings, when set, is merged over Connector.Settings for this call:
	// each of its fields replaces the connector's, and the suite builds the
	// tools with the result. Use it for output a setting turns on (an answer,
	// extra fields), so schema_honesty.dead_fields sees it.
	Settings json.RawMessage
}

// Connector describes a connector to [RunConnector].
type Connector struct {
	// New returns the factory under test, wired to env.
	New func(env Env) toolset.Factory
	// Settings is a valid configuration (a JSON object, or empty).
	Settings json.RawMessage
	// KeyEnv is the environment variable the settings name for the API key;
	// "" for a connector that needs none.
	KeyEnv string
	// KeyOptional is true for a connector that also runs keyless (Tavily's
	// auto mode): without the key, it must send KeylessMarker and no key.
	KeyOptional bool
	// KeylessMarker is the header(s) a keyless request carries instead of
	// the key. Required with KeyOptional.
	KeylessMarker http.Header
	// Cassettes is the cassette directory (default testdata/cassettes).
	Cassettes string
	// Calls are the happy paths, each answered from cassettes, its output
	// compared with testdata/golden/<Name>.json. Every tool needs at least
	// one. The error matrix runs on the first.
	Calls []Call
	// Invalid are inputs that must fail with tool.ErrInvalidInput and make no
	// request. Include inputs the JSON schema accepts but the connector must
	// refuse (max_results over the limit, a non-http URL): tool.Func already
	// rejects input the schema does not accept, before any connector code runs.
	Invalid []Call

	// ErrorBody formats an error response body carrying message the way the
	// provider does, so the suite can check that a 400's message reaches the
	// model. Default: {"error": message}.
	ErrorBody func(message string) []byte
	// MaxBodyBytes is the connector's response body limit (default
	// [DefaultMaxBodyBytes]).
	MaxBodyBytes int64
	// Skip skips checks by ID, each with the reason, which every run reports
	// (go test -v shows it). Some checks cannot be skipped; naming one, or
	// an ID that is not a check, fails the suite. docs/CONNECTORS.md lists
	// the checks and lists every skip in the repository.
	Skip map[CheckID]string
	// Enable runs opt-in checks: new checks start opt-in, so they do not
	// break connectors already on main, and become opt-out once every
	// connector passes them. docs/CONNECTORS.md lists them.
	Enable []CheckID
}

var callName = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// suite is a validated Connector and how to run it.
type suite struct {
	c      Connector
	fake   bool // RunFake: only the checks that apply to a fake
	canary string
}

func newSuite(tb testing.TB, c Connector, fake bool) *suite {
	tb.Helper()
	c = normalize(tb, c)
	checkCalls(tb, c, !fake)
	return &suite{c: c, fake: fake, canary: "toolsettest-canary-" + newToken()}
}

// normalize fills in defaults and checks the fields that do not depend on
// cassettes.
func normalize(tb testing.TB, c Connector) Connector {
	tb.Helper()
	if c.New == nil {
		tb.Fatal("toolsettest: Connector.New is nil")
	}
	if c.Cassettes == "" {
		c.Cassettes = filepath.Join("testdata", "cassettes")
	}
	if c.ErrorBody == nil {
		c.ErrorBody = func(m string) []byte {
			b, _ := json.Marshal(map[string]string{"error": m})
			return b
		}
	}
	checkSkips(tb, c.Skip)
	checkEnable(tb, c.Enable)
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if len(bytes.TrimSpace(c.Settings)) == 0 {
		c.Settings = json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(c.Settings, &obj); err != nil || obj == nil {
		tb.Fatalf("toolsettest: Connector.Settings must be a JSON object: %s", c.Settings)
	}
	for _, call := range slices.Concat(c.Calls, c.Invalid) {
		if len(call.Settings) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(call.Settings, &obj); err != nil || obj == nil {
			tb.Fatalf("toolsettest: Call %s: Settings must be a JSON object: %s", call.Name, call.Settings)
		}
	}
	if c.KeyOptional && (c.KeyEnv == "" || len(c.KeylessMarker) == 0) {
		tb.Fatal("toolsettest: KeyOptional needs KeyEnv and KeylessMarker")
	}
	if len(c.Calls) == 0 {
		tb.Fatal("toolsettest: Connector.Calls is empty: the suite needs at least one happy call per tool")
	}
	if len(c.Invalid) == 0 {
		tb.Fatal("toolsettest: Connector.Invalid is empty: give at least one input the connector must refuse")
	}
	seen := map[string]bool{}
	for _, calls := range [][]Call{c.Calls, c.Invalid} {
		for _, call := range calls {
			if !callName.MatchString(call.Name) || seen[call.Name] {
				tb.Fatalf("toolsettest: call name %q must be unique and match %s", call.Name, callName)
			}
			seen[call.Name] = true
		}
	}
	for _, call := range c.Invalid {
		if len(call.Cassettes) != 0 {
			tb.Fatalf("toolsettest: invalid call %s lists cassettes; it must make no request", call.Name)
		}
	}
	return c
}

// checkCalls checks that every happy call lists cassettes and, if files is
// set, that they exist.
func checkCalls(tb testing.TB, c Connector, files bool) {
	tb.Helper()
	for _, call := range c.Calls {
		if !files {
			continue
		}
		if len(call.Cassettes) == 0 {
			tb.Fatalf("toolsettest: call %s lists no cassettes", call.Name)
		}
		for _, name := range call.Cassettes {
			for _, suffix := range []string{reqSuffix, resSuffix} {
				if _, err := os.Stat(filepath.Join(c.Cassettes, name+suffix)); err != nil {
					tb.Fatalf("toolsettest: call %s: cassette %s: %v (record it: make record-cassettes)", call.Name, name, err)
				}
			}
		}
	}
}

// RunConnector runs the conformance suite against a connector. Every
// connector passes it with one call:
//
//	func TestConformance(t *testing.T) { toolsettest.RunConnector(t, connector()) }
//
// Its subtests are a contract, each a check with a [CheckID]
// (docs/CONNECTORS.md lists what each checks): definitions;
// settings/{unknown_field,literal_api_key,missing_key,plain_http_base_url,
// base_url}; calls/<call>; keyless/<call> (KeyOptional only);
// invalid/<call>; errors/{400,422,401,403,500,502,503,429_retry_after,
// 429_then_200,malformed_json,oversized_body,hang,connection_refused};
// schema_honesty/{undeclared,dead_fields}; canary; hermetic; mcp; concurrency.
//
// After every subtest, whatever it checked, the suite also checks that the key
// appeared in no output, error or log line (debug level included), that every
// keyed request carried it in exactly one header and nowhere else, and that
// every request went through Env.HTTPClient to the upstream.
func RunConnector(t *testing.T, c Connector) {
	t.Helper()
	s := newSuite(t, c, false)
	s.reportSynthetic(t)
	s.run(t, "definitions", s.definitions)
	t.Run("settings", s.settings)
	s.run(t, "calls", func(t *testing.T) { s.calls(t, modeKeyed) })
	if s.c.KeyOptional {
		s.run(t, "keyless", func(t *testing.T) { s.calls(t, modeKeyless) })
	}
	s.run(t, "invalid", s.invalid)
	t.Run("errors", s.errorMatrix)
	t.Run("schema_honesty", s.schemaHonesty)
	s.run(t, "canary", s.canaryCheck)
	s.run(t, "hermetic", s.hermeticCheck)
	s.run(t, "mcp", s.mcpCheck)
	s.run(t, "concurrency", s.concurrency)
}

// RunFake runs the parts of the suite that apply to a fake ([FakeFactory]):
// definitions, calls (golden output, output schema, no request), invalid,
// mcp and concurrency, with the same after-every-subtest checks. It is how a
// fake proves it has not drifted from the connector it stands for, as
// llmtest.RunModel runs against llmtest.Fake. Connectors call
// [RunConnector]; RunFake does not count for them.
func RunFake(t *testing.T, c Connector) {
	t.Helper()
	s := newSuite(t, c, true)
	s.run(t, "definitions", s.definitions)
	s.run(t, "calls", func(t *testing.T) { s.calls(t, modeKeyed) })
	s.run(t, "invalid", s.invalid)
	s.run(t, "mcp", s.mcpCheck)
	s.run(t, "concurrency", s.concurrency)
}
