package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Providers are the connectors the scenarios may offer, with each one's
// endpoints (the paths its upstream serves).
var Providers = map[string][]string{
	"tavily":   {"/search", "/extract"},
	"exa":      {"/search", "/contents"},
	"parallel": {"/v1/search", "/v1/extract"},
}

// Knowledge is the toolset type a scenario may offer besides the
// connectors: the knowledge base of pkg/knowledge over KnowledgeRoot, with
// no upstream to fake.
const Knowledge = "knowledge"

// KnowledgeRoot is the knowledge base the scenarios search, relative to the
// repository root: the sample handbook the support agent answers from.
const KnowledgeRoot = "deploy/local/agent/knowledge"

// toolsOf are the tools of a toolset a scenario offers.
func toolsOf(ts string) []string {
	if ts == Knowledge {
		return []string{"knowledge_search", "knowledge_list", "knowledge_read"}
	}
	return []string{ts + "_search", ts + "_extract"}
}

// The agents a scenario may run: the deployed search agent (the default)
// and the support agent, which answers from the knowledge base
// (deploy/local/agent/search.json has both).
const (
	AgentSearch  = "search"
	AgentSupport = "support"
)

// Scenario is one evals/connectors/*.json file: a prompt, the connectors
// offered, what their faked upstreams answer, and the checks on the run.
type Scenario struct {
	// Name is the file's name without .json.
	Name string `json:"-"`
	// Behavior is what the scenario tests, in a few words ("known URL goes
	// straight to extract").
	Behavior string `json:"behavior"`
	// Prompt is the user message.
	Prompt string `json:"prompt"`
	// Agent is whose system prompt the run has: AgentSearch (the default)
	// or AgentSupport.
	Agent string `json:"agent,omitempty"`
	// Toolsets are the connectors offered, by type (tavily, exa, parallel,
	// or knowledge); each is a toolset of that name.
	Toolsets []string `json:"toolsets"`
	// PrefixToolNames names the tools "<toolset>__<tool>". Checks name
	// tools without the prefix either way.
	PrefixToolNames bool `json:"prefix_tool_names,omitempty"`
	// Include, by toolset, keeps only these of its tools (the toolset's
	// include list); the default is all of them.
	Include map[string][]string `json:"include,omitempty"`
	// Settings are added to a toolset's settings (by toolset), over the
	// base_url and key the eval sets.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// Upstream is what each toolset's faked API answers, by toolset and
	// endpoint: the responses in order, the last one repeated. An endpoint
	// left out answers with its default (DefaultResponses).
	Upstream map[string]map[string][]Response `json:"upstream,omitempty"`
	// Checks are what a run must satisfy to pass.
	Checks Checks `json:"checks"`
	// Misleading are tool descriptions (by toolset, then tool) that should
	// make the run fail its checks: `make eval-misleading` swaps them in to
	// prove the checks can fail.
	Misleading map[string]map[string]string `json:"misleading,omitempty"`
}

// Response is one scripted answer of a faked upstream: a recorded cassette
// (a raw HTTP response, as T049's cassettes are), or a status, headers and a
// body given here or in a file.
type Response struct {
	// Cassette is a raw HTTP response file, relative to the repository root.
	Cassette string `json:"cassette,omitempty"`
	// Status defaults to 200.
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Body is a JSON body; BodyFile one in a file relative to the
	// repository root.
	Body     json.RawMessage `json:"body,omitempty"`
	BodyFile string          `json:"body_file,omitempty"`
}

// Checks are the checks on one run. Tool names are given without a toolset
// prefix.
type Checks struct {
	// Called tools must each be called at least once.
	Called []string `json:"called,omitempty"`
	// NotCalled tools must not be called.
	NotCalled []string `json:"not_called,omitempty"`
	// First, if set, must be the first tool called.
	First string `json:"first,omitempty"`
	// MaxCalls limits the run's tool calls; nil means no limit, 0 none.
	MaxCalls *int `json:"max_calls,omitempty"`
	// Args are checks on the input of calls of a tool.
	Args []ArgCheck `json:"args,omitempty"`
	// CitesResultURL: the answer gives a URL that a tool result had.
	CitesResultURL bool `json:"cites_result_url,omitempty"`
	// After checks the call that follows a tool result with an error or a
	// truncation marker.
	After *AfterCheck `json:"after,omitempty"`
	// OneProvider: every call goes to the tools of one toolset.
	OneProvider bool `json:"one_provider,omitempty"`
	// AnswerContains: the answer has each of these texts,
	// case-insensitively (a figure the tools gave, say).
	AnswerContains []string `json:"answer_contains,omitempty"`
	// CitesSource: the answer names a document a knowledge result came
	// from, by its source ("returns.md").
	CitesSource bool `json:"cites_source,omitempty"`
}

// ArgCheck checks a field of the input of the calls of Tool: the first call
// (or, with Every, every call) must have it, and its value must satisfy the
// rest. Field is a top-level input field.
type ArgCheck struct {
	Tool  string `json:"tool"`
	Field string `json:"field"`
	Every bool   `json:"every,omitempty"`
	// Contains: the value (a string, or a list of strings) contains this
	// text, case-insensitively.
	Contains string `json:"contains,omitempty"`
	// OneOf: the value is one of these strings.
	OneOf []string `json:"one_of,omitempty"`
	// AtLeast: the value, a date or time string, is not before this date
	// (compared as text: YYYY-MM-DD).
	AtLeast string `json:"at_least,omitempty"`
	// Absent: the field must not be set (or be null or empty).
	Absent bool `json:"absent,omitempty"`
}

// AfterCheck checks what the model does after a tool result: the first
// result of Tool that is an error (On "error") or holds the truncation
// marker (On "truncation") must be followed by a call of one of Next, and,
// with URLFromResult, one whose input holds a URL from that result.
type AfterCheck struct {
	Tool          string   `json:"tool"`
	On            string   `json:"on"`
	Next          []string `json:"next"`
	URLFromResult bool     `json:"url_from_result,omitempty"`
}

// DefaultResponses answer an endpoint a scenario does not script: recorded
// cassettes about the Go 1.27 release, the scenarios' topic.
var DefaultResponses = map[string]map[string][]Response{
	"tavily": {
		"/search":  {{Cassette: "connectors/tavily/testdata/cassettes/search-news-answer.res.http"}},
		"/extract": {{Cassette: "connectors/tavily/testdata/cassettes/extract-partial-failure.res.http"}},
	},
	"exa": {
		"/search":   {{Cassette: "connectors/exa/testdata/cassettes/search-text.res.http"}},
		"/contents": {{Cassette: "connectors/exa/testdata/cassettes/contents-partial.res.http"}},
	},
	"parallel": {
		"/v1/search":  {{Cassette: "connectors/parallel/testdata/cassettes/search-defaults.res.http"}},
		"/v1/extract": {{Cassette: "connectors/parallel/testdata/cassettes/extract-full.res.http"}},
	},
}

var scenarioName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// LoadScenarios reads every *.json in dir. root is the repository root,
// which files named in responses are relative to; each must exist.
func LoadScenarios(dir, root string) ([]Scenario, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no scenarios in %s", dir)
	}
	var out []Scenario
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s Scenario
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		s.Name = strings.TrimSuffix(filepath.Base(f), ".json")
		if err := s.validate(root); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func (s *Scenario) validate(root string) error {
	if !scenarioName.MatchString(s.Name) {
		return fmt.Errorf("name %q: lower case letters, digits and dashes", s.Name)
	}
	if s.Prompt == "" || s.Behavior == "" {
		return fmt.Errorf("prompt and behavior are required")
	}
	if len(s.Toolsets) == 0 {
		return fmt.Errorf("no toolsets")
	}
	if s.Agent != "" && s.Agent != AgentSearch && s.Agent != AgentSupport {
		return fmt.Errorf("agent %q: %q or %q", s.Agent, AgentSearch, AgentSupport)
	}
	tools := map[string]bool{}
	for _, ts := range s.Toolsets {
		if _, ok := Providers[ts]; !ok && ts != Knowledge {
			return fmt.Errorf("toolset %q: one of tavily, exa, parallel, knowledge", ts)
		}
		for _, n := range toolsOf(ts) {
			tools[n] = true
		}
	}
	for ts, names := range s.Include {
		if !slices.Contains(s.Toolsets, ts) || len(names) == 0 {
			return fmt.Errorf("include for %q, which is not offered, or empty", ts)
		}
		for _, n := range names {
			if !tools[n] || !strings.HasPrefix(n, ts+"_") {
				return fmt.Errorf("include: %s has no tool %q", ts, n)
			}
		}
		for name := range tools {
			if strings.HasPrefix(name, ts+"_") && !slices.Contains(names, name) {
				delete(tools, name)
			}
		}
	}
	for ts := range s.Settings {
		if !slices.Contains(s.Toolsets, ts) {
			return fmt.Errorf("settings for %q, which is not offered", ts)
		}
	}
	for ts, eps := range s.Upstream {
		if !slices.Contains(s.Toolsets, ts) {
			return fmt.Errorf("upstream for %q, which is not offered", ts)
		}
		for ep, rs := range eps {
			if !slices.Contains(Providers[ts], ep) {
				return fmt.Errorf("upstream %s: no endpoint %q (%v)", ts, ep, Providers[ts])
			}
			if len(rs) == 0 {
				return fmt.Errorf("upstream %s %s: no responses", ts, ep)
			}
			for _, r := range rs {
				if err := r.validate(root); err != nil {
					return fmt.Errorf("upstream %s %s: %w", ts, ep, err)
				}
			}
		}
	}
	for ts, descs := range s.Misleading {
		if !slices.Contains(s.Toolsets, ts) {
			return fmt.Errorf("misleading descriptions for %q, which is not offered", ts)
		}
		for name, d := range descs {
			if !tools[name] || d == "" {
				return fmt.Errorf("misleading: %s has no tool %q, or its description is empty", ts, name)
			}
		}
	}
	return s.Checks.validate(tools)
}

func (r Response) validate(root string) error {
	n := 0
	for _, set := range []bool{r.Cassette != "", r.Body != nil, r.BodyFile != ""} {
		if set {
			n++
		}
	}
	if n > 1 || n == 0 && r.Status == 0 {
		return fmt.Errorf("a response is one of cassette, body or body_file (or a status alone)")
	}
	if r.Body != nil && !json.Valid(r.Body) {
		return fmt.Errorf("body is not JSON")
	}
	for _, f := range []string{r.Cassette, r.BodyFile} {
		if f == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			return err
		}
	}
	return nil
}

func (c Checks) validate(tools map[string]bool) error {
	known := func(name string) error {
		if !tools[name] {
			return fmt.Errorf("checks name %q, which is not an offered tool", name)
		}
		return nil
	}
	for _, list := range [][]string{c.Called, c.NotCalled} {
		for _, n := range list {
			if err := known(n); err != nil {
				return err
			}
		}
	}
	if c.First != "" {
		if err := known(c.First); err != nil {
			return err
		}
	}
	if c.CitesSource && !tools["knowledge_search"] {
		return fmt.Errorf("cites_source needs the knowledge toolset")
	}
	for _, a := range c.AnswerContains {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("answer_contains has an empty text")
		}
	}
	for _, a := range c.Args {
		if err := known(a.Tool); err != nil {
			return err
		}
		if a.Field == "" {
			return fmt.Errorf("args check on %s without a field", a.Tool)
		}
	}
	if a := c.After; a != nil {
		if err := known(a.Tool); err != nil {
			return err
		}
		if a.On != "error" && a.On != "truncation" {
			return fmt.Errorf(`after.on is "error" or "truncation"`)
		}
		if len(a.Next) == 0 {
			return fmt.Errorf("after.next is empty")
		}
		for _, n := range a.Next {
			if err := known(n); err != nil {
				return err
			}
		}
	}
	if c.MaxCalls == nil && len(c.Called) == 0 && len(c.NotCalled) == 0 && c.First == "" &&
		len(c.Args) == 0 && !c.CitesResultURL && c.After == nil && !c.OneProvider &&
		len(c.AnswerContains) == 0 && !c.CitesSource {
		return fmt.Errorf("no checks")
	}
	return nil
}
