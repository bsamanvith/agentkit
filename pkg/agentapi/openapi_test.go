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
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
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

	srv, err := agentapi.New(agentHarness(t, echo(t), registry(t)))
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

// probe sends a request on r, checks it gets status, and records it. A body
// that is not a valid request (a probe of a 400) is recorded as reqBody.
func (rec *recorder) probe(r *rig, name string, status int, method, target, body, reqBody string, header ...string) []byte {
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
	agents := []agentapi.Agent{
		{
			Name: agentapi.DefaultAgent, Description: "The default.", Model: wire.Model{Provider: "anthropic", ID: "claude", Effort: "low"}, System: "Be brief.",
			Toolsets: []string{"a", "web"}, Harness: agentHarness(t, m, sets["a"]),
		},
		{Name: "other", Harness: agentHarness(t, echo(t), registry(t))},
	}
	r := newRig(t, m, rigConfig{agents: agents, token: token, api: []agentapi.Option{
		toolsets, agentapi.WithMaxConcurrentRuns(1), agentapi.WithMaxBodyBytes(512),
	}})
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

	rec.probe(r, "list_conversations", 200, "GET", "/v1/conversations?limit=1", "", "")
	rec.probe(r, "list_conversations_bad_limit", 400, "GET", "/v1/conversations?limit=0", "", "")
	rec.probe(r, "list_conversations_unauthorized", 401, "GET", "/v1/conversations", "", "", bearer...)
	rec.probe(r, "list_conversations_no_agent", 404, "GET", "/v1/conversations?agent=nobody", "", "")

	var created agentapi.Conversation
	if err := json.Unmarshal(rec.probe(r, "create_conversation", 201, "POST", "/v1/conversations", `{"agent":"other"}`, ""), &created); err != nil {
		t.Fatal(err)
	}
	rec.probe(r, "run_in_created", 200, "POST", "/v1/runs", `{"message":"hi","conversation_id":"`+created.ID+`"}`, "", jsonAccept...)
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
	rec.probe(r, "delete_busy", 409, "DELETE", "/v1/conversations/"+busy, "", "")
	m.gate <- struct{}{}
	s2.all()

	rec.probe(r, "delete_unauthorized", 401, "DELETE", "/v1/conversations/"+conv, "", "", bearer...)
	rec.probe(r, "delete", 204, "DELETE", "/v1/conversations/"+conv, "", "")
	rec.probe(r, "delete_not_found", 404, "DELETE", "/v1/conversations/"+conv, "", "")

	// A toolset whose tools cannot be listed: the catalog answers 500.
	broken := []agentapi.Agent{{Name: "solo", Toolsets: []string{"down"}, Harness: agentHarness(t, echo(t), registry(t))}}
	rb := newRig(t, echo(t), rigConfig{agents: broken, api: []agentapi.Option{agentapi.WithToolsets(agentapi.Toolset{Name: "down", Type: "demo", Tools: failing{}})}})
	rec.probe(rb, "list_agents_internal", 500, "GET", "/v1/agents", "", "")
	rec.probe(rb, "get_agent_internal", 500, "GET", "/v1/agents/solo", "", "")
	rec.probe(rb, "list_toolsets_internal", 500, "GET", "/v1/toolsets", "", "")
	rec.probe(rb, "list_tools_internal", 500, "GET", "/v1/toolsets/down/tools", "", "")

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
