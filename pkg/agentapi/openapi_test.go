package agentapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
	"github.com/bsamanvith/agentkit/pkg/trigger"
)

const specFile = "../../docs/api/openapi.yaml"

// spec is the part of the OpenAPI document the tests read.
type spec struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Security []map[string][]string `json:"security"`
	Paths    map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Parameters  []struct {
			Name     string `json:"name"`
			In       string `json:"in"`
			Required bool   `json:"required"`
		} `json:"parameters"`
		Responses map[string]struct {
			Headers map[string]any `json:"headers"`
			Content map[string]any `json:"content"`
		} `json:"responses"`
	} `json:"paths"`
	Components struct {
		SecuritySchemes map[string]any `json:"securitySchemes"`
	} `json:"components"`
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func loadSpec(t *testing.T) (spec, []byte) {
	t.Helper()
	raw, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	var s spec
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("%s: %v", specFile, err)
	}
	return s, raw
}

// operations returns the spec's operations as "METHOD /path" and their
// documented statuses.
func (s spec) operations() map[string][]string {
	ops := map[string][]string{}
	for path, item := range s.Paths {
		for method, op := range item {
			ops[strings.ToUpper(method)+" "+path] = slices.Sorted(maps.Keys(op.Responses))
		}
	}
	return ops
}

// routeProblems compares the server's routes ("METHOD /path", as
// Server.Routes returns them) with the spec's operations, both ways.
func routeProblems(routes []string, s spec) []string {
	ops := s.operations()
	var problems []string
	for _, r := range routes {
		if _, ok := ops[r]; !ok {
			problems = append(problems, r+" is served but not in "+specFile)
		}
	}
	for op := range ops {
		if !slices.Contains(routes, op) {
			problems = append(problems, op+" is in "+specFile+" but not served")
		}
	}
	slices.Sort(problems)
	return problems
}

// TestOpenAPISpec: docs/api/openapi.yaml is an OpenAPI 3.1 document whose
// schemas resolve, and its operations are exactly the server's routes; a
// planted undocumented route (or a dropped documented one) fails the check.
func TestOpenAPISpec(t *testing.T) {
	t.Parallel()
	s, _ := loadSpec(t)
	if s.OpenAPI != "3.1.0" || s.Info.Title == "" || s.Info.Version == "" || len(s.Paths) == 0 {
		t.Errorf("openapi %q, info %+v, %d paths: want 3.1.0 with a title, a version and paths", s.OpenAPI, s.Info, len(s.Paths))
	}
	if _, err := toolsettest.LoadOpenAPI(specFile); err != nil {
		t.Errorf("schemas: %v", err)
	}
	for _, req := range s.Security {
		for name := range req {
			if s.Components.SecuritySchemes[name] == nil {
				t.Errorf("security %q is not a security scheme", name)
			}
		}
	}
	param := regexp.MustCompile(`\{([^}]+)\}`)
	ids := map[string]string{}
	for path, item := range s.Paths {
		if !strings.HasPrefix(path, "/v1/") {
			t.Errorf("path %s is not under /v1/", path)
		}
		var inPath []string
		for _, m := range param.FindAllStringSubmatch(path, -1) {
			inPath = append(inPath, m[1])
		}
		for method, op := range item {
			at := strings.ToUpper(method) + " " + path
			if !slices.Contains(methods, method) {
				t.Errorf("%s: %q is not an HTTP method", at, method)
			}
			if prev, dup := ids[op.OperationID]; op.OperationID == "" || dup {
				t.Errorf("%s: operationId %q (also %s)", at, op.OperationID, prev)
			}
			ids[op.OperationID] = at
			var declared []string
			for _, p := range op.Parameters {
				if p.In == "path" {
					declared = append(declared, p.Name)
					if !p.Required {
						t.Errorf("%s: path parameter %s is not required", at, p.Name)
					}
				}
			}
			if !slices.Equal(slices.Sorted(slices.Values(declared)), slices.Sorted(slices.Values(inPath))) {
				t.Errorf("%s: path parameters %v, but the path has %v", at, declared, inPath)
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s has no responses", at)
			}
			for status := range op.Responses {
				if n, err := strconv.Atoi(status); err != nil || n < 100 || n > 599 {
					t.Errorf("%s: response %q is not a status code", at, status)
				}
			}
		}
	}

	srv, err := agentapi.New(agentHarness(t, echo(t), registry(t)), agentapi.WithStore(scoped.Memory(nil)))
	if err != nil {
		t.Fatal(err)
	}
	routes := srv.Routes()
	if p := routeProblems(routes, s); len(p) > 0 {
		t.Errorf("the server's routes and the spec differ:\n%s", strings.Join(p, "\n"))
	}
	planted := append(slices.Clone(routes), "GET /v1/planted")
	if p := routeProblems(planted, s); !slices.Equal(p, []string{"GET /v1/planted is served but not in " + specFile}) {
		t.Errorf("a planted undocumented route: %q", p)
	}
	dropped := slices.DeleteFunc(slices.Clone(routes), func(r string) bool { return r == "DELETE /v1/conversations/{id}" })
	if p := routeProblems(dropped, s); !slices.Equal(p, []string{"DELETE /v1/conversations/{id} is in " + specFile + " but not served"}) {
		t.Errorf("a documented route the server lacks: %q", p)
	}
}

// failing is a toolset whose tools cannot be listed.
type failing struct{}

func (failing) List(context.Context) ([]tool.Definition, error) {
	return nil, errors.New("the toolset is down")
}

func (failing) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("the toolset is down")
}

// recorder keeps the exchanges a probe makes, as toolsettest cassettes.
type recorder struct {
	t      *testing.T
	dir    string
	probed map[string]bool // "METHOD /template STATUS"
	ops    map[string][]string
	spec   spec
}

// template returns the spec path a request path matches.
func (rec *recorder) template(method, path string) string {
	segs := strings.Split(path, "/")
	for op := range rec.ops {
		m, tmpl, _ := strings.Cut(op, " ")
		tsegs := strings.Split(tmpl, "/")
		if m != method || len(tsegs) != len(segs) {
			continue
		}
		match := true
		for i, ts := range tsegs {
			if !strings.HasPrefix(ts, "{") && ts != segs[i] {
				match = false
			}
		}
		if match {
			return tmpl
		}
	}
	rec.t.Fatalf("%s %s is not in the spec", method, path)
	return ""
}

// doer sends requests: a rig, or a signinRig.
type doer interface {
	do(method, path, body string, header ...string) (*http.Response, []byte)
}

// probe sends a request on r, checks it gets status, and records it. A body
// that is not a valid request (a probe of a 400) is recorded as reqBody.
func (rec *recorder) probe(r doer, name string, status int, method, target, body, reqBody string, header ...string) []byte {
	rec.t.Helper()
	resp, got := r.do(method, target, body, header...)
	if resp.StatusCode != status {
		rec.t.Fatalf("%s: %s %s = %d %s, want %d", name, method, target, resp.StatusCode, got, status)
	}
	u, err := url.Parse(target)
	if err != nil {
		rec.t.Fatal(err)
	}
	tmpl := rec.template(method, u.Path)
	rec.probed[fmt.Sprintf("%s %s %d", method, tmpl, status)] = true
	documented := rec.spec.Paths[tmpl][strings.ToLower(method)].Responses[strconv.Itoa(status)]
	for h := range documented.Headers {
		if resp.Header.Get(h) == "" {
			rec.t.Errorf("%s: no %s header, which the spec documents", name, h)
		}
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch {
	case documented.Content == nil && len(got) != 0:
		rec.t.Errorf("%s: a body %q where the spec has none", name, got)
	case documented.Content != nil && documented.Content[ct] == nil:
		rec.t.Errorf("%s: Content-Type %q, not one the spec documents", name, ct)
	}
	if reqBody == "" {
		reqBody = body
	}
	rec.write(name, toolsettest.CassetteRequest{Method: method, Path: u.Path, Query: u.Query(), Body: json.RawMessage(reqBody)}, resp, got)
	return got
}

func (rec *recorder) write(name string, req toolsettest.CassetteRequest, resp *http.Response, body []byte) {
	rec.t.Helper()
	if len(req.Query) == 0 {
		req.Query = nil
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		rec.t.Fatal(err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength, resp.TransferEncoding = int64(len(body)), nil
	dump, err := httputil.DumpResponse(resp, true)
	if err != nil {
		rec.t.Fatal(err)
	}
	for file, b := range map[string][]byte{name + ".req.json": reqJSON, name + ".res.http": dump} {
		if err := os.WriteFile(filepath.Join(rec.dir, file), b, 0o600); err != nil {
			rec.t.Fatal(err)
		}
	}
}

// stream sends a request answered by an event stream, checks it is one, and
// records each event, to check against the spec's Event.
func (rec *recorder) stream(r *rig, name, method, target string) {
	rec.t.Helper()
	resp, got := r.do(method, target, "")
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || ct != "text/event-stream" {
		rec.t.Fatalf("%s: %s %s = %d %s", name, method, target, resp.StatusCode, ct)
	}
	u, err := url.Parse(target)
	if err != nil {
		rec.t.Fatal(err)
	}
	tmpl := rec.template(method, u.Path)
	if rec.spec.Paths[tmpl][strings.ToLower(method)].Responses["200"].Content[ct] == nil {
		rec.t.Errorf("%s: Content-Type %q, not one the spec documents", name, ct)
	}
	rec.probed[fmt.Sprintf("%s %s 200", method, tmpl)] = true
	n := 0
	for line := range strings.SplitSeq(string(got), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			n++
			ev := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}}
			rec.write(fmt.Sprintf("%s_event_%02d", name, n), toolsettest.CassetteRequest{Method: "POST", Path: "/x-test/event"}, ev, []byte(data))
		}
	}
	if n < 2 {
		rec.t.Errorf("%s: %d events", name, n)
	}
}

// strictSpec writes a copy of the spec in which no object takes properties
// it does not list (the published spec leaves them open: the API grows), and
// with an operation POST /x-test/event answering an Event, to check stream
// events against.
func strictSpec(t *testing.T, raw []byte) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var seal func(v any)
	seal = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if _, ok := v["properties"].(map[string]any); ok {
				if _, set := v["additionalProperties"]; !set {
					v["additionalProperties"] = false
				}
			}
			for _, x := range v {
				seal(x)
			}
		case []any:
			for _, x := range v {
				seal(x)
			}
		}
	}
	seal(doc["components"])
	doc["paths"].(map[string]any)["/x-test/event"] = map[string]any{"post": map[string]any{"responses": map[string]any{
		"200": map[string]any{"description": "an event", "content": map[string]any{"application/json": map[string]any{
			"schema": map[string]any{"$ref": "#/components/schemas/Event"},
		}}},
	}}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOpenAPIResponses: every documented status of every operation is
// produced by the server (a probe each), with the documented content type
// and headers, and every response body, request body and stream event fits
// the spec's schemas, with no field the spec does not list.
func TestOpenAPIResponses(t *testing.T) {
	t.Parallel()
	s, raw := loadSpec(t)
	rec := &recorder{t: t, dir: t.TempDir(), probed: map[string]bool{}, ops: s.operations(), spec: s}
	const token = "a-token-for-the-openapi-probes-0123456789"
	bearer := []string{"Authorization", "Bearer " + strings.Repeat("x", len(token))}
	jsonAccept := []string{"Accept", "application/json"}

	sets := demoTools(t)
	m := echo(t)
	m.gate = make(chan struct{}, 8)
	for range 2 { // the JSON run and the stream; the third run waits
		m.gate <- struct{}{}
	}
	toolsets := agentapi.WithToolsets(
		agentapi.Toolset{Name: "a", Type: "demo", Tools: sets["a"], MCPPath: "/mcp/a", Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}},
		agentapi.Toolset{
			Name: "web", Type: "exa", Skipped: "toolset web (exa): environment variable EXA_API_KEY is unset or empty",
			Credential: agentapi.Credential{Status: wire.CredentialMissing, Env: "EXA_API_KEY"},
		},
	)
	// The harnesses run on the rig's clock, as the rig's own does: a run's
	// replayed run_start has its prompt's time.
	clk := clocktest.NewFake(time.Time{})
	now := harness.WithClock(clk)
	agents := []agentapi.Agent{
		{
			Name: agentapi.DefaultAgent, Description: "The default.", Model: wire.Model{Provider: "anthropic", ID: "claude", Effort: "low"}, System: "Be brief.",
			Toolsets: []string{"a", "web"}, Harness: agentHarness(t, m, sets["a"], now),
		},
		{Name: "other", Harness: agentHarness(t, echo(t), registry(t), now)},
		// An interactive agent whose run waits for input.
		{Name: "asker", Harness: agentHarness(t, script(t,
			llmtest.ToolCalls(llm.ToolUse{ID: "q1", Name: "ask", Input: json.RawMessage(`{}`)}), llmtest.Text("red it is"),
		), interruptTools(t, new(atomic.Int64)), append(interactive(), now)...)},
		// An agent with UI: its run streams ui events, and an action
		// continues it.
		{Name: "painter", UI: a2ui.UIMode, Harness: agentHarness(t, script(t,
			llmtest.ToolCalls(renderCall("u1", false)), llmtest.Text("pick one"), llmtest.Text("searching"),
		), registry(t, a2ui.Tool()), now)},
	}
	// A flow with an approval, for the decisions; and a store the probes
	// share with a second server, which holds no run of its own: it creates
	// nothing, so its IDs (from 001, as this server's) never meet.
	flows := runner.WithFlows(func(context.Context, string) (flow.Step, error) {
		return flow.Sequence("f", flow.Approve("ok")), nil
	})
	st := scoped.Memory(clk)
	r := newRig(t, m, rigConfig{agents: agents, token: token, clock: clk, store: st, api: []agentapi.Option{
		toolsets, agentapi.WithMaxConcurrentRuns(1), agentapi.WithMaxBodyBytes(512), agentapi.WithRunnerOptions(flows),
		agentapi.WithMaxBackgroundRuns(1), agentapi.WithWebhooks(agentapi.Webhooks{
			Secret: func(tenancy.Tenant) (string, error) { return "whsec_c2VjcmV0", nil },
		}),
	}})
	other := newRig(t, echo(t), rigConfig{token: token, clock: clk, store: st})
	// The probes post raw requests the rig does not track, and delete a
	// conversation: there is no transcript to match the model's requests to.
	r.models = nil

	var run agentapi.Run
	if err := json.Unmarshal(rec.probe(r, "run_json", 200, "POST", "/v1/runs", `{"message":"hello"}`, "", jsonAccept...), &run); err != nil {
		t.Fatal(err)
	}
	conv := run.ConversationID

	// The stream: its content type is documented, and each event fits Event.
	resp := r.post(t.Context(), `{"message":"stream me"}`, "")
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if s.Paths["/v1/runs"]["post"].Responses["200"].Content[ct] == nil || ct != "text/event-stream" {
		t.Errorf("a stream's Content-Type is %q", ct)
	}
	rec.probed["POST /v1/runs 200"] = true
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	events := 0
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			events++
			ev := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}}
			rec.write(fmt.Sprintf("event_%02d", events), toolsettest.CassetteRequest{Method: "POST", Path: "/x-test/event"}, ev, []byte(data))
		}
	}
	resp.Body.Close()
	if events < 3 {
		t.Errorf("the stream had %d events", events)
	}

	rec.probe(r, "run_bad_request", 400, "POST", "/v1/runs", `{"message":""}`, "", jsonAccept...)
	rec.probe(r, "run_unauthorized", 401, "POST", "/v1/runs", `{"message":"hi"}`, "", bearer...)
	rec.probe(r, "run_no_conversation", 404, "POST", "/v1/runs", `{"message":"hi","conversation_id":"nope"}`, "", jsonAccept...)
	rec.probe(r, "run_agent_mismatch", 409, "POST", "/v1/runs", `{"message":"hi","conversation_id":"`+conv+`","agent":"other"}`, "", jsonAccept...)
	rec.probe(r, "run_too_large", 413, "POST", "/v1/runs", `{"message":"`+strings.Repeat("x", 600)+`"}`, `{"message":"too long"}`, jsonAccept...)

	rec.probe(r, "get_run", 200, "GET", "/v1/runs/"+run.ID, "", "")
	rec.probe(r, "get_run_unauthorized", 401, "GET", "/v1/runs/"+run.ID, "", "", bearer...)
	rec.probe(r, "get_run_not_found", 404, "GET", "/v1/runs/nope", "", "")

	rec.probe(r, "list_runs", 200, "GET", "/v1/runs?conversation_id="+conv+"&limit=1", "", "")
	rec.probe(r, "list_runs_bad_request", 400, "GET", "/v1/runs?background=maybe", "", "")
	rec.probe(r, "list_runs_unauthorized", 401, "GET", "/v1/runs?conversation_id="+conv, "", "", bearer...)
	rec.probe(r, "list_runs_not_found", 404, "GET", "/v1/runs?conversation_id=nope", "", "")
	rec.stream(r, "run_events", "GET", "/v1/runs/"+run.ID+"/events")
	rec.probe(r, "run_events_bad_request", 400, "GET", "/v1/runs/"+run.ID+"/events", "", "", "Last-Event-ID", "x")
	rec.probe(r, "run_events_unauthorized", 401, "GET", "/v1/runs/"+run.ID+"/events", "", "", bearer...)
	rec.probe(r, "run_events_not_found", 404, "GET", "/v1/runs/nope/events", "", "")

	// Runs stopped while they wait for the model: interrupted. The first is
	// resumed; the second waits for the probes of a full server.
	stopped := func(message string) string {
		s := r.start(t.Context(), message, "")
		id := s.until(agentapi.TypeRunStart, 0).RunID
		rec.probe(r, "stop_run", 200, "POST", "/v1/runs/"+id+"/stop", "", "")
		s.all()
		return id
	}
	resumed, interrupted := stopped("stop me"), stopped("stop me too")
	rec.probe(r, "stop_run_unauthorized", 401, "POST", "/v1/runs/"+resumed+"/stop", "", "", bearer...)
	rec.probe(r, "stop_run_not_found", 404, "POST", "/v1/runs/nope/stop", "", "")
	m.gate <- struct{}{}
	rec.probe(r, "resume_run", 200, "POST", "/v1/runs/"+resumed+"/resume", "", "", jsonAccept...)
	rec.probe(r, "resume_run_final", 409, "POST", "/v1/runs/"+resumed+"/resume", "", "")
	rec.probe(r, "resume_run_unauthorized", 401, "POST", "/v1/runs/"+resumed+"/resume", "", "", bearer...)
	rec.probe(r, "resume_run_not_found", 404, "POST", "/v1/runs/nope/resume", "", "")
	rec.probe(r, "cancel_run_final", 409, "POST", "/v1/runs/"+resumed+"/cancel", "", "")
	rec.probe(r, "cancel_run_unauthorized", 401, "POST", "/v1/runs/"+resumed+"/cancel", "", "", bearer...)
	rec.probe(r, "cancel_run_not_found", 404, "POST", "/v1/runs/nope/cancel", "", "")

	// A flow run waiting at its approval.
	tenant, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	x, err := r.srv.Runner().RunFlow(tenancy.With(t.Context(), tenant), r.create(`{}`).ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Wait(); err != nil {
		t.Fatal(err)
	}
	suspended := x.Run.ID
	decisions := "/v1/runs/" + suspended + "/decisions"
	rec.probe(r, "decide_bad_request", 400, "POST", decisions, `{"path":"","approved":true}`, "")
	rec.probe(r, "decide_unauthorized", 401, "POST", decisions, `{"path":"f/ok","approved":true}`, "", bearer...)
	rec.probe(r, "decide_not_found", 404, "POST", "/v1/runs/nope/decisions", `{"path":"f/ok","approved":true}`, "")
	rec.probe(r, "decide_not_pending", 409, "POST", decisions, `{"path":"f/nope","approved":true}`, "")
	rec.probe(r, "decide_too_large", 413, "POST", decisions, `{"path":"`+strings.Repeat("x", 600)+`"}`, `{"path":"too long"}`)

	// A chat run waiting for input.
	var asked agentapi.Run
	resp, body := r.do("POST", "/v1/runs", `{"message":"pick","agent":"asker"}`, "Accept", "application/json")
	if err := json.Unmarshal(body, &asked); err != nil || resp.StatusCode != 200 || asked.Status != agentapi.StatusSuspended || len(asked.Interrupts) != 1 {
		t.Fatalf("the asker's run = %d %s", resp.StatusCode, body)
	}
	input := "/v1/runs/" + asked.ID + "/input"
	answer := `{"answers":[{"id":"q1","text":"red"}]}`
	rec.probe(r, "input_bad_request", 400, "POST", input, `{"answers":[{"id":"q1","text":"green"}]}`, "")
	rec.probe(r, "input_unauthorized", 401, "POST", input, answer, "", bearer...)
	rec.probe(r, "input_not_found", 404, "POST", "/v1/runs/nope/input", answer, "")
	rec.probe(r, "input_not_pending", 409, "POST", "/v1/runs/"+suspended+"/input", answer, "")
	rec.probe(r, "input_too_large", 413, "POST", input, `{"answers":[{"id":"`+strings.Repeat("x", 600)+`"}]}`, `{"answers":[{"id":"too long"}]}`)

	rec.probe(r, "list_conversations", 200, "GET", "/v1/conversations?limit=1", "", "")
	rec.probe(r, "list_conversations_bad_limit", 400, "GET", "/v1/conversations?limit=0", "", "")
	rec.probe(r, "list_conversations_unauthorized", 401, "GET", "/v1/conversations", "", "", bearer...)
	rec.probe(r, "list_conversations_no_agent", 404, "GET", "/v1/conversations?agent=nobody", "", "")

	var created agentapi.Conversation
	if err := json.Unmarshal(rec.probe(r, "create_conversation", 201, "POST", "/v1/conversations", `{"agent":"other"}`, ""), &created); err != nil {
		t.Fatal(err)
	}
	rec.probe(r, "run_in_created", 200, "POST", "/v1/runs", `{"message":"hi","conversation_id":"`+created.ID+`"}`, "", jsonAccept...)
	var painted agentapi.Run
	if err := json.Unmarshal(rec.probe(r, "run_ui", 200, "POST", "/v1/runs", `{"message":"paint","agent":"painter"}`, "", jsonAccept...), &painted); err != nil {
		t.Fatal(err)
	}
	rec.stream(r, "ui_events", "GET", "/v1/runs/"+painted.ID+"/events")
	action := `{"conversation_id":"` + painted.ConversationID + `","ui_action":{"surface_id":"news","component_id":"go","action":"search","context":{"topic":["go"]}}}`
	rec.probe(r, "run_ui_action", 200, "POST", "/v1/runs", action, "", jsonAccept...)
	rec.probe(r, "run_ui_action_bad_request", 400, "POST", "/v1/runs", `{"agent":"painter","ui_action":{"surface_id":"news","component_id":"go","action":"search"}}`, "", jsonAccept...)
	rec.probe(r, "get_agent_ui", 200, "GET", "/v1/agents/painter", "", "")
	rec.probe(r, "create_conversation_bad_request", 400, "POST", "/v1/conversations", `{"title":"x"}`, `{"agent":"an unknown field instead"}`)
	rec.probe(r, "create_conversation_unauthorized", 401, "POST", "/v1/conversations", `{}`, "", bearer...)
	rec.probe(r, "create_conversation_no_agent", 404, "POST", "/v1/conversations", `{"agent":"nobody"}`, "")
	rec.probe(r, "create_conversation_too_large", 413, "POST", "/v1/conversations", `{"agent":"`+strings.Repeat("x", 600)+`"}`, `{"agent":"too long"}`)

	rec.probe(r, "get_conversation", 200, "GET", "/v1/conversations/"+conv, "", "")
	rec.probe(r, "get_conversation_unauthorized", 401, "GET", "/v1/conversations/"+conv, "", "", bearer...)
	rec.probe(r, "get_conversation_not_found", 404, "GET", "/v1/conversations/nope", "", "")

	rec.probe(r, "list_agents", 200, "GET", "/v1/agents", "", "")
	rec.probe(r, "list_agents_unauthorized", 401, "GET", "/v1/agents", "", "", bearer...)
	rec.probe(r, "get_agent", 200, "GET", "/v1/agents/default", "", "")
	rec.probe(r, "get_agent_unauthorized", 401, "GET", "/v1/agents/default", "", "", bearer...)
	rec.probe(r, "get_agent_not_found", 404, "GET", "/v1/agents/nobody", "", "")

	rec.probe(r, "list_toolsets", 200, "GET", "/v1/toolsets", "", "")
	rec.probe(r, "list_toolsets_unauthorized", 401, "GET", "/v1/toolsets", "", "", bearer...)
	rec.probe(r, "list_tools", 200, "GET", "/v1/toolsets/a/tools", "", "")
	rec.probe(r, "list_tools_skipped", 200, "GET", "/v1/toolsets/web/tools", "", "")
	rec.probe(r, "list_tools_unauthorized", 401, "GET", "/v1/toolsets/a/tools", "", "", bearer...)
	rec.probe(r, "list_tools_not_found", 404, "GET", "/v1/toolsets/nope/tools", "", "")

	// A run going on (it waits at the model's gate): its conversation is busy,
	// and the server, at one run, is over capacity.
	s2 := r.start(t.Context(), "hold on", "")
	busy := s2.until(agentapi.TypeRunStart, 0).ConversationID
	rec.probe(r, "run_over_capacity", 503, "POST", "/v1/runs", `{"message":"one more"}`, "", jsonAccept...)
	rec.probe(r, "resume_over_capacity", 503, "POST", "/v1/runs/"+interrupted+"/resume", "", "")
	rec.probe(r, "decide_over_capacity", 503, "POST", decisions, `{"path":"f/ok","approved":true}`, "")
	rec.probe(r, "input_over_capacity", 503, "POST", input, answer, "")
	rec.probe(r, "delete_busy", 409, "DELETE", "/v1/conversations/"+busy, "", "")
	rec.probe(r, "task_over_capacity", 503, "POST", "/v1/tasks", `{"input":"one more"}`, "")
	// The other server shares the store: the run is held by this one.
	rec.probe(other, "stop_run_leased", 409, "POST", "/v1/runs/"+s2.runID()+"/stop", "", "")
	m.gate <- struct{}{}
	s2.all()
	rec.probe(r, "cancel_run", 200, "POST", "/v1/runs/"+interrupted+"/cancel", "", "")
	rec.probe(r, "decide", 200, "POST", decisions, `{"path":"f/ok","approved":true}`, "")
	r.waitEnded(suspended)
	rec.probe(r, "input", 200, "POST", input, answer, "")
	r.waitEnded(asked.ID)

	// A runner pool at its limit (T105): a chat, new or resumed, gets 429.
	pm := echo(t)
	pm.gate = make(chan struct{}, 1)
	chats, err := pool.NewRouter([]pool.Pool{pool.NewProcess("chat")}, nil, "chat", pool.WithLimit("chat", 1))
	if err != nil {
		t.Fatal(err)
	}
	rp := newRig(t, pm, rigConfig{token: token, api: []agentapi.Option{agentapi.WithRouter(chats)}})
	rp.models = nil
	waits := rp.start(t.Context(), "stop me", "")
	waiting := waits.until(agentapi.TypeRunStart, 0).RunID
	rec.probe(rp, "stop_run_pooled", 200, "POST", "/v1/runs/"+waiting+"/stop", "", "")
	waits.all()
	holds := rp.start(t.Context(), "hold on", "")
	holds.until(agentapi.TypeRunStart, 0)
	rec.probe(rp, "run_pool_full", 429, "POST", "/v1/runs", `{"message":"one more"}`, "", jsonAccept...)
	rec.probe(rp, "resume_pool_full", 429, "POST", "/v1/runs/"+waiting+"/resume", "", "")
	pm.gate <- struct{}{}
	holds.all()
	// An answer that lets a suspended chat go on takes a slot too.
	asks, err := pool.NewRouter([]pool.Pool{pool.NewProcess("chat")}, nil, "chat", pool.WithLimit("chat", 1))
	if err != nil {
		t.Fatal(err)
	}
	var noted atomic.Int64
	ra := newRig(t, script(t, llmtest.ToolCalls(llm.ToolUse{ID: "q1", Name: "ask", Input: json.RawMessage(`{}`)})),
		rigConfig{token: token, tools: interruptTools(t, &noted), harness: interactive(), api: []agentapi.Option{agentapi.WithRouter(asks)}})
	ra.models = nil
	pending := ra.run("pick", "").runID()
	ra.waitEnded(pending)
	if !asks.Slots("chat").TryAcquire() {
		t.Fatal("the asker's pool is full")
	}
	rec.probe(ra, "input_pool_full", 429, "POST", "/v1/runs/"+pending+"/input", `{"answers":[{"id":"q1","text":"red"}]}`, "")
	asks.Slots("chat").Release()
	// A task, waiting at the model's gate: the tenant's one background run.
	task := decodeAs[agentapi.Run](t, rec.probe(r, "task", 202, "POST", "/v1/tasks", `{"input":"in the background","notify":{"url":"https://hooks.example/done"}}`, ""))
	rec.probe(r, "task_too_many", 429, "POST", "/v1/tasks", `{"input":"one more"}`, "")
	rec.probe(r, "list_tasks", 200, "GET", "/v1/runs?background=true&status=running&agent=default", "", "")
	rec.probe(r, "get_task", 200, "GET", "/v1/runs/"+task.ID, "", "")
	rec.probe(r, "task_bad_request", 400, "POST", "/v1/tasks", `{"input":" "}`, "")
	rec.probe(r, "task_unauthorized", 401, "POST", "/v1/tasks", `{"input":"hi"}`, "", bearer...)
	rec.probe(r, "task_no_agent", 404, "POST", "/v1/tasks", `{"input":"hi","agent":"nobody"}`, "")
	rec.probe(r, "task_too_large", 413, "POST", "/v1/tasks", `{"input":"`+strings.Repeat("x", 600)+`"}`, `{"input":"too long"}`)
	m.gate <- struct{}{}
	r.waitEnded(task.ID)
	rec.probe(r, "webhook_secret", 200, "GET", "/v1/webhooks/secret", "", "")
	rec.probe(r, "webhook_secret_unauthorized", 401, "GET", "/v1/webhooks/secret", "", "", bearer...)
	rec.probe(other, "webhook_secret_none", 404, "GET", "/v1/webhooks/secret", "", "")
	// A thread of messages between agents (T073), in the store the probes
	// share, of the token's tenant.
	seedThread(t, st, agentapi.DefaultTenant, "thr_probe")
	seedThread(t, st, agentapi.DefaultTenant, "thr_probe2")
	rec.probe(r, "list_threads", 200, "GET", "/v1/threads?limit=1", "", "")
	rec.probe(r, "list_threads_bad_cursor", 400, "GET", "/v1/threads?cursor=nope", "", "")
	rec.probe(r, "list_threads_unauthorized", 401, "GET", "/v1/threads", "", "", bearer...)
	rec.probe(r, "get_thread", 200, "GET", "/v1/threads/thr_probe", "", "")
	rec.probe(r, "get_thread_unauthorized", 401, "GET", "/v1/threads/thr_probe", "", "", bearer...)
	rec.probe(r, "get_thread_not_found", 404, "GET", "/v1/threads/nope", "", "")

	// Triggers, and their inbound webhooks: a run of the webhook's trigger
	// waits at the gate, so that the next delivery is skipped.
	tm := echo(t)
	tm.gate = make(chan struct{}, 2)
	rt := newTrigRig(t, tm, trigConfig{token: token})
	sched := `{"id":"daily","prompt":"Summarize {{date}}","schedule":"CRON_TZ=Europe/Paris 0 9 * * 1-5"}`
	rec.probe(rt, "create_trigger", 201, "POST", "/v1/triggers", sched, "")
	rec.probe(rt, "create_trigger_invalid", 400, "POST", "/v1/triggers", `{"id":"bad","prompt":"x","schedule":"61 * * * *"}`, "")
	rec.probe(rt, "create_trigger_unauthorized", 401, "POST", "/v1/triggers", sched, "", bearer...)
	rec.probe(rt, "create_trigger_no_agent", 404, "POST", "/v1/triggers", `{"id":"x","agent":"nobody","prompt":"x","schedule":"@daily"}`, "")
	rec.probe(rt, "create_trigger_exists", 409, "POST", "/v1/triggers", sched, "")
	gh := decodeAs[wire.Trigger](t, rec.probe(rt, "create_trigger_webhook", 201, "POST", "/v1/triggers", `{"id":"gh","prompt":"Triage the issue","webhook":true}`, ""))
	rec.probe(rt, "list_triggers", 200, "GET", "/v1/triggers", "", "")
	rec.probe(rt, "list_triggers_unauthorized", 401, "GET", "/v1/triggers", "", "", bearer...)
	rec.probe(other, "list_triggers_none", 404, "GET", "/v1/triggers", "", "")
	rec.probe(rt, "get_trigger", 200, "GET", "/v1/triggers/gh", "", "")
	rec.probe(rt, "get_trigger_unauthorized", 401, "GET", "/v1/triggers/gh", "", "", bearer...)
	rec.probe(rt, "get_trigger_not_found", 404, "GET", "/v1/triggers/nope", "", "")
	rec.probe(rt, "update_trigger", 200, "PATCH", "/v1/triggers/daily", `{"prompt":"Summarize the news of {{date}}"}`, "")
	rec.probe(rt, "update_trigger_invalid", 400, "PATCH", "/v1/triggers/daily", `{"prompt":"{{nope}}"}`, "")
	rec.probe(rt, "update_trigger_unauthorized", 401, "PATCH", "/v1/triggers/daily", `{"prompt":"x"}`, "", bearer...)
	rec.probe(rt, "update_trigger_not_found", 404, "PATCH", "/v1/triggers/nope", `{"prompt":"x"}`, "")
	rec.probe(rt, "update_trigger_conflict", 409, "PATCH", "/v1/triggers/daily", `{"version":99,"prompt":"x"}`, "")
	rec.probe(rt, "pause_trigger", 200, "POST", "/v1/triggers/daily/pause", "", "")
	rec.probe(rt, "pause_trigger_unauthorized", 401, "POST", "/v1/triggers/daily/pause", "", "", bearer...)
	rec.probe(rt, "pause_trigger_not_found", 404, "POST", "/v1/triggers/nope/pause", "", "")
	rec.probe(rt, "resume_trigger", 200, "POST", "/v1/triggers/daily/resume", "", "")
	rec.probe(rt, "resume_trigger_unauthorized", 401, "POST", "/v1/triggers/daily/resume", "", "", bearer...)
	rec.probe(rt, "resume_trigger_not_found", 404, "POST", "/v1/triggers/nope/resume", "", "")

	key, err := trigger.DecodeStandardSecret(gh.Webhook.Secret)
	if err != nil {
		t.Fatal(err)
	}
	signed := func(id, body string) []string {
		at := rt.clock.Now()
		return []string{
			trigger.StandardHeaderID, id, trigger.StandardHeaderTimestamp, strconv.FormatInt(at.Unix(), 10),
			trigger.StandardHeaderSignature, trigger.SignStandard(key, id, at, []byte(body)),
		}
	}
	event := `{"action":"opened","issue":{"number":7}}`
	started := decodeAs[wire.HookAccepted](t, rec.probe(rt, "hook", 202, "POST", gh.Webhook.Path, event, "", signed("msg_1", event)...))
	rec.probe(rt, "hook_skipped", 200, "POST", gh.Webhook.Path, event, "", signed("msg_2", event)...)
	rec.probe(rt, "hook_not_json", 400, "POST", gh.Webhook.Path, "not json", `{"not":"json"}`, signed("msg_3", "not json")...)
	rec.probe(rt, "hook_unsigned", 401, "POST", gh.Webhook.Path, event, "")
	rec.probe(rt, "hook_not_found", 404, "POST", "/v1/hooks/hk_nope", event, "", signed("msg_4", event)...)
	rec.probe(rt, "hook_replayed", 409, "POST", gh.Webhook.Path, event, "", signed("msg_1", event)...)
	bigHook := `{"x":"` + strings.Repeat("x", agentapi.DefaultHookBodyBytes) + `"}`
	rec.probe(rt, "hook_too_large", 413, "POST", gh.Webhook.Path, bigHook, `{"x":"too long"}`, signed("msg_5", bigHook)...)
	tm.gate <- struct{}{}
	rt.waitEnded(started.Firing.RunID)
	rec.probe(rt, "list_firings", 200, "GET", "/v1/triggers/gh/firings?limit=5", "", "")
	rec.probe(rt, "list_firings_invalid", 400, "GET", "/v1/triggers/gh/firings?limit=0", "", "")
	rec.probe(rt, "list_firings_unauthorized", 401, "GET", "/v1/triggers/gh/firings", "", "", bearer...)
	rec.probe(rt, "list_firings_not_found", 404, "GET", "/v1/triggers/nope/firings", "", "")
	if err := rt.srv.SyncTriggers(t.Context(), []agentapi.StaticTrigger{{Tenant: rt.tn, ID: "static", Definition: trigger.Definition{Prompt: "x", Schedule: "@daily"}}}); err != nil {
		t.Fatal(err)
	}
	rec.probe(rt, "delete_trigger_static", 409, "DELETE", "/v1/triggers/static", "", "")
	rec.probe(rt, "delete_trigger_unauthorized", 401, "DELETE", "/v1/triggers/daily", "", "", bearer...)
	rec.probe(rt, "delete_trigger", 204, "DELETE", "/v1/triggers/daily", "", "")
	rec.probe(rt, "delete_trigger_not_found", 404, "DELETE", "/v1/triggers/daily", "", "")

	rec.probe(r, "delete_unauthorized", 401, "DELETE", "/v1/conversations/"+conv, "", "", bearer...)
	rec.probe(r, "delete", 204, "DELETE", "/v1/conversations/"+conv, "", "")
	rec.probe(r, "delete_not_found", 404, "DELETE", "/v1/conversations/"+conv, "", "")

	// A toolset whose tools cannot be listed: the catalog lists it as
	// unavailable.
	broken := []agentapi.Agent{{Name: "solo", Toolsets: []string{"down"}, Harness: agentHarness(t, echo(t), registry(t))}}
	rb := newRig(t, echo(t), rigConfig{agents: broken, api: []agentapi.Option{agentapi.WithToolsets(agentapi.Toolset{Name: "down", Type: "demo", Tools: failing{}, Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}})}})
	rec.probe(rb, "list_agents_unavailable", 200, "GET", "/v1/agents", "", "")
	rec.probe(rb, "get_agent_unavailable", 200, "GET", "/v1/agents/solo", "", "")
	rec.probe(rb, "list_toolsets_unavailable", 200, "GET", "/v1/toolsets", "", "")
	rec.probe(rb, "list_tools_unavailable", 200, "GET", "/v1/toolsets/down/tools", "", "")

	// The toolsets added at run time.
	ra, _, mcpURL := adminRig(t)
	addBody := `{"type":"mcp","name":"linear","url":"` + mcpURL + `"}`
	added := decodeAs[wire.ServerToolset](t, rec.probe(ra, "add_toolset", 201, "POST", "/v1/toolsets", addBody, ""))
	rec.probe(ra, "add_toolset_invalid", 400, "POST", "/v1/toolsets", `{"type":"mcp","name":"x","url":"https://10.0.0.1/mcp"}`, "")
	rec.probe(ra, "add_toolset_unauthorized", 401, "POST", "/v1/toolsets", addBody, "", bearer...)
	rec.probe(ra, "add_toolset_exists", 409, "POST", "/v1/toolsets", addBody, "")
	rec.probe(ra, "add_toolset_unreachable", 422, "POST", "/v1/toolsets", `{"type":"mcp","name":"x","url":"http://127.0.0.1:1/mcp"}`, "")
	rec.probe(ra, "get_review", 200, "GET", "/v1/toolsets/linear/review", "", "")
	rec.probe(ra, "get_review_unauthorized", 401, "GET", "/v1/toolsets/linear/review", "", "", bearer...)
	rec.probe(ra, "get_review_not_found", 404, "GET", "/v1/toolsets/nope/review", "", "")
	fp := added.Tools[slices.IndexFunc(added.Tools, func(rt wire.ReviewTool) bool { return rt.Name == "search" })].Fingerprint
	decision := `{"tools":[{"name":"search","fingerprint":"` + fp + `","decision":"approve","kind":"read"}]}`
	rec.probe(ra, "review_toolset", 200, "POST", "/v1/toolsets/linear/review", decision, "")
	rec.probe(ra, "review_toolset_invalid", 400, "POST", "/v1/toolsets/linear/review", `{"tools":[]}`, "")
	rec.probe(ra, "review_toolset_unauthorized", 401, "POST", "/v1/toolsets/linear/review", decision, "", bearer...)
	rec.probe(ra, "review_toolset_not_found", 404, "POST", "/v1/toolsets/nope/review", decision, "")
	rec.probe(ra, "review_toolset_conflict", 409, "POST", "/v1/toolsets/linear/review", strings.Replace(decision, fp, "sha256:"+strings.Repeat("0", 64), 1), "")
	rec.probe(ra, "list_toolsets_runtime", 200, "GET", "/v1/toolsets", "", "")
	rec.probe(ra, "update_toolset", 200, "PATCH", "/v1/toolsets/linear", `{"enabled":false}`, "")
	rec.probe(ra, "update_toolset_invalid", 400, "PATCH", "/v1/toolsets/linear", `{}`, "")
	rec.probe(ra, "update_toolset_unauthorized", 401, "PATCH", "/v1/toolsets/linear", `{"enabled":true}`, "", bearer...)
	rec.probe(ra, "update_toolset_not_found", 404, "PATCH", "/v1/toolsets/nope", `{"enabled":true}`, "")
	rec.probe(ra, "delete_toolset_unauthorized", 401, "DELETE", "/v1/toolsets/linear", "", "", bearer...)
	rec.probe(ra, "delete_toolset", 204, "DELETE", "/v1/toolsets/linear", "", "")
	rec.probe(ra, "delete_toolset_not_found", 404, "DELETE", "/v1/toolsets/linear", "", "")

	// Signing in and out.
	rs := newSigninRig(t)
	rec.probe(rs, "auth_config", 200, "GET", "/v1/auth/config", "", "")
	rec.probe(rs, "login_bad_request", 400, "POST", "/v1/auth/login", `{"username":"ann"}`, `{"username":"ann","password":"missing"}`)
	rec.probe(rs, "login_unauthorized", 401, "POST", "/v1/auth/login", `{"username":"ann","password":"wrong-password"}`, "")
	rec.probe(rs, "login_csrf", 403, "POST", "/v1/auth/login", `{"username":"ann","password":"wrong-password"}`, "", "Origin", "https://evil.example")
	rec.probe(r, "login_not_found", 404, "POST", "/v1/auth/login", `{"username":"ann","password":"wrong-password"}`, "")
	for range 9 {
		rs.do("POST", "/v1/auth/login", `{"username":"ann","password":"wrong-password"}`)
	}
	rec.probe(rs, "login_throttled", 429, "POST", "/v1/auth/login", `{"username":"ann","password":"wrong-password"}`, "")
	rs.clk.Advance(time.Hour)
	rec.probe(rs, "login", 204, "POST", "/v1/auth/login", `{"username":"root","password":"`+adminPassword+`"}`, `{"username":"root","password":"redacted"}`, "Origin", rs.ts.URL)
	sess := rs.signIn("root", adminPassword)
	rec.probe(rs, "me", 200, "GET", "/v1/auth/me", "", "", sess.headers(rs, false)...)
	rec.probe(rs, "me_unauthorized", 401, "GET", "/v1/auth/me", "", "", "Authorization", "Bearer not-a-key-0123456789")
	rec.probe(rs, "logout_csrf", 403, "POST", "/v1/auth/logout", "", "", sess.headers(rs, false)...)
	rec.probe(rs, "logout", 204, "POST", "/v1/auth/logout", "", "", sess.headers(rs, true)...)
	rec.probe(rs, "logout_unauthorized", 401, "POST", "/v1/auth/logout", "", "", sess.headers(rs, true)...)
	rec.probe(r, "logout_not_found", 404, "POST", "/v1/auth/logout", "", "")
	// Credentials (T045), as user u1 of a server that keeps them; the
	// rig r keeps none.
	cr := newCredRig(t, agentapi.WithMaxBodyBytes(512))
	big := `{"kind":"api_key","api_key":"` + strings.Repeat("x", 600) + `"}`
	rec.probe(r, "list_credentials_off", 404, "GET", "/v1/credentials", "", "")
	rec.probe(cr.rig, "put_credential", 200, "PUT", "/v1/credentials/mail", `{"kind":"api_key","api_key":"k"}`, "")
	rec.probe(cr.rig, "put_credential_invalid", 400, "PUT", "/v1/credentials/mail", `{"kind":"api_key"}`, "")
	rec.probe(cr.rig, "put_credential_unauthorized", 401, "PUT", "/v1/credentials/mail", `{"kind":"api_key","api_key":"k"}`, "", bearer...)
	rec.probe(cr.rig, "put_credential_not_found", 404, "PUT", "/v1/credentials/nope", `{"kind":"api_key","api_key":"k"}`, "")
	rec.probe(cr.rig, "put_credential_too_large", 413, "PUT", "/v1/credentials/mail", big, `{"kind":"api_key","api_key":"too long"}`)
	rec.probe(cr.rig, "list_credentials", 200, "GET", "/v1/credentials", "", "")
	rec.probe(cr.rig, "list_credentials_unauthorized", 401, "GET", "/v1/credentials", "", "", bearer...)
	rec.probe(cr.rig, "connect_credential", 200, "POST", "/v1/credentials/mail/connect", `{"return_url":"https://console.example.com/connections/done"}`, "")
	rec.probe(cr.rig, "connect_credential_invalid", 400, "POST", "/v1/credentials/mail/connect", `{"return_url":"https://evil.example.com/"}`, "")
	rec.probe(cr.rig, "connect_credential_unauthorized", 401, "POST", "/v1/credentials/mail/connect", `{}`, "", bearer...)
	rec.probe(cr.rig, "connect_credential_not_found", 404, "POST", "/v1/credentials/nope/connect", `{}`, "")
	rec.probe(cr.rig, "connect_credential_too_large", 413, "POST", "/v1/credentials/mail/connect", `{"return_url":"`+strings.Repeat("x", 600)+`"}`, `{"return_url":"too long"}`)
	rec.probe(cr.rig, "delete_credential_bad_scope", 400, "DELETE", "/v1/credentials/mail?scope=tenant", "", "")
	rec.probe(cr.rig, "delete_credential_unauthorized", 401, "DELETE", "/v1/credentials/mail", "", "", bearer...)
	rec.probe(cr.rig, "delete_credential", 204, "DELETE", "/v1/credentials/mail?scope=user", "", "")
	rec.probe(cr.rig, "delete_credential_not_found", 404, "DELETE", "/v1/credentials/mail", "", "")
	// The agent registry (T075).
	rg := registryRig(t)
	rec.probe(rg, "save_agent", 201, "POST", "/v1/agents/support/versions", `{"definition":{"system":"v1"},"message":"first","publish":true}`, "")
	rec.probe(rg, "save_agent_invalid", 400, "POST", "/v1/agents/support/versions", `{"definition":{"system":"x","toolsets":["exa"]}}`, "")
	rec.probe(rg, "save_agent_unauthorized", 401, "POST", "/v1/agents/support/versions", `{"definition":{"system":"x"}}`, "", bearer...)
	rec.probe(rg, "save_agent_not_found", 404, "POST", "/v1/agents/fresh/versions", `{"definition":{"system":"x"},"if_latest":3}`, "")
	rec.probe(rg, "save_agent_read_only", 409, "POST", "/v1/agents/default/versions", `{"definition":{"system":"x"}}`, "")
	rg.save("support", `{"system": "v2"}`, false)
	rec.probe(rg, "list_agent_versions", 200, "GET", "/v1/agents/support/versions", "", "")
	rec.probe(rg, "list_agent_versions_invalid", 400, "GET", "/v1/agents/a.b/versions", "", "")
	rec.probe(rg, "list_agent_versions_unauthorized", 401, "GET", "/v1/agents/support/versions", "", "", bearer...)
	rec.probe(rg, "list_agent_versions_not_found", 404, "GET", "/v1/agents/nope/versions", "", "")
	rec.probe(rg, "get_agent_version", 200, "GET", "/v1/agents/support/versions/1", "", "")
	rec.probe(rg, "get_agent_version_invalid", 400, "GET", "/v1/agents/support/versions/0", "", "")
	rec.probe(rg, "get_agent_version_unauthorized", 401, "GET", "/v1/agents/support/versions/1", "", "", bearer...)
	rec.probe(rg, "get_agent_version_not_found", 404, "GET", "/v1/agents/support/versions/9", "", "")
	rec.probe(rg, "publish_agent", 200, "POST", "/v1/agents/support/publish", `{"version":2}`, "")
	rec.probe(rg, "publish_agent_invalid", 400, "POST", "/v1/agents/support/publish", `{"version":0}`, `{"version":1}`)
	rec.probe(rg, "publish_agent_unauthorized", 401, "POST", "/v1/agents/support/publish", `{"version":2}`, "", bearer...)
	rec.probe(rg, "publish_agent_not_found", 404, "POST", "/v1/agents/nope/publish", `{"version":1}`, "")
	rec.probe(rg, "publish_agent_read_only", 409, "POST", "/v1/agents/default/publish", `{"version":1}`, "")
	rg.save("support", `{"system": "v3"}`, false)
	rec.probe(rg, "delete_agent_version_published", 409, "DELETE", "/v1/agents/support/versions/1", "", "")
	rec.probe(rg, "delete_agent_version_invalid", 400, "DELETE", "/v1/agents/support/versions/x", "", "")
	rec.probe(rg, "delete_agent_version_unauthorized", 401, "DELETE", "/v1/agents/support/versions/3", "", "", bearer...)
	rec.probe(rg, "delete_agent_version", 204, "DELETE", "/v1/agents/support/versions/3", "", "")
	rec.probe(rg, "delete_agent_version_not_found", 404, "DELETE", "/v1/agents/support/versions/3", "", "")
	rec.probe(rg, "list_agents_stored", 200, "GET", "/v1/agents", "", "")
	rec.probe(rg, "get_agent_stored", 200, "GET", "/v1/agents/support", "", "")
	rg.model.gate <- struct{}{}
	rec.probe(rg, "run_stored_agent", 200, "POST", "/v1/runs", `{"message":"hi","agent":"support"}`, "", jsonAccept...)

	// Keys without the scope an endpoint needs: 403 on every route. The
	// rig's own requests read, with the read key.
	tenant403, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	const readKey, runKey = "a-key-that-only-reads-0123", "a-key-that-only-runs-0123"
	keys := authtest.NewFake(map[string]auth.Principal{
		readKey: {Tenant: tenant403, Scopes: []string{agentapi.ScopeRead}, Toolsets: []string{"*"}},
		runKey:  {Tenant: tenant403, Scopes: []string{agentapi.ScopeRun}, Toolsets: []string{"*"}},
	})
	rf := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithAuthenticator(keys)}})
	rf.token = readKey
	for _, rt := range rf.srv.Routes() {
		if strings.Contains(rt, " /v1/auth/") || strings.Contains(rt, " /v1/hooks/") {
			continue // any credential may sign out and ask who it is; a webhook's signature is its credential
		}
		method, target, _ := strings.Cut(rt, " ")
		target = strings.NewReplacer("{id}", "x", "{name}", "x", "{slot}", "x", "{version}", "1").Replace(target)
		name := strings.ToLower(method) + strings.NewReplacer("/v1/", "_", "/", "_", "{", "", "}", "").Replace(strings.TrimPrefix(rt, method+" ")) + "_forbidden"
		if method == http.MethodGet {
			rec.probe(rf, name, 403, method, target, "", "", "Authorization", "Bearer "+runKey)
		} else {
			body := map[string]string{
				"POST /v1/runs": `{"message":"hi"}`, "POST /v1/runs/{id}/decisions": `{"path":"f/ok","approved":true}`,
				"POST /v1/runs/{id}/input": `{"answers":[{"id":"q1","text":"red"}]}`,
				"POST /v1/toolsets":        `{"type":"mcp","name":"x","url":"https://mcp.example.com/mcp"}`, "POST /v1/toolsets/{name}/review": `{"tools":[]}`,
				"PATCH /v1/toolsets/{name}":  `{"enabled":true}`,
				"POST /v1/tasks":             `{"input":"hi"}`,
				"PUT /v1/credentials/{slot}": `{"kind":"api_key","api_key":"k"}`, "POST /v1/credentials/{slot}/connect": `{}`,
				"POST /v1/agents/{name}/versions": `{"definition":{"system":"x"}}`, "POST /v1/agents/{name}/publish": `{"version":1}`,
				"POST /v1/triggers": `{"id":"x","prompt":"hi","schedule":"@daily"}`, "PATCH /v1/triggers/{id}": `{"prompt":"hi"}`,
			}[rt]
			rec.probe(rf, name, 403, method, target, body, "")
		}
	}

	var documented []string
	for op, statuses := range rec.ops {
		for _, st := range statuses {
			documented = append(documented, op+" "+st)
		}
	}
	if got, want := slices.Sorted(maps.Keys(rec.probed)), slices.Sorted(slices.Values(documented)); !slices.Equal(got, want) {
		t.Errorf("probed\n%s\nwant every documented status\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	toolsettest.ValidateCassettes(t, rec.dir, strictSpec(t, raw))
}
