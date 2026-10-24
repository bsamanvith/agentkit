package memory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ToolName is the name of the memory tool: Claude's memory tool's name.
const ToolName = "memory"

// MaxViewChars is how much of a file one view shows (in characters) before
// it points to view_range, as Claude's memory tool does.
const MaxViewChars = 16_000

// ToolInput is the memory tool's input: Claude's memory tool's, one command
// and the fields it takes.
type ToolInput struct {
	Command    string  `json:"command" jsonschema:"One of view, grep, create, str_replace, insert, delete, rename."`
	Path       string  `json:"path,omitempty" jsonschema:"The file or directory, under /memories (all commands but rename; grep: /memories when left out)."`
	Pattern    string  `json:"pattern,omitempty" jsonschema:"grep: a regular expression (RE2 syntax), matched against each line."`
	ViewRange  []int   `json:"view_range,omitempty" jsonschema:"view: [start_line, end_line] of a file, 1-based; end_line -1 reads to the end."`
	FileText   *string `json:"file_text,omitempty" jsonschema:"create: the file's content."`
	OldStr     *string `json:"old_str,omitempty" jsonschema:"str_replace: the text to replace; it must occur exactly once."`
	NewStr     *string `json:"new_str,omitempty" jsonschema:"str_replace: the replacement (empty or left out deletes old_str)."`
	InsertLine *int    `json:"insert_line,omitempty" jsonschema:"insert: the line to insert after; 0 inserts at the start."`
	InsertText *string `json:"insert_text,omitempty" jsonschema:"insert: the text to insert."`
	OldPath    string  `json:"old_path,omitempty" jsonschema:"rename: the file or directory to move."`
	NewPath    string  `json:"new_path,omitempty" jsonschema:"rename: where to move it."`
}

const toolDescription = `Your memory: a directory of files at /memories that persists across conversations. ` +
	`Check it at the start of a task, and record what you learn that will matter later (the user's preferences, facts about the work, progress on a long task). ` +
	`Keep it organized: update or remove files that are out of date rather than adding new ones.
Commands (the "command" field):
- view: show a directory listing (two levels deep) or a file with line numbers; "view_range" [start, end] shows part of a file (-1 for the end).
- grep: the lines matching "pattern", a regular expression, in the files under "path" (all of /memories when left out), as path:line: text.
- create: write "file_text" to "path", creating or overwriting the file.
- str_replace: replace "old_str", which must occur exactly once in the file, with "new_str".
- insert: insert "insert_text" after line "insert_line" (0 for the start of the file).
- delete: delete a file, or a directory and everything in it.
- rename: move "old_path" to "new_path", which must not exist.
Every path is under /memories. Files hold plain readable text only, and the memory has size limits, so keep notes short. Never store secrets such as passwords or API keys.`

// NewTool returns the memory tool over s: one tool, [ToolName], with
// Claude's memory tool's input ([ToolInput]) and its result texts, so a model
// trained on that tool uses it as is, and a grep command. Its output is a
// JSON string. A failed command is a tool error (tool.ErrToolFailed) whose
// message is the memory tool's error text; a bad input (an unknown command,
// a missing field) is tool.ErrInvalidInput.
//
// One more command, "load", is the agent's: the description the model reads
// leaves it out. It returns the memory as a new message starts with it
// ([WithLoadBytes]): every file's text while the memory is small, the file
// list past that, and "" when there is nothing to show. A prepare hook calls
// it through the tool, so that an agent whose tools are remote loads its
// memory the same way (T129).
func NewTool(s Store, opts ...ToolOption) (*tool.FuncTool[ToolInput, string], error) {
	if s == nil {
		return nil, fmt.Errorf("%w: memory: nil store", tool.ErrInvalidDefinition)
	}
	c := toolConfig{loadBytes: DefaultLoadBytes}
	for _, o := range opts {
		o(&c)
	}
	return tool.Func(ToolName, toolDescription, func(ctx context.Context, in ToolInput) (string, error) {
		out, err := run(ctx, s, c, in)
		if errors.Is(err, errBadInput) {
			return "", &tool.Error{Tool: ToolName, Kind: tool.ErrInvalidInput, Err: err}
		}
		return out, err
	}, tool.WithMetadata(tool.Metadata{
		Kind: tool.KindWrite, Risk: tool.RiskLow, Tags: []string{"memory"},
		Display: tool.Display{Title: "Memory", Icon: "page", Present: "Using memory", Past: "Used memory"},
	}))
}

var errBadInput = errors.New("invalid memory command")

// toolText is an error whose message is the memory tool's text for it,
// shown to the model as is.
type toolText string

func (t toolText) Error() string { return string(t) }

func fail(format string, args ...any) error { return toolText(fmt.Sprintf(format, args...)) }

func badInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadInput, fmt.Sprintf(format, args...))
}

// need returns the field or a bad-input error naming it.
func need[T any](v *T, command, field string) (T, error) {
	if v == nil {
		var zero T
		return zero, badInput("%s needs %q", command, field)
	}
	return *v, nil
}

func run(ctx context.Context, s Store, c toolConfig, in ToolInput) (string, error) {
	switch in.Command {
	case "load":
		return load(ctx, s, c.loadBytes)
	case "grep":
		if in.Pattern == "" {
			return "", badInput("grep needs %q", "pattern")
		}
		path := in.Path
		if path == "" {
			path = Root
		}
		return grep(ctx, s, path, in.Pattern)
	}
	if in.Command != "rename" && in.Path == "" {
		return "", badInput("%s needs %q", in.Command, "path")
	}
	switch in.Command {
	case "view":
		v, err := s.View(ctx, in.Path)
		if err != nil {
			return "", toolError(ctx, err, in.Path, "The path %s does not exist. Please provide a valid path.")
		}
		if v.Dir {
			if in.ViewRange != nil {
				return "", badInput("view_range is for a file, and %s is a directory", v.Path)
			}
			return listing(v), nil
		}
		return showFile(v, in.ViewRange)
	case "create":
		text, err := need(in.FileText, in.Command, "file_text")
		if err != nil {
			return "", err
		}
		if err := s.Create(ctx, in.Path, text); err != nil {
			return "", toolError(ctx, err, in.Path, "")
		}
		return "File created successfully at: " + display(in.Path), nil
	case "str_replace":
		old, err := need(in.OldStr, in.Command, "old_str")
		if err != nil {
			return "", err
		}
		var new string
		if in.NewStr != nil {
			new = *in.NewStr
		}
		if err := s.StrReplace(ctx, in.Path, old, new); err != nil {
			var m *MatchError
			switch {
			case errors.Is(err, ErrNoMatch):
				return "", fail("No replacement was performed, old_str `%s` did not appear verbatim in %s.", old, display(in.Path))
			case errors.As(err, &m):
				return "", fail("No replacement was performed. Multiple occurrences of old_str `%s` in lines: %s. Please ensure it is unique", old, joinInts(m.Lines))
			}
			return "", toolError(ctx, err, in.Path, "Error: The path %s does not exist. Please provide a valid path.")
		}
		return edited(ctx, s, in.Path, new), nil
	case "insert":
		line, err := need(in.InsertLine, in.Command, "insert_line")
		if err != nil {
			return "", err
		}
		text, err := need(in.InsertText, in.Command, "insert_text")
		if err != nil {
			return "", err
		}
		if err := s.Insert(ctx, in.Path, line, text); err != nil {
			var l *LineError
			if errors.As(err, &l) {
				return "", fail("Error: Invalid `insert_line` parameter: %d. It should be within the range of lines of the file: [0, %d]", l.Line, l.Lines)
			}
			return "", toolError(ctx, err, in.Path, "")
		}
		return "The file " + display(in.Path) + " has been edited.", nil
	case "delete":
		if err := s.Delete(ctx, in.Path); err != nil {
			return "", toolError(ctx, err, in.Path, "")
		}
		return "Successfully deleted " + display(in.Path), nil
	case "rename":
		if in.OldPath == "" || in.NewPath == "" {
			return "", badInput("rename needs %q and %q", "old_path", "new_path")
		}
		if err := s.Rename(ctx, in.OldPath, in.NewPath); err != nil {
			if errors.Is(err, ErrExists) {
				return "", fail("Error: The destination %s already exists", display(in.NewPath))
			}
			return "", toolError(ctx, err, in.OldPath, "")
		}
		return "Successfully renamed " + display(in.OldPath) + " to " + display(in.NewPath), nil
	case "":
		return "", badInput("no command")
	default:
		return "", badInput("unknown command %q: want view, grep, create, str_replace, insert, delete or rename", in.Command)
	}
}

// display is a path as the model gave it, cleaned when it can be.
func display(p string) string {
	if c, err := CleanPath(p); err == nil {
		return c
	}
	return p
}

// toolError is the memory tool's text for a store error. notFound, if not
// empty, is the command's own text for a missing path (with %s for it).
// Context errors pass through unchanged.
func toolError(ctx context.Context, err error, path, notFound string) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	p := display(path)
	switch {
	case errors.Is(err, ErrNotFound):
		if notFound != "" {
			return fail(notFound, p)
		}
		return fail("Error: The path %s does not exist", p)
	case errors.Is(err, ErrInvalidPath):
		return fail("Error: %v. Every path must be under %s, with no \"..\" and no names starting with \".\"", err, Root)
	case errors.Is(err, ErrIsDir):
		return fail("Error: The path %s is a directory", p)
	case errors.Is(err, ErrNotDir):
		return fail("Error: %v", err)
	case errors.Is(err, ErrLimit):
		return fail("Error: The memory is full: %v. Shorten or delete files that are no longer needed", err)
	case errors.Is(err, ErrNotText):
		return fail("Error: %v. Memory files hold plain readable text only", err)
	case errors.Is(err, ErrInvalid):
		return fail("Error: %v", err)
	}
	return err
}

// listing is a directory view as Claude's memory tool shows it.
func listing(v View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Here're the files and directories up to 2 levels deep in %s, excluding hidden items and node_modules:\n", v.Path)
	fmt.Fprintf(&b, "%s\t%s", humanSize(v.Size), v.Path)
	for _, e := range v.Entries {
		fmt.Fprintf(&b, "\n%s\t%s", humanSize(e.Size), e.Path)
	}
	return b.String()
}

// humanSize is a size as du -h prints one: bytes, then K and M with one
// decimal.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10)
	case n < 1<<20:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	}
}

// fileLines splits a file into its lines, without their newlines. An empty
// file is one empty line, as editors show it.
func fileLines(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// numbered shows lines from first (1-based) with line numbers.
func numbered(lines []string, first int) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d\t%s", first+i, l)
	}
	return b.String()
}

// showFile is a file view, whole or of view_range, cut at MaxViewChars.
func showFile(v View, rng []int) (string, error) {
	lines := fileLines(v.Text)
	first, last := 1, len(lines)
	if rng != nil {
		if len(rng) != 2 {
			return "", badInput("view_range must be [start_line, end_line]")
		}
		first, last = rng[0], rng[1]
		if last == -1 {
			last = len(lines)
		}
		if first < 1 || first > len(lines) || last < first || last > len(lines) {
			return "", badInput("view_range %v is outside the file's lines [1, %d]", rng, len(lines))
		}
	}
	header := "Here's the content of " + v.Path + " with line numbers:\n"
	body := numbered(lines[first-1:last], first)
	if len(body) <= MaxViewChars {
		return header + body, nil
	}
	// Cut at the last whole line that fits, and say where to go on.
	shown := 0
	for n := 0; first-1+shown < last; shown++ {
		line := len(fmt.Sprintf("%6d\t%s", first+shown, lines[first-1+shown])) + 1
		if n+line > MaxViewChars {
			break
		}
		n += line
	}
	shown = max(shown, 1)
	next := first + shown
	return header + numbered(lines[first-1:next-1], first) +
		fmt.Sprintf("\n[The view stops here: the file is longer than %d characters. View lines %d onward with view_range [%d, -1].]", MaxViewChars, next, next), nil
}

// edited is str_replace's success text with a snippet around the new text.
func edited(ctx context.Context, s Store, path, new string) string {
	const msg = "The memory file has been edited."
	v, err := s.View(ctx, path)
	if err != nil || v.Dir {
		return msg
	}
	lines := fileLines(v.Text)
	at := 0
	if i := strings.Index(v.Text, new); new != "" && i >= 0 {
		at = strings.Count(v.Text[:i], "\n")
	}
	from := max(0, at-4)
	to := min(len(lines), at+strings.Count(new, "\n")+5)
	return msg + " Here is a snippet of " + v.Path + " with line numbers:\n" + numbered(lines[from:to], from+1)
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}
