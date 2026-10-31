package knowledge_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/knowledge"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

const crops = `# Crops of the Deccan

Intro.

## Pulses

Pulses fix nitrogen.

### Pigeon pea

Sow in June.

` + "```" + `
# not a heading
` + "```" + `

### Chickpea

Sow in October.

## Millets

Hardy.
`

func TestOutline(t *testing.T) {
	t.Parallel()
	doc := knowledge.Document{Source: "crops.md", Title: "Crops of the Deccan", Text: crops}
	secs := knowledge.Outline(doc)
	var paths []string
	for _, s := range secs {
		paths = append(paths, s.Path)
	}
	want := "Crops of the Deccan|Crops of the Deccan > Pulses|Crops of the Deccan > Pulses > Pigeon pea|" +
		"Crops of the Deccan > Pulses > Chickpea|Crops of the Deccan > Millets"
	if strings.Join(paths, "|") != want {
		t.Fatalf("paths %q", paths)
	}
	text := func(i int) string { return crops[secs[i].Start:secs[i].End] }
	// A section runs to the next heading of its level or above, with its
	// subsections; the code fence's "#" line is not a heading.
	if !strings.HasPrefix(text(1), "## Pulses") || !strings.Contains(text(1), "Sow in October.") || strings.Contains(text(1), "Millets") {
		t.Errorf("Pulses section %q", text(1))
	}
	if !strings.Contains(text(2), "# not a heading") || strings.Contains(text(2), "Chickpea") {
		t.Errorf("Pigeon pea section %q", text(2))
	}
	if secs[0].Start != 0 || secs[0].End != len(crops) || secs[4].End != len(crops) || secs[2].Level != 3 || secs[2].Title != "Pigeon pea" {
		t.Errorf("sections %+v", secs)
	}
	if got := knowledge.Outline(knowledge.Document{Text: "no headings here\n"}); len(got) != 0 {
		t.Errorf("sections of a text without headings: %+v", got)
	}
}

func TestOutlineAndSectionTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, text := range map[string]string{"crops.md": crops, "soils.md": "# Soils\n\n## Black soil\n\nCracks when dry.\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := mustBuild(t, `{"root": `+quote(dir)+`}`)
	all, err := invoke[knowledge.OutlineOutput](t, reg, knowledge.OutlineTool, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Documents) != 2 || len(all.Documents[0].Sections) != 5 || all.Documents[1].Sections[1].Path != "Soils > Black soil" {
		t.Fatalf("outline %+v", all)
	}
	shallow, err := invoke[knowledge.OutlineOutput](t, reg, knowledge.OutlineTool, `{"source": "crops.md", "depth": 2}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow.Documents) != 1 || len(shallow.Documents[0].Sections) != 3 || shallow.Documents[0].Sections[1].Bytes == 0 {
		t.Errorf("depth 2 outline %+v", shallow)
	}
	sec, err := invoke[knowledge.ReadOutput](t, reg, knowledge.ReadTool, `{"source": "crops.md", "section": "Crops of the Deccan > Pulses > Chickpea"}`)
	if err != nil {
		t.Fatal(err)
	}
	if sec.Text != "### Chickpea\n\nSow in October.\n\n" || sec.NextOffset != 0 || sec.Section == "" {
		t.Errorf("section read %+v", sec)
	}
	for key, in := range map[string]string{
		knowledge.OutlineTool + " missing": `{"source": "missing.md"}`,
		knowledge.OutlineTool + " depth":   `{"depth": 9}`,
		knowledge.ReadTool + " section":    `{"source": "crops.md", "section": "Fruits"}`,
		knowledge.ReadTool + " outside":    `{"source": "crops.md", "section": "Crops of the Deccan > Millets", "offset": 3}`,
	} {
		name := strings.Fields(key)[0]
		if _, err := invoke[knowledge.OutlineOutput](t, reg, name, in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s %s: %v, want tool.ErrInvalidInput", name, in, err)
		}
	}
}

func TestSectionReadsInParts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	line := strings.Repeat("y", 99) + "\n"
	long := "# A\n\n## Big\n\n" + strings.Repeat(line, 300) + "## After\n\nnot in Big\n"
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := mustBuild(t, `{"root": `+quote(dir)+`}`)
	var got strings.Builder
	offset := 0
	for {
		out, err := invoke[knowledge.ReadOutput](t, reg, knowledge.ReadTool, `{"source": "a.md", "section": "A > Big", "offset": `+itoa(offset)+`}`)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(out.Text)
		if out.NextOffset == 0 {
			break
		}
		offset = out.NextOffset
	}
	if want := "## Big\n\n" + strings.Repeat(line, 300); got.String() != want {
		t.Errorf("read %d bytes of the section, want %d", got.Len(), len(want))
	}
}
