package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/knowledge"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// build builds one "knowledge" toolset with settings, as serve's config
// does.
func build(t *testing.T, settings string, opts ...knowledge.Option) (tool.Registry, error) {
	t.Helper()
	catalog, err := toolset.NewCatalog(knowledge.Factory(opts...))
	if err != nil {
		t.Fatal(err)
	}
	spec := toolset.Spec{Name: "handbook", Type: knowledge.Type, Settings: json.RawMessage(settings)}
	regs, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{spec})
	if err != nil {
		return nil, err
	}
	return regs["handbook"], nil
}

func mustBuild(t *testing.T, settings string) tool.Registry {
	t.Helper()
	reg, err := build(t, settings)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func invoke[Out any](t *testing.T, reg tool.Registry, name, input string) (Out, error) {
	t.Helper()
	var out Out
	b, err := reg.Invoke(t.Context(), name, json.RawMessage(input))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s output %s: %v", name, b, err)
	}
	return out, nil
}

func TestToolsetTools(t *testing.T) {
	t.Parallel()
	reg := mustBuild(t, `{"root": "testdata/handbook", "description": "Northwind Bikes' support handbook."}`)
	defs, err := reg.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if !strings.Contains(d.Description, "the knowledge base (Northwind Bikes' support handbook)") {
			t.Errorf("%s's description %q does not say what the base holds", d.Name, d.Description)
		}
		if d.Kind != tool.KindRead || d.Risk != tool.RiskLow {
			t.Errorf("%s: kind %q, risk %q; want read, low", d.Name, d.Kind, d.Risk)
		}
	}
	if strings.Join(names, ",") != "knowledge_list,knowledge_outline,knowledge_read,knowledge_search" {
		t.Errorf("tools %v", names)
	}
}

func TestSearchTool(t *testing.T) {
	t.Parallel()
	reg := mustBuild(t, `{"root": "testdata/handbook", "k": 2}`)
	out, err := invoke[knowledge.SearchOutput](t, reg, knowledge.SearchTool, `{"query": "e-bike battery warranty claim"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("%d results, want the toolset's k, 2", len(out.Results))
	}
	out, err = invoke[knowledge.SearchOutput](t, reg, knowledge.SearchTool, `{"query": "What does E07 mean?"}`)
	if err != nil {
		t.Fatal(err)
	}
	r := out.Results[0]
	if r.Source != "troubleshooting/error-codes.md" || r.Section != "E-bike display error codes > E07: torque sensor fault" ||
		r.Title != "E-bike display error codes" || !strings.Contains(r.Text, "8 seconds") || r.Score <= 0 {
		t.Errorf("first result %+v", r)
	}
	out, err = invoke[knowledge.SearchOutput](t, reg, knowledge.SearchTool, `{"query": "battery", "k": 1}`)
	if err != nil || len(out.Results) != 1 {
		t.Errorf("k 1: %d results, %v", len(out.Results), err)
	}
	out, err = invoke[knowledge.SearchOutput](t, reg, knowledge.SearchTool, `{"query": "zygomorphic"}`)
	if err != nil || out.Results == nil || len(out.Results) != 0 {
		t.Errorf("no match: %+v, %v; want an empty list", out, err)
	}
	for _, in := range []string{`{"query": ""}`, `{"query": "x", "k": 51}`, `{"k": 1}`} {
		if _, err := invoke[knowledge.SearchOutput](t, reg, knowledge.SearchTool, in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: %v, want tool.ErrInvalidInput", in, err)
		}
	}
}

func TestListTool(t *testing.T) {
	t.Parallel()
	reg := mustBuild(t, `{"root": "testdata/handbook"}`)
	out, err := invoke[knowledge.ListOutput](t, reg, knowledge.ListTool, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Documents) != 4 || out.Truncated || out.Documents[0].Source != "returns.md" ||
		out.Documents[0].Title != "Returns and refunds" || out.Documents[0].Bytes == 0 {
		t.Errorf("list %+v", out)
	}
	out, err = invoke[knowledge.ListOutput](t, reg, knowledge.ListTool, `{"prefix": "troubleshooting/"}`)
	if err != nil || len(out.Documents) != 1 || out.Documents[0].Source != "troubleshooting/error-codes.md" {
		t.Errorf("prefix: %+v, %v", out, err)
	}
}

func TestReadTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	line := strings.Repeat("x", 99) + "\n"
	long := "# Long\n" + strings.Repeat(line, 400) // 40,007 bytes: three parts
	if err := os.WriteFile(filepath.Join(dir, "long.md"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := mustBuild(t, `{"root": `+quote(dir)+`}`)
	var got strings.Builder
	offset, parts := 0, 0
	for {
		out, err := invoke[knowledge.ReadOutput](t, reg, knowledge.ReadTool, `{"source": "long.md", "offset": `+itoa(offset)+`}`)
		if err != nil {
			t.Fatal(err)
		}
		parts++
		if len(out.Text) > knowledge.ReadBytes || out.TotalBytes != len(long) || out.Title != "Long" {
			t.Fatalf("part %d: %d bytes, total %d, title %q", parts, len(out.Text), out.TotalBytes, out.Title)
		}
		if out.NextOffset != 0 && !strings.HasSuffix(out.Text, "\n") {
			t.Errorf("part %d does not end at a line break", parts)
		}
		got.WriteString(out.Text)
		if out.NextOffset == 0 {
			break
		}
		offset = out.NextOffset
	}
	if got.String() != long || parts != 3 {
		t.Errorf("read %d bytes in %d parts, want the %d bytes in 3", got.Len(), parts, len(long))
	}
	for _, in := range []string{`{"source": "missing.md"}`, `{"source": "long.md", "offset": -1}`, `{"source": "long.md", "offset": 40007}`} {
		if _, err := invoke[knowledge.ReadOutput](t, reg, knowledge.ReadTool, in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: %v, want tool.ErrInvalidInput", in, err)
		}
	}
}

func TestReadToolRefusesAnOffsetInsideACharacter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("héllo"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := mustBuild(t, `{"root": `+quote(dir)+`}`)
	if _, err := invoke[knowledge.ReadOutput](t, reg, knowledge.ReadTool, `{"source": "a.txt", "offset": 2}`); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("offset 2: %v, want tool.ErrInvalidInput", err)
	}
}

func TestToolsetSettings(t *testing.T) {
	t.Parallel()
	noEnv := knowledge.WithLookupEnv(func(string) (string, bool) { return "", false })
	for name, settings := range map[string]string{
		"no root":            `{}`,
		"unknown field":      `{"root": "testdata/handbook", "path": "x"}`,
		"k":                  `{"root": "testdata/handbook", "k": 51}`,
		"chunk_bytes":        `{"root": "testdata/handbook", "chunk_bytes": 10}`,
		"long description":   `{"root": "testdata/handbook", "description": "` + strings.Repeat("d", knowledge.MaxDescriptionBytes+1) + `"}`,
		"semantic, no embed": `{"root": "testdata/handbook", "modes": ["semantic"]}`,
		"unknown mode":       `{"root": "testdata/handbook", "modes": ["fuzzy"]}`,
		"embedder type":      `{"root": "testdata/handbook", "embedder": {"type": "cohere", "model": "x"}}`,
		"voyage without key": `{"root": "testdata/handbook", "embedder": {"type": "voyage", "model": "voyage-3.5-lite"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := build(t, settings, noEnv); !errors.Is(err, toolset.ErrInvalidSettings) {
				t.Errorf("build: %v, want ErrInvalidSettings", err)
			}
		})
	}
	t.Run("missing root", func(t *testing.T) {
		t.Parallel()
		if _, err := build(t, `{"root": "testdata/none"}`); err == nil || !strings.Contains(err.Error(), "open root") {
			t.Errorf("build: %v, want an open root error", err)
		}
	})
	t.Run("no documents", func(t *testing.T) {
		t.Parallel()
		if _, err := build(t, `{"root": `+quote(t.TempDir())+`}`); err == nil || !strings.Contains(err.Error(), "no documents") {
			t.Errorf("build: %v, want no documents", err)
		}
	})
}

// TestToolsetReadsAConfigMapVolume: a Kubernetes ConfigMap volume holds
// each file as a link into its hidden "..data" folder; the links are read.
func TestToolsetReadsAConfigMapVolume(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := filepath.Join(root, "..2026_10_24_16_04_16.1")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "returns.md"), []byte("# Returns\nWithin 30 days.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(data), filepath.Join(root, "..data")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink("..data/returns.md", filepath.Join(root, "returns.md")); err != nil {
		t.Fatal(err)
	}
	reg := mustBuild(t, `{"root": `+quote(root)+`}`)
	out, err := invoke[knowledge.ListOutput](t, reg, knowledge.ListTool, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Documents) != 1 || out.Documents[0].Source != "returns.md" || out.Documents[0].Title != "Returns" {
		t.Errorf("documents %+v, want returns.md alone", out.Documents)
	}
}

// TestToolsetStaysInItsRoot: a symlink out of the root is not followed.
func TestToolsetStaysInItsRoot(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("# Secret\nThe vault code is 4711.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.md"), []byte("# Ok\nfine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	reg := mustBuild(t, `{"root": `+quote(root)+`}`)
	out, err := invoke[knowledge.ListOutput](t, reg, knowledge.ListTool, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Documents) != 1 || out.Documents[0].Source != "ok.md" {
		t.Errorf("documents %+v, want ok.md alone", out.Documents)
	}
}

// TestAgentAnswersFromTheBase: a model searches the base and gets the
// passage that answers, as a tool result, in a harness run.
func TestAgentAnswersFromTheBase(t *testing.T) {
	t.Parallel()
	reg := mustBuild(t, `{"root": "testdata/handbook"}`)
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: knowledge.SearchTool, Input: json.RawMessage(`{"query": "restocking fee"}`)}),
		llmtest.Text("Opened e-bikes carry a 12% restocking fee (returns.md)."),
	)
	h, err := harness.New(model, reg)
	if err != nil {
		t.Fatal(err)
	}
	var results []llm.ToolResult
	for ev, err := range h.Run(context.Background(), harness.Input{Message: harness.UserText("Is there a restocking fee?")}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == harness.EventToolDone {
			results = append(results, ev.ToolResult)
		}
	}
	if len(results) != 1 || results[0].IsError || !strings.Contains(results[0].Content, "12% restocking fee") ||
		!strings.Contains(results[0].Content, `"source":"returns.md"`) {
		t.Errorf("tool results %+v, want the restocking fee passage", results)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
