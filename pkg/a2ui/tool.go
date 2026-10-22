package a2ui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Tool returns ui_render, the tool with which an agent shows the person it
// works for UI. Its input is {"messages": [A2UI message, ...]}, checked
// with [ParseInput]: a problem is an error matching tool.ErrInvalidInput
// whose message lists each problem with its JSON pointer, for the model to
// fix and call again. A valid call returns the surfaces it drew; the agent
// API streams its messages to the client as "ui" events.
func Tool() tool.Tool { return renderTool{} }

// Output is ui_render's result.
type Output struct {
	// Surfaces are the surfaces the call drew, in order.
	Surfaces []string `json:"surfaces"`
	// Messages counts its messages.
	Messages int `json:"messages"`
	// Note tells the model what comes next.
	Note string `json:"note"`
}

const outputNote = "Shown to the user. If they act on it, their next message is the action between <ui_action> tags; otherwise they may just reply."

type renderTool struct{}

func (renderTool) Definition() tool.Definition {
	return tool.Definition{
		Name:         ToolName,
		Description:  description(),
		InputSchema:  json.RawMessage(inputSchema),
		OutputSchema: json.RawMessage(outputSchema),
		Metadata: tool.Metadata{
			Kind: tool.KindRead, Risk: tool.RiskLow,
			Display: tool.Display{Title: "Show UI", Icon: "ui", Present: "Showing you a form", Past: "Showed you a form"},
		},
	}
}

func (renderTool) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msgs, err := ParseInput(input)
	if err != nil {
		return nil, fmt.Errorf("%w: the UI was not shown; fix these and call %s again:\n%w", tool.ErrInvalidInput, ToolName, err)
	}
	out := Output{Messages: len(msgs), Note: outputNote}
	for _, m := range msgs {
		if id := m.SurfaceID(); !slices.Contains(out.Surfaces, id) {
			out.Surfaces = append(out.Surfaces, id)
		}
	}
	return json.Marshal(out)
}

const inputSchema = `{
  "type": "object",
  "properties": {
    "messages": {
      "type": "array",
      "minItems": 1,
      "maxItems": 16,
      "description": "A2UI v0.9 messages, in order: createSurface, then updateComponents (the surface's whole component tree) and updateDataModel; or updateComponents/updateDataModel/deleteSurface for a surface shown earlier.",
      "items": {"type": "object"}
    }
  },
  "required": ["messages"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "surfaces": {"type": "array", "items": {"type": "string"}},
    "messages": {"type": "integer"},
    "note": {"type": "string"}
  },
  "required": ["surfaces", "messages", "note"]
}`

// description is ui_render's description: when to use it, the messages,
// the catalog (from its tables) and an example.
func description() string {
	descriptionOnce.Do(func() { descriptionText = describe() })
	return descriptionText
}

var (
	descriptionOnce sync.Once
	descriptionText string
)

// describe builds the description.
func describe() string {
	var b strings.Builder
	b.WriteString(`Show the user a small UI (a form, a set of choices, a card) instead of plain text, with A2UI v0.9 messages. Use it when the user should pick, fill in or confirm something; do not use it for plain answers.

Each message is {"version": "v0.9", "<kind>": {...}} with one kind:
- createSurface {surfaceId, catalogId: "` + BasicCatalogID + `"}: first, once per surface. Pick a new descriptive surfaceId.
- updateComponents {surfaceId, components: [...]}: a flat list of components that name each other by id; one has id "root". Every call that updates components sends the whole tree, and every id it names is defined in it.
- updateDataModel {surfaceId, path?: "/a/b", value?}: the data the inputs bind to (a missing value removes the path).
- deleteSurface {surfaceId}.

A component is {"id": "...", "component": "<Type>", ...properties}. Text values are a literal, a binding {"path": "/form/name"} to the data model, or a function call {"call": "formatString", "args": {...}}. Inputs (TextField, CheckBox, ChoicePicker, DateTimeInput) bind "value" to a path; a Button's action {"event": {"name": "...", "context": {"key": {"path": "/form/x"}}}} sends the bound values back. Image and link URLs must be absolute https (or http) URLs.

Components (* required):
`)
	for _, typ := range Components() {
		spec := catalog().components[typ]
		var ps []string
		for _, name := range names(spec) {
			if name == "accessibility" || name == "weight" || name == "checks" {
				continue
			}
			s := name
			if spec[name].required {
				s += "*"
			}
			if spec[name].kind == kEnum {
				s += ": " + strings.Join(spec[name].enum, "|")
			}
			ps = append(ps, s)
		}
		b.WriteString("- " + typ + " {" + strings.Join(ps, ", ") + "}\n")
	}
	b.WriteString(`Every component may have accessibility {label, description}, weight (in a Row or Column) and, for inputs and buttons, checks [{"condition": {"call": ...}, "message": "..."}]. Functions: ` +
		strings.Join(sortedKeys(catalog().functions), ", ") + `.

When the user acts, their next message is <ui_action>{"version": "v0.9", "action": {"name", "surfaceId", "sourceComponentId", "timestamp", "context"}}</ui_action>, context holding the values.

Example: {"messages": [
 {"version": "v0.9", "createSurface": {"surfaceId": "pick_plan", "catalogId": "` + BasicCatalogID + `"}},
 {"version": "v0.9", "updateComponents": {"surfaceId": "pick_plan", "components": [
  {"id": "root", "component": "Card", "child": "col"},
  {"id": "col", "component": "Column", "children": ["title", "plan", "ok"]},
  {"id": "title", "component": "Text", "text": "Which plan?", "variant": "h3"},
  {"id": "plan", "component": "ChoicePicker", "options": [{"label": "Basic", "value": "basic"}, {"label": "Pro", "value": "pro"}], "value": {"path": "/plan"}},
  {"id": "ok_label", "component": "Text", "text": "Choose"},
  {"id": "ok", "component": "Button", "child": "ok_label", "variant": "primary", "action": {"event": {"name": "choose_plan", "context": {"plan": {"path": "/plan"}}}}}]}},
 {"version": "v0.9", "updateDataModel": {"surfaceId": "pick_plan", "path": "/plan", "value": ["basic"]}}]}`)
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
