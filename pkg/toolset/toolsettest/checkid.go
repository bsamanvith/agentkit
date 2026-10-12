package toolsettest

import (
	"slices"
	"strings"
	"testing"
)

// CheckID names one check of the suite: its subtest path with dots, such as
// "settings.unknown_field" or "errors.429_retry_after". [Connector.Skip] uses
// it to skip a check, with a reason.
type CheckID string

// stage is how far a check has rolled in. A new check starts opt-in, becomes
// opt-out (it runs unless the connector skips it, with a reason), then
// mandatory (no connector may skip it). docs/CONNECTORS.md lists the stages.
type stage int

const (
	optOut stage = iota
	mandatory
)

// checks lists every check of [RunConnector] and its stage. Keep it in step
// with RunConnector's subtests and with docs/CONNECTORS.md (a test checks
// both).
var checks = []checkInfo{
	{"definitions", mandatory},
	{"settings.unknown_field", optOut},
	{"settings.literal_api_key", mandatory},
	{"settings.missing_key", optOut},
	{"settings.plain_http_base_url", mandatory},
	{"settings.base_url", optOut},
	{"calls", mandatory},
	{"keyless", mandatory},
	{"invalid", mandatory},
	{"errors.400", optOut},
	{"errors.422", optOut},
	{"errors.401", optOut},
	{"errors.403", optOut},
	{"errors.500", optOut},
	{"errors.502", optOut},
	{"errors.503", optOut},
	{"errors.429_retry_after", optOut},
	{"errors.429_then_200", optOut},
	{"errors.malformed_json", optOut},
	{"errors.oversized_body", optOut},
	{"errors.hang", optOut},
	{"errors.connection_refused", optOut},
	{"schema_honesty", optOut},
	{"canary", mandatory},
	{"hermetic", mandatory},
	{"mcp", optOut},
	{"concurrency", optOut},
}

type checkInfo struct {
	id    CheckID
	stage stage
}

func checkStage(id CheckID) (stage, bool) {
	i := slices.IndexFunc(checks, func(c checkInfo) bool { return c.id == id })
	if i < 0 {
		return 0, false
	}
	return checks[i].stage, true
}

// checkSkips fails the test for a Skip entry that names no check, a check
// that cannot be skipped, or gives no reason.
func checkSkips(tb testing.TB, skip map[CheckID]string) {
	tb.Helper()
	for id, reason := range skip {
		st, ok := checkStage(id)
		switch {
		case !ok:
			tb.Fatalf("toolsettest: Connector.Skip names %q, which is not a check (docs/CONNECTORS.md lists them)", id)
		case st == mandatory:
			tb.Fatalf("toolsettest: Connector.Skip names %q, which no connector may skip", id)
		case strings.TrimSpace(reason) == "":
			tb.Fatalf("toolsettest: Connector.Skip[%q] gives no reason", id)
		}
	}
}

// run runs check id as a subtest of t, named by the id's last element, or
// reports it skipped with the connector's reason.
func (s *suite) run(t *testing.T, id CheckID, fn func(t *testing.T)) {
	t.Helper()
	if _, ok := checkStage(id); !ok {
		t.Fatalf("toolsettest: check %q is missing from the checks table", id) // a bug in the kit
	}
	name := string(id)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	t.Run(name, func(t *testing.T) {
		if reason, ok := s.c.Skip[id]; ok {
			t.Skipf("toolsettest: check %s skipped by Connector.Skip: %s", id, reason)
		}
		fn(t)
	})
}
