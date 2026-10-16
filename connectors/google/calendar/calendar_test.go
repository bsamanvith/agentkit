package calendar_test

import (
	"context"
	"errors"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/google/calendar"
	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

type seen struct {
	Method, Path, EscapedPath, Auth string
	Query                           url.Values
	Body                            map[string]any
}

// fake answers every request with the same JSON and records what it got.
type fake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newFake(t *testing.T, status int, body string) *fake {
	t.Helper()
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := seen{Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization"), Query: r.URL.Query()}
		if len(b) > 0 {
			_ = json.Unmarshal(b, &s.Body)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, s)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) last(t *testing.T) seen {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no request reached the upstream")
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

var now = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func build(t *testing.T, f *fake, settings string, opts ...calendar.Option) tool.Registry {
	t.Helper()
	opts = append([]calendar.Option{
		calendar.WithBaseURL(f.srv.URL), calendar.WithHTTPClient(f.srv.Client()), calendar.WithClock(clocktest.NewFake(now)),
		calendar.WithLookupEnv(func(k string) (string, bool) { return "tok-123", k == "GOOGLE_ACCESS_TOKEN" }),
	}, opts...)
	if settings == "" {
		settings = "{}"
	}
	tools, err := calendar.Factory(opts...).Build(t.Context(), json.RawMessage(settings))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

const rw = `{"read_only": false}`

func invoke(t *testing.T, reg tool.Registry, name, in string) map[string]any {
	t.Helper()
	out, err := reg.Invoke(t.Context(), name, json.RawMessage(in))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, in, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func definitions(t *testing.T, reg tool.Registry) []tool.Definition {
	t.Helper()
	defs, err := reg.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func names(t *testing.T, reg tool.Registry) []string {
	var ns []string
	for _, d := range definitions(t, reg) {
		ns = append(ns, d.Name)
	}
	return ns
}

func TestWriteToolsExistOnlyWhenReadOnlyIsOff(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{}`)
	read := names(t, build(t, f, ""))
	if len(read) != 4 {
		t.Errorf("default tools = %v, want the four read tools", read)
	}
	for _, n := range read {
		if strings.Contains(n, "create") || strings.Contains(n, "update") || strings.Contains(n, "delete") {
			t.Errorf("default settings give the write tool %s", n)
		}
	}
	reg := build(t, f, rw)
	if got := len(names(t, reg)); got != 7 {
		t.Errorf("read_only false gives %d tools, want 7", got)
	}
	for _, d := range definitions(t, reg) {
		kind, risk := d.Class()
		switch d.Name {
		case "calendar_delete_event":
			if kind != tool.KindDelete || risk != tool.RiskHigh || !d.RequiresApproval {
				t.Errorf("delete is %s/%s approval=%v, want delete/high with approval", kind, risk, d.RequiresApproval)
			}
		case "calendar_create_event", "calendar_update_event":
			if kind != tool.KindWrite || risk != tool.RiskMedium {
				t.Errorf("%s is %s/%s, want write/medium", d.Name, kind, risk)
			}
		default:
			if kind != tool.KindRead || risk != tool.RiskLow {
				t.Errorf("%s is %s/%s, want read/low", d.Name, kind, risk)
			}
		}
	}
}

func TestGuestsAreNotEmailedUnlessAsked(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"id":"e1","start":{"date":"2026-10-20"},"end":{"date":"2026-10-21"}}`)
	reg := build(t, f, rw)
	calls := map[string]string{
		"calendar_create_event": `{"summary":"s","start":"2026-10-20","attendees":["a@example.com"]`,
		"calendar_update_event": `{"event_id":"e1","summary":"t"`,
		"calendar_delete_event": `{"event_id":"e1"`,
	}
	for name, in := range calls {
		for _, tc := range []struct{ extra, want string }{
			{``, "none"},
			{`,"send_updates":"all"`, "all"},
			{`,"send_updates":"externalOnly"`, "externalOnly"},
		} {
			invoke(t, reg, name, in+tc.extra+`}`)
			if got := f.last(t).Query.Get("sendUpdates"); got != tc.want {
				t.Errorf("%s%s sent sendUpdates=%q, want %q", name, tc.extra, got, tc.want)
			}
		}
	}
}

func TestListEventsWithoutARangeLooksAWeekAhead(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"items":[]}`)
	invoke(t, build(t, f, ""), "calendar_list_events", `{"query":"standup"}`)
	q := f.last(t).Query
	want := map[string]string{
		"timeMin": "2026-10-16T09:00:00Z", "timeMax": "2026-10-23T09:00:00Z",
		"singleEvents": "true", "orderBy": "startTime", "maxResults": "20", "q": "standup",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestAllDayEvents(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"items":[{"id":"a","summary":"Holiday","start":{"date":"2026-10-20"},"end":{"date":"2026-10-21"}}]}`)
	reg := build(t, f, rw)
	out := invoke(t, reg, "calendar_list_events", `{}`)
	ev := out["events"].([]any)[0].(map[string]any)
	if ev["all_day"] != true || ev["start"] != "2026-10-20" || ev["end"] != "2026-10-21" {
		t.Errorf("all-day event = %v", ev)
	}
	invoke(t, reg, "calendar_create_event", `{"summary":"Holiday","start":"2026-10-20"}`)
	body := f.last(t).Body
	if start, end := body["start"].(map[string]any), body["end"].(map[string]any); start["date"] != "2026-10-20" || end["date"] != "2026-10-21" || start["dateTime"] != nil {
		t.Errorf("all-day body = %v, want dates and the next day as end", body)
	}
}

func TestUpdateSendsOnlyTheGivenFields(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"id":"e1","start":{"date":"2026-10-20"},"end":{"date":"2026-10-21"}}`)
	reg := build(t, f, rw)
	invoke(t, reg, "calendar_update_event", `{"event_id":"e1","location":""}`)
	if s := f.last(t); s.Method != "PATCH" || len(s.Body) != 1 || s.Body["location"] != "" {
		t.Errorf("clearing the location sent %s %v, want a PATCH of just location=\"\"", s.Method, s.Body)
	}
	invoke(t, reg, "calendar_update_event", `{"event_id":"e1","attendees":[]}`)
	if g, ok := f.last(t).Body["attendees"].([]any); !ok || len(g) != 0 {
		t.Errorf("an empty attendees list sent %v, want [] to clear the guests", f.last(t).Body)
	}
	invoke(t, reg, "calendar_update_event", `{"event_id":"e1","summary":"x"}`)
	if _, has := f.last(t).Body["attendees"]; has {
		t.Errorf("an update without attendees sent them: %v", f.last(t).Body)
	}
}

func TestAMeetLinkIsRequestedOncePerCall(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"id":"e1","start":{"date":"2026-10-20"},"end":{"date":"2026-10-21"}}`)
	reg := build(t, f, rw)
	id := func(in string) string {
		invoke(t, reg, "calendar_create_event", in)
		s := f.last(t)
		if s.Query.Get("conferenceDataVersion") != "1" {
			t.Errorf("no conferenceDataVersion=1: %v", s.Query)
		}
		return s.Body["conferenceData"].(map[string]any)["createRequest"].(map[string]any)["requestId"].(string)
	}
	a := id(`{"summary":"s","start":"2026-10-20","add_meet_link":true}`)
	if again := id(`{"summary":"s","start":"2026-10-20","add_meet_link":true}`); again != a {
		t.Errorf("the same call sent requestId %q then %q: a retry could make two meetings", a, again)
	}
	if other := id(`{"summary":"other","start":"2026-10-20","add_meet_link":true}`); other == a {
		t.Error("two different events share a requestId")
	}
}

func TestIDsStayOnePathSegment(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"id":"x","start":{"date":"2026-10-20"},"end":{"date":"2026-10-21"}}`)
	invoke(t, build(t, f, ""), "calendar_get_event", `{"calendar_id":"a/b@example.com","event_id":"e/../x?y=1"}`)
	if got, want := f.last(t).EscapedPath, "/calendars/a%2Fb@example.com/events/e%2F..%2Fx%3Fy=1"; got != want {
		t.Errorf("path = %s, want %s", got, want)
	}
}

func TestFreeBusyKeepsTheOrderAskedAndReportsAMissingCalendar(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"calendars":{"b@example.com":{"busy":[]}}}`)
	out := invoke(t, build(t, f, ""), "calendar_free_busy", `{"calendar_ids":["a@example.com","b@example.com","a@example.com"]}`)
	cals := out["calendars"].([]any)
	if len(cals) != 2 || cals[0].(map[string]any)["id"] != "a@example.com" || cals[1].(map[string]any)["id"] != "b@example.com" {
		t.Fatalf("calendars = %v, want a then b, once each", cals)
	}
	if errs := cals[0].(map[string]any)["errors"]; errs == nil {
		t.Errorf("a calendar the API left out has no error: %v", cals[0])
	}
}

func TestTheTokenComesFromTheTokenSource(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{"items":[]}`)
	src := common.TokenSourceFunc(func(context.Context) (string, error) { return "owner-token", nil })
	reg := build(t, f, "", calendar.WithTokenSource(src))
	invoke(t, reg, "calendar_list_calendars", `{}`)
	if got := f.last(t).Auth; got != "Bearer owner-token" {
		t.Errorf("Authorization = %q, want the source's token", got)
	}
}

func TestInvalidInputMakesNoRequest(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{}`)
	reg := build(t, f, rw)
	for _, c := range []struct{ name, in string }{
		{"calendar_list_events", `{"time_min":"tomorrow"}`},
		{"calendar_list_events", `{"time_min":"2026-10-20T00:00:00Z","time_max":"2026-10-19T00:00:00Z"}`},
		{"calendar_create_event", `{"summary":"s","start":"2026-10-20","end":"2026-10-20"}`},
		{"calendar_create_event", `{"summary":"s","start":"2026-10-20T10:00:00Z"}`},
		{"calendar_create_event", `{"summary":" ","start":"2026-10-20"}`},
		{"calendar_update_event", `{"event_id":"e","start":"2026-10-20T10:00:00Z","end":"2026-10-20"}`},
	} {
		_, err := reg.Invoke(t.Context(), c.name, json.RawMessage(c.in))
		if !isInvalid(err) {
			t.Errorf("%s(%s) = %v, want ErrInvalidInput", c.name, c.in, err)
		}
	}
	if n := f.count(); n != 0 {
		t.Errorf("%d requests were made for invalid input", n)
	}
}

func isInvalid(err error) bool {
	return errors.Is(err, tool.ErrInvalidInput) && !errors.Is(err, tool.ErrToolFailed)
}

func TestSettings(t *testing.T) {
	t.Parallel()
	f := newFake(t, 200, `{}`)
	build(t, f, `{"time_zone":"America/Argentina/Buenos_Aires","default_calendar_id":"team@example.com","max_description_chars":10}`)
	lookup := calendar.WithLookupEnv(func(string) (string, bool) { return "tok", true })
	for name, settings := range map[string]string{
		"zone":        `{"time_zone":"not a zone!"}`,
		"description": `{"max_description_chars":-1}`,
		"token":       `{"access_token":"ya29.secret"}`,
		"unknown":     `{"read_write":true}`,
		"calendar":    `{"default_calendar_id":" "}`,
	} {
		_, err := calendar.Factory(lookup).Build(t.Context(), json.RawMessage(settings))
		if !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("%s: Build(%s) = %v, want ErrInvalidSettings", name, settings, err)
		}
		if err != nil && strings.Contains(err.Error(), "ya29.secret") {
			t.Errorf("%s: the error echoes the token: %v", name, err)
		}
	}
	if _, err := calendar.Factory().Build(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Error("Build with no token and no variable succeeded")
	}
}

func TestScopes(t *testing.T) {
	t.Parallel()
	if got := calendar.Scopes(true); len(got) != 1 || got[0] != calendar.ScopeReadOnly {
		t.Errorf("Scopes(true) = %v", got)
	}
	if got := calendar.Scopes(false); len(got) != 2 || got[1] != calendar.ScopeEvents {
		t.Errorf("Scopes(false) = %v", got)
	}
}
