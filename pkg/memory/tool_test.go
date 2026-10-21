package memory_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func newTool(t *testing.T, cfg memory.Config) *tool.FuncTool[memory.ToolInput, string] {
	t.Helper()
	s, err := inmem.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mt, err := memory.NewTool(s)
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

// TestToolCommands: each command, through the tool on the reference store,
// gives the Claude memory tool's documented text, including its error cases:
// str_replace with zero or two matches, insert past the end, rename onto an
// existing file and view of a missing path. The steps run in order on one
// store.
func TestToolCommands(t *testing.T) {
	t.Parallel()
	mt := newTool(t, memory.Config{})
	ctx := memorytest.Ctx(t, "t1", "")
	const (
		ok = iota
		failed
		invalid
	)
	for _, step := range []struct {
		name  string
		input string
		want  string
		kind  int
	}{
		{
			"view an empty root", `{"command":"view","path":"/memories"}`,
			"Here're the files and directories up to 2 levels deep in /memories, excluding hidden items and node_modules:\n0\t/memories", ok,
		},
		{
			"view a missing path", `{"command":"view","path":"/memories/prefs.md"}`,
			"The path /memories/prefs.md does not exist. Please provide a valid path.", failed,
		},
		{
			"create", `{"command":"create","path":"/memories/prefs.md","file_text":"units: metric\ntone: brief\n"}`,
			"File created successfully at: /memories/prefs.md", ok,
		},
		{
			"view the file", `{"command":"view","path":"/memories/prefs.md"}`,
			"Here's the content of /memories/prefs.md with line numbers:\n     1\tunits: metric\n     2\ttone: brief", ok,
		},
		{
			"view a range", `{"command":"view","path":"/memories/prefs.md","view_range":[2,-1]}`,
			"Here's the content of /memories/prefs.md with line numbers:\n     2\ttone: brief", ok,
		},
		{"view a range outside the file", `{"command":"view","path":"/memories/prefs.md","view_range":[3,4]}`, "", invalid},
		{
			"str_replace", `{"command":"str_replace","path":"/memories/prefs.md","old_str":"brief","new_str":"warm"}`,
			"The memory file has been edited. Here is a snippet of /memories/prefs.md with line numbers:\n     1\tunits: metric\n     2\ttone: warm", ok,
		},
		{
			"str_replace with no match", `{"command":"str_replace","path":"/memories/prefs.md","old_str":"imperial","new_str":"x"}`,
			"No replacement was performed, old_str `imperial` did not appear verbatim in /memories/prefs.md.", failed,
		},
		{
			"create a file with a repeated line", `{"command":"create","path":"/memories/log.md","file_text":"a\nx\nb\nx\n"}`,
			"File created successfully at: /memories/log.md", ok,
		},
		{
			"str_replace with two matches", `{"command":"str_replace","path":"/memories/log.md","old_str":"x","new_str":"y"}`,
			"No replacement was performed. Multiple occurrences of old_str `x` in lines: 2, 4. Please ensure it is unique", failed,
		},
		{
			"str_replace on a missing file", `{"command":"str_replace","path":"/memories/none.md","old_str":"x","new_str":"y"}`,
			"Error: The path /memories/none.md does not exist. Please provide a valid path.", failed,
		},
		{
			"insert", `{"command":"insert","path":"/memories/log.md","insert_line":0,"insert_text":"start\n"}`,
			"The file /memories/log.md has been edited.", ok,
		},
		{
			"insert past the end", `{"command":"insert","path":"/memories/log.md","insert_line":9,"insert_text":"x"}`,
			"Error: Invalid `insert_line` parameter: 9. It should be within the range of lines of the file: [0, 5]", failed,
		},
		{
			"insert into a missing file", `{"command":"insert","path":"/memories/none.md","insert_line":0,"insert_text":"x"}`,
			"Error: The path /memories/none.md does not exist", failed,
		},
		{
			"rename onto an existing file", `{"command":"rename","old_path":"/memories/log.md","new_path":"/memories/prefs.md"}`,
			"Error: The destination /memories/prefs.md already exists", failed,
		},
		{
			"rename a missing file", `{"command":"rename","old_path":"/memories/none.md","new_path":"/memories/x.md"}`,
			"Error: The path /memories/none.md does not exist", failed,
		},
		{
			"rename", `{"command":"rename","old_path":"/memories/log.md","new_path":"/memories/old/log.md"}`,
			"Successfully renamed /memories/log.md to /memories/old/log.md", ok,
		},
		{
			"view the root", `{"command":"view","path":"/memories"}`,
			"Here're the files and directories up to 2 levels deep in /memories, excluding hidden items and node_modules:\n39\t/memories\n14\t/memories/old\n14\t/memories/old/log.md\n25\t/memories/prefs.md", ok,
		},
		{"delete", `{"command":"delete","path":"/memories/old"}`, "Successfully deleted /memories/old", ok},
		{"delete a missing path", `{"command":"delete","path":"/memories/old"}`, "Error: The path /memories/old does not exist", failed},
		{"a path outside /memories", `{"command":"view","path":"/memories/../etc/passwd"}`, "", failed},
		{"an unknown command", `{"command":"undo_edit","path":"/memories/prefs.md"}`, "", invalid},
		{"create without file_text", `{"command":"create","path":"/memories/x.md"}`, "", invalid},
		{"an unknown field", `{"command":"view","path":"/memories","extra":1}`, "", invalid},
	} {
		out, err := mt.Invoke(ctx, json.RawMessage(step.input))
		switch step.kind {
		case ok:
			var got string
			if err != nil || json.Unmarshal(out, &got) != nil || got != step.want {
				t.Errorf("%s: %s, %v\nwant %q", step.name, out, err, step.want)
			}
		case failed:
			if !errors.Is(err, tool.ErrToolFailed) || (step.want != "" && !strings.HasSuffix(err.Error(), ": "+step.want)) {
				t.Errorf("%s: %s, %v\nwant a failure %q", step.name, out, err, step.want)
			}
		case invalid:
			if !errors.Is(err, tool.ErrInvalidInput) {
				t.Errorf("%s: %s, %v; want ErrInvalidInput", step.name, out, err)
			}
		}
	}
}

// TestToolViewOfALongFile: a view stops near MaxViewChars at a whole line and
// says how to read on, and view_range reads on.
func TestToolViewOfALongFile(t *testing.T) {
	t.Parallel()
	mt := newTool(t, memory.Config{Limits: memory.Limits{MaxFileBytes: 64 << 10}})
	ctx := memorytest.Ctx(t, "t1", "")
	line := strings.Repeat("x", 99) + "\n"
	text, _ := json.Marshal(strings.Repeat(line, 300)) // 30,000 bytes
	if _, err := mt.Invoke(ctx, json.RawMessage(`{"command":"create","path":"/memories/long.md","file_text":`+string(text)+`}`)); err != nil {
		t.Fatal(err)
	}
	out, err := mt.Invoke(ctx, json.RawMessage(`{"command":"view","path":"/memories/long.md"}`))
	var got string
	if err != nil || json.Unmarshal(out, &got) != nil {
		t.Fatalf("view: %s, %v", out, err)
	}
	if len(got) > memory.MaxViewChars+300 || !strings.Contains(got, "with view_range [") || strings.Contains(got, "   300\t") {
		t.Errorf("view of a long file is %d bytes and ends %q", len(got), got[max(0, len(got)-200):])
	}
	out, err = mt.Invoke(ctx, json.RawMessage(`{"command":"view","path":"/memories/long.md","view_range":[290,-1]}`))
	if err != nil || !strings.Contains(string(out), `   300\t`) {
		t.Errorf("view of the end: %.200s, %v", out, err)
	}
}

// TestToolLimitAndScopeErrors: a write past a limit and a call with no
// tenant are tool failures, and the limit's text says what to do.
func TestToolLimitAndScopeErrors(t *testing.T) {
	t.Parallel()
	mt := newTool(t, memory.Config{Limits: memory.Limits{MaxFileBytes: 4}})
	ctx := memorytest.Ctx(t, "t1", "")
	_, err := mt.Invoke(ctx, json.RawMessage(`{"command":"create","path":"/memories/a.md","file_text":"12345"}`))
	if !errors.Is(err, tool.ErrToolFailed) || !strings.Contains(err.Error(), "memory is full") {
		t.Errorf("create past the limit: %v", err)
	}
	if _, err := mt.Invoke(t.Context(), json.RawMessage(`{"command":"view","path":"/memories"}`)); !errors.Is(err, memory.ErrNoTenant) {
		t.Errorf("view with no tenant: %v, want ErrNoTenant", err)
	}
}

func TestNewToolRefusesANilStore(t *testing.T) {
	t.Parallel()
	if _, err := memory.NewTool(nil); !errors.Is(err, tool.ErrInvalidDefinition) {
		t.Errorf("NewTool(nil) = %v, want ErrInvalidDefinition", err)
	}
}
