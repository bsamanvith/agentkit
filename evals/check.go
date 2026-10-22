package evals

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// truncationMarker is how the connectors' content cut-off notes begin
// (connectors/internal/trunc): "…[truncated 12,400 characters; call ...]".
const truncationMarker = "…[truncated"

// Call is one tool call of a run and its result.
type Call struct {
	// Tool is the tool's name without a toolset prefix; Toolset the prefix
	// or, without one, the provider the tool belongs to.
	Tool    string          `json:"tool"`
	Toolset string          `json:"toolset"`
	Input   json.RawMessage `json:"input"`
	Result  string          `json:"result,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

// Transcript is what a run did, read from its agent API events.
type Transcript struct {
	Calls  []Call `json:"calls"`
	Answer string `json:"answer"`
	// Error is the run_error's code and message, if the run failed.
	Error        string `json:"error,omitempty"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// ReadTranscript builds a transcript from the events of `agentkit ask -json`.
func ReadTranscript(events []wire.Event) Transcript {
	var tr Transcript
	byID := map[string]int{}
	for _, e := range events {
		switch e.Type {
		case wire.TypeToolCall:
			toolset, name := splitTool(e.Name)
			byID[e.ToolUseID] = len(tr.Calls)
			tr.Calls = append(tr.Calls, Call{Tool: name, Toolset: toolset, Input: e.Input})
		case wire.TypeToolResult:
			if i, ok := byID[e.ToolUseID]; ok {
				tr.Calls[i].Result, tr.Calls[i].IsError = e.Content, e.IsError
			}
		case wire.TypeRunDone:
			tr.Answer = e.Answer
		case wire.TypeRunError:
			tr.Error = "run_error"
			if e.Error != nil {
				tr.Error = e.Error.Code + ": " + e.Error.Message
			}
		}
		if (e.Type == wire.TypeRunDone || e.Type == wire.TypeRunError) && e.Usage != nil {
			tr.InputTokens = e.Usage.InputTokens + e.Usage.CacheReadTokens + e.Usage.CacheWriteTokens
			tr.OutputTokens = e.Usage.OutputTokens
		}
	}
	return tr
}

// splitTool splits "<toolset>__<tool>" (prefix_tool_names); a name without
// a prefix belongs to the provider its name starts with.
func splitTool(name string) (toolset, tool string) {
	if ts, t, ok := strings.Cut(name, "__"); ok {
		return ts, t
	}
	ts, _, _ := strings.Cut(name, "_")
	return ts, name
}

// Check returns the checks the run failed, each as one line; none means it
// passed. A failed run fails every scenario.
func Check(c Checks, tr Transcript) []string {
	var fails []string
	failf := func(format string, args ...any) { fails = append(fails, fmt.Sprintf(format, args...)) }
	if tr.Error != "" {
		failf("the run failed: %s", tr.Error)
	}
	called := map[string]int{}
	for _, call := range tr.Calls {
		called[call.Tool]++
	}
	for _, t := range c.Called {
		if called[t] == 0 {
			failf("called: %s was not called (calls: %s)", t, callList(tr))
		}
	}
	for _, t := range c.NotCalled {
		if called[t] > 0 {
			failf("not_called: %s was called %d times", t, called[t])
		}
	}
	if c.First != "" && (len(tr.Calls) == 0 || tr.Calls[0].Tool != c.First) {
		failf("first: the first call was not %s (calls: %s)", c.First, callList(tr))
	}
	if c.MaxCalls != nil && len(tr.Calls) > *c.MaxCalls {
		failf("max_calls: %d calls, at most %d allowed (calls: %s)", len(tr.Calls), *c.MaxCalls, callList(tr))
	}
	for _, a := range c.Args {
		if msg := checkArgs(a, tr); msg != "" {
			failf("args: %s", msg)
		}
	}
	if c.CitesResultURL && !citesResult(tr) {
		failf("cites_result_url: the answer gives no URL from a tool result")
	}
	if c.After != nil {
		if msg := checkAfter(*c.After, tr); msg != "" {
			failf("after: %s", msg)
		}
	}
	for _, want := range c.AnswerContains {
		if !strings.Contains(strings.ToLower(tr.Answer), strings.ToLower(want)) {
			failf("answer_contains: the answer does not say %q", want)
		}
	}
	if c.CitesSource && !citesSource(tr) {
		failf("cites_source: the answer names no document a knowledge result came from")
	}
	if c.OneProvider {
		providers := map[string]bool{}
		for _, call := range tr.Calls {
			providers[call.Toolset] = true
		}
		if len(providers) > 1 {
			failf("one_provider: calls went to %d toolsets (calls: %s)", len(providers), callList(tr))
		}
	}
	return fails
}

func callList(tr Transcript) string {
	if len(tr.Calls) == 0 {
		return "none"
	}
	names := make([]string, len(tr.Calls))
	for i, c := range tr.Calls {
		names[i] = c.Tool
	}
	return strings.Join(names, ", ")
}

func checkArgs(a ArgCheck, tr Transcript) string {
	n := 0
	for _, call := range tr.Calls {
		if call.Tool != a.Tool {
			continue
		}
		n++
		if msg := checkField(a, call.Input); msg != "" {
			return fmt.Sprintf("%s call %d: %s (input %s)", a.Tool, n, msg, call.Input)
		}
		if !a.Every {
			return ""
		}
	}
	if n == 0 {
		return fmt.Sprintf("%s was not called, so its %s could not be checked", a.Tool, a.Field)
	}
	return ""
}

func checkField(a ArgCheck, input json.RawMessage) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return "the input is not a JSON object"
	}
	raw, ok := fields[a.Field]
	empty := !ok || string(raw) == "null" || string(raw) == `""` || string(raw) == "[]"
	if a.Absent {
		if !empty {
			return fmt.Sprintf("%s is set to %s, want it absent", a.Field, raw)
		}
		return ""
	}
	if empty {
		return fmt.Sprintf("%s is not set", a.Field)
	}
	var values []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		values = []string{one}
	} else if json.Unmarshal(raw, &values) != nil {
		values = []string{string(raw)}
	}
	if a.Contains != "" && !slices.ContainsFunc(values, func(v string) bool {
		return strings.Contains(strings.ToLower(v), strings.ToLower(a.Contains))
	}) {
		return fmt.Sprintf("%s is %s, which does not contain %q", a.Field, raw, a.Contains)
	}
	if len(a.OneOf) > 0 && !slices.ContainsFunc(values, func(v string) bool { return slices.Contains(a.OneOf, v) }) {
		return fmt.Sprintf("%s is %s, want one of %q", a.Field, raw, a.OneOf)
	}
	if a.AtLeast != "" && (len(values) == 0 || len(values[0]) < len(a.AtLeast) || values[0][:len(a.AtLeast)] < a.AtLeast) {
		return fmt.Sprintf("%s is %s, want %s or later", a.Field, raw, a.AtLeast)
	}
	return ""
}

var urlPattern = regexp.MustCompile(`https?://[^\s"'<>()\[\]{}\\]+`)

// urls returns the URLs in s, without trailing punctuation.
func urls(s string) []string {
	found := urlPattern.FindAllString(s, -1)
	for i, u := range found {
		found[i] = strings.TrimRight(u, ".,;:!?*_`")
	}
	return found
}

func citesResult(tr Transcript) bool {
	answer := urls(tr.Answer)
	for _, call := range tr.Calls {
		if call.IsError {
			continue
		}
		for _, u := range urls(call.Result) {
			for _, a := range answer {
				if sameURL(a, u) {
					return true
				}
			}
		}
	}
	return false
}

// citesSource reports whether the answer names, by its source ("returns.md"),
// a document of a knowledge_search or knowledge_read result. A title or a
// file name without its extension is not enough: they are words an answer
// has anyway ("warranty").
func citesSource(tr Transcript) bool {
	answer := strings.ToLower(tr.Answer)
	for _, call := range tr.Calls {
		if call.IsError || !strings.HasPrefix(call.Tool, "knowledge_") {
			continue
		}
		var out struct {
			Source  string                    `json:"source"`
			Results []struct{ Source string } `json:"results"`
		}
		if json.Unmarshal([]byte(call.Result), &out) != nil {
			continue
		}
		sources := []string{out.Source}
		for _, r := range out.Results {
			sources = append(sources, r.Source)
		}
		for _, src := range sources {
			if src != "" && strings.Contains(answer, strings.ToLower(src)) {
				return true
			}
		}
	}
	return false
}

// sameURL compares URLs ignoring a trailing slash.
func sameURL(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

func checkAfter(a AfterCheck, tr Transcript) string {
	for i, call := range tr.Calls {
		if call.Tool != a.Tool {
			continue
		}
		hit := call.IsError
		if a.On == "truncation" {
			hit = !call.IsError && strings.Contains(call.Result, truncationMarker)
		}
		if !hit {
			continue
		}
		if i+1 == len(tr.Calls) {
			return fmt.Sprintf("no call followed the %s of %s (want one of %s)", a.On, a.Tool, strings.Join(a.Next, ", "))
		}
		next := tr.Calls[i+1]
		if !slices.Contains(a.Next, next.Tool) {
			return fmt.Sprintf("after the %s of %s came %s, want one of %s", a.On, a.Tool, next.Tool, strings.Join(a.Next, ", "))
		}
		if a.URLFromResult && !slices.ContainsFunc(urls(string(next.Input)), func(u string) bool {
			return slices.ContainsFunc(urls(call.Result), func(r string) bool { return sameURL(u, r) })
		}) {
			return fmt.Sprintf("%s's input %s has no URL from the %s result", next.Tool, next.Input, a.Tool)
		}
		return ""
	}
	return fmt.Sprintf("no result of %s had a%s %s", a.Tool, map[bool]string{true: "n", false: ""}[a.On == "error"], a.On)
}
