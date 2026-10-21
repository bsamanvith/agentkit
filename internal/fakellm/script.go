package fakellm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ErrScript is wrapped by every error about a script that cannot be loaded.
var ErrScript = errors.New("fakellm: invalid script")

// maxLine bounds one script line (a fixture is read from its own file).
const maxLine = 1 << 20

// scriptName is what a script may be called: the X-Fakellm-Script header
// names it, so it never reaches the file system as a path.
var scriptName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// stopReasons are the stop reasons a response may name.
var stopReasons = []string{"end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal"}

// Script is a parsed script. It is not changed after parsing, so a server
// may answer from it concurrently.
type Script struct {
	name     string
	entries  []*entry // with a match, in file order
	fallback *entry   // nil: none
}

// Name returns the script's name.
func (s *Script) Name() string { return s.name }

// entry is one line of a script.
type entry struct {
	line    int
	match   string
	reply   *reply // or fixture
	fixture []byte
	status  int // the fixture's status
}

// reply is a scripted response.
type reply struct {
	text       string
	calls      []toolCall
	stopReason string
}

type toolCall struct {
	id    string // empty: derived from the request
	name  string
	input json.RawMessage // a compact JSON object
}

// The script's wire format.
type entryJSON struct {
	Match    *string    `json:"match"`
	Response *replyJSON `json:"response"`
	Fixture  string     `json:"fixture"`
}

type replyJSON struct {
	Text       string         `json:"text"`
	ToolCalls  []toolCallJSON `json:"tool_calls"`
	StopReason string         `json:"stop_reason"`
}

type toolCallJSON struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ParseScript parses a script from r. Fixtures named by its entries are read
// from fsys (nil: a script with a fixture is an error). Errors wrap
// [ErrScript] and name the line.
func ParseScript(name string, r io.Reader, fsys fs.FS) (*Script, error) {
	if !scriptName.MatchString(name) {
		return nil, fmt.Errorf("%w: name %q: want letters, digits, '_', '-' or '.'", ErrScript, name)
	}
	s := &Script{name: name}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		e, err := parseEntry(line, fsys)
		if err != nil {
			return nil, fmt.Errorf("%w: %s line %d: %w", ErrScript, name, n, err)
		}
		e.line = n
		switch {
		case e.match != "":
			s.entries = append(s.entries, e)
		case s.fallback != nil:
			return nil, fmt.Errorf("%w: %s line %d: a second fallback (no match); the first is line %d", ErrScript, name, n, s.fallback.line)
		default:
			s.fallback = e
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrScript, name, err)
	}
	if len(s.entries) == 0 && s.fallback == nil {
		return nil, fmt.Errorf("%w: %s has no entries", ErrScript, name)
	}
	return s, nil
}

func parseEntry(line []byte, fsys fs.FS) (*entry, error) {
	var ej entryJSON
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ej); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("more than one JSON value on the line")
	}
	e := &entry{}
	if ej.Match != nil {
		if *ej.Match == "" {
			return nil, errors.New(`"match" is empty; leave it out for the fallback`)
		}
		e.match = *ej.Match
	}
	switch {
	case (ej.Response == nil) == (ej.Fixture == ""):
		return nil, errors.New(`want exactly one of "response" and "fixture"`)
	case ej.Fixture != "":
		raw, status, err := readFixture(fsys, ej.Fixture)
		if err != nil {
			return nil, err
		}
		e.fixture, e.status = raw, status
	default:
		rp, err := parseReply(ej.Response)
		if err != nil {
			return nil, err
		}
		e.reply = rp
	}
	return e, nil
}

func parseReply(rj *replyJSON) (*reply, error) {
	if rj.Text == "" && len(rj.ToolCalls) == 0 {
		return nil, errors.New(`a response needs "text", "tool_calls" or both`)
	}
	rp := &reply{text: rj.Text, stopReason: rj.StopReason}
	switch {
	case rp.stopReason == "":
		rp.stopReason = "end_turn"
		if len(rj.ToolCalls) > 0 {
			rp.stopReason = "tool_use"
		}
	case !slices.Contains(stopReasons, rp.stopReason):
		return nil, fmt.Errorf("stop_reason %q: want one of %s", rp.stopReason, strings.Join(stopReasons, ", "))
	case (rp.stopReason == "tool_use") != (len(rj.ToolCalls) > 0):
		return nil, errors.New(`stop_reason "tool_use" goes with tool calls, and only with them`)
	}
	ids := map[string]bool{}
	for i, c := range rj.ToolCalls {
		if c.Name == "" {
			return nil, fmt.Errorf("tool call %d has no name", i)
		}
		if c.ID != "" {
			if ids[c.ID] {
				return nil, fmt.Errorf("tool call %d: id %q used twice", i, c.ID)
			}
			ids[c.ID] = true
		}
		in := []byte("{}")
		if len(c.Input) > 0 {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(c.Input, &obj); err != nil || obj == nil {
				return nil, fmt.Errorf("tool call %d (%s): input must be a JSON object", i, c.Name)
			}
			var buf bytes.Buffer
			if err := json.Compact(&buf, c.Input); err != nil {
				return nil, err
			}
			in = buf.Bytes()
		}
		rp.calls = append(rp.calls, toolCall{id: c.ID, name: c.Name, input: in})
	}
	return rp, nil
}

// readFixture reads a fixture and checks it is a whole recorded response,
// body included, so that serving it cannot fail halfway. It returns the
// fixture's status too.
func readFixture(fsys fs.FS, name string) ([]byte, int, error) {
	if fsys == nil {
		return nil, 0, fmt.Errorf("fixture %q: no directory to read fixtures from", name)
	}
	name = path.Clean(name)
	if !fs.ValidPath(name) {
		return nil, 0, fmt.Errorf("fixture %q: want a relative path inside the script directory", name)
	}
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, 0, fmt.Errorf("fixture: %w", err)
	}
	resp, err := parseFixture(raw, nil)
	if err == nil {
		_, err = io.Copy(io.Discard, resp.Body)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("fixture %q: %w", name, err)
	}
	return raw, resp.StatusCode, nil
}

// LoadDir loads every name.jsonl in dir as the script name. Fixtures are
// read from dir and cannot reach outside it, through .. or a symbolic link.
func LoadDir(dir string) (map[string]*Script, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("fakellm: script directory: %w", err)
	}
	defer root.Close()
	fsys := root.FS()
	files, err := fs.Glob(fsys, "*.jsonl")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no *.jsonl in %s", ErrScript, dir)
	}
	scripts := make(map[string]*Script, len(files))
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("fakellm: %w", err)
		}
		s, err := ParseScript(strings.TrimSuffix(f, ".jsonl"), bytes.NewReader(data), fsys)
		if err != nil {
			return nil, err
		}
		scripts[s.name] = s
	}
	return scripts, nil
}

// answer returns the entry that answers a request whose last user message
// has the given text, or nil.
func (s *Script) answer(text string) *entry {
	for _, e := range s.entries {
		if strings.Contains(text, e.match) {
			return e
		}
	}
	return s.fallback
}
