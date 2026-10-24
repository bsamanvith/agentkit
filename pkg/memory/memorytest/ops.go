package memorytest

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// Op is one command of a test: its kind ("view", "create", "str_replace",
// "insert", "delete", "rename") and the arguments it takes.
type Op struct {
	Kind     string
	Path     string
	To       string // rename's destination
	Text     string // create's and insert's text
	Old, New string // str_replace's
	Line     int    // insert's
}

func (o Op) String() string {
	switch o.Kind {
	case "create":
		return fmt.Sprintf("create %s %q", o.Path, o.Text)
	case "str_replace":
		return fmt.Sprintf("str_replace %s %q -> %q", o.Path, o.Old, o.New)
	case "insert":
		return fmt.Sprintf("insert %s @%d %q", o.Path, o.Line, o.Text)
	case "rename":
		return fmt.Sprintf("rename %s -> %s", o.Path, o.To)
	}
	return o.Kind + " " + o.Path
}

// Apply runs o on s.
func (o Op) Apply(ctx context.Context, s memory.Store) error {
	switch o.Kind {
	case "view":
		_, err := s.View(ctx, o.Path)
		return err
	case "create":
		return s.Create(ctx, o.Path, o.Text)
	case "str_replace":
		return s.StrReplace(ctx, o.Path, o.Old, o.New)
	case "insert":
		return s.Insert(ctx, o.Path, o.Line, o.Text)
	case "delete":
		return s.Delete(ctx, o.Path)
	case "rename":
		return s.Rename(ctx, o.Path, o.To)
	}
	panic("memorytest: unknown op " + o.Kind)
}

// Step applies o, checks the scope's invariants after it, and checks that
// the outcome follows from pre, the scope's files before it: a failure
// changed nothing and its error is justified, a success did what o says and
// nothing else. It returns the files after o, and o's error.
func Step(ctx context.Context, t testing.TB, s memory.Store, l memory.Limits, pre map[string]string, o Op) (map[string]string, error) {
	t.Helper()
	err := o.Apply(ctx, s)
	post := CheckState(ctx, t, s, l)
	if err != nil {
		if !same(pre, post) {
			t.Errorf("%v failed (%v) but changed the files:\nbefore %q\nafter  %q", o, err, pre, post)
		}
		if why := justified(o, pre, l, err); why != "" {
			t.Errorf("%v: error %v is not justified: %s (files %q)", o, err, why, pre)
		}
		return post, err
	}
	if want, why := expected(o, pre, l); why != "" {
		t.Errorf("%v succeeded, but it should have failed: %s (files %q)", o, why, pre)
	} else if !same(want, post) {
		t.Errorf("%v: files are\n%q\nwant\n%q", o, post, want)
	}
	return post, nil
}

// model helpers over a snapshot.

func fileAt(files map[string]string, p string) (string, bool) {
	t, ok := files[p]
	return t, ok
}

func dirAt(files map[string]string, p string) bool {
	if p == memory.Root {
		return true
	}
	for f := range files {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

func existsAt(files map[string]string, p string) bool {
	_, ok := files[p]
	return ok || dirAt(files, p)
}

func parentFile(files map[string]string, p string) bool {
	for _, d := range memory.Parents(p) {
		if _, ok := files[d]; ok {
			return true
		}
	}
	return false
}

func total(files map[string]string) int64 {
	var n int64
	for _, t := range files {
		n += int64(len(t))
	}
	return n
}

// overLimit says which limit putting text at p in files would pass, or "".
func overLimit(files map[string]string, l memory.Limits, p, text string) string {
	old, had := files[p]
	switch {
	case int64(len(text)) > l.MaxFileBytes:
		return "file bytes"
	case len(text) > len(old) && total(files)-int64(len(old))+int64(len(text)) > l.MaxScopeBytes:
		return "scope bytes"
	case !had && len(files)+1 > l.MaxFiles:
		return "files"
	}
	return ""
}

// insertModel inserts text after line of file, as whole lines; ok is false
// for a line outside the file. It is written apart from the backends' code.
func insertModel(file string, line int, text string) (string, bool) {
	lines := strings.SplitAfter(file, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if line < 0 || line > len(lines) {
		return "", false
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	var b strings.Builder
	for i, l := range lines[:line] {
		b.WriteString(l)
		if i == line-1 && !strings.HasSuffix(l, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(text)
	for _, l := range lines[line:] {
		b.WriteString(l)
	}
	return b.String(), true
}

// expected returns the files o makes of pre, or why o must fail.
func expected(o Op, pre map[string]string, l memory.Limits) (map[string]string, string) {
	p, err := memory.CleanPath(o.Path)
	if err != nil {
		return nil, "path " + o.Path + " is invalid"
	}
	post := maps.Clone(pre)
	if t, ok := written(o); ok && memory.CheckText(t) != nil {
		return nil, "the text is not readable text"
	}
	switch o.Kind {
	case "view":
		if !existsAt(pre, p) {
			return nil, "nothing at " + p
		}
	case "create":
		if _, ok := pre[p]; !ok && (dirAt(pre, p) || parentFile(pre, p)) {
			return nil, p + " is a directory or under a file"
		}
		if lim := overLimit(pre, l, p, o.Text); lim != "" {
			return nil, "past the " + lim + " limit"
		}
		post[p] = o.Text
	case "str_replace":
		text, ok := fileAt(pre, p)
		switch {
		case !ok:
			return nil, "no file at " + p
		case o.Old == "":
			return nil, "old is empty"
		case strings.Count(text, o.Old) != 1:
			return nil, fmt.Sprintf("old occurs %d times", strings.Count(text, o.Old))
		}
		edited := strings.Replace(text, o.Old, o.New, 1)
		if lim := overLimit(pre, l, p, edited); lim != "" {
			return nil, "past the " + lim + " limit"
		}
		post[p] = edited
	case "insert":
		text, ok := fileAt(pre, p)
		if !ok {
			return nil, "no file at " + p
		}
		edited, ok := insertModel(text, o.Line, o.Text)
		if !ok {
			return nil, "line outside the file"
		}
		if lim := overLimit(pre, l, p, edited); lim != "" {
			return nil, "past the " + lim + " limit"
		}
		post[p] = edited
	case "delete":
		if p == memory.Root || !existsAt(pre, p) {
			return nil, "root, or nothing at " + p
		}
		maps.DeleteFunc(post, func(f, _ string) bool { return memory.Within(f, p) })
	case "rename":
		to, err := memory.CleanPath(o.To)
		if err != nil {
			return nil, "path " + o.To + " is invalid"
		}
		switch {
		case p == memory.Root || !existsAt(pre, p):
			return nil, "root, or nothing at " + p
		case existsAt(pre, to):
			return nil, to + " exists"
		case memory.Within(to, p):
			return nil, "into itself"
		case parentFile(pre, to):
			return nil, to + " is under a file"
		}
		for f, text := range pre {
			if memory.Within(f, p) {
				moved := to + strings.TrimPrefix(f, p)
				if len(moved) > memory.MaxPathBytes {
					return nil, moved + " is too long"
				}
				delete(post, f)
				post[moved] = text
			}
		}
	}
	return post, ""
}

// written is the text o writes, for the commands that write one.
func written(o Op) (string, bool) {
	switch o.Kind {
	case "create", "insert":
		return o.Text, true
	case "str_replace":
		return o.New, true
	}
	return "", false
}

// justified says why err is not a reason for o to fail on pre, or "" if it
// is.
func justified(o Op, pre map[string]string, l memory.Limits, err error) string {
	if _, why := expected(o, pre, l); why == "" {
		return "the model expects it to succeed"
	}
	p, perr := memory.CleanPath(o.Path)
	to, terr := memory.CleanPath(o.To)
	switch {
	case is(err, memory.ErrInvalidPath):
		if perr != nil || (o.Kind == "rename" && terr != nil) {
			return ""
		}
		if (o.Kind == "delete" || o.Kind == "rename") && p == memory.Root {
			return ""
		}
		if o.Kind == "rename" && memory.Within(to, p) {
			return ""
		}
		for f := range pre {
			if o.Kind == "rename" && memory.Within(f, p) && len(to+strings.TrimPrefix(f, p)) > memory.MaxPathBytes {
				return ""
			}
		}
		return "the paths are valid"
	case perr != nil:
		return "an invalid path must be ErrInvalidPath"
	case is(err, memory.ErrNotText):
		if t, ok := written(o); ok && memory.CheckText(t) != nil {
			return ""
		}
		return "the text is readable"
	case is(err, memory.ErrNotFound):
		if !existsAt(pre, p) {
			return ""
		}
		return p + " exists"
	case is(err, memory.ErrExists):
		if o.Kind == "rename" && terr == nil && existsAt(pre, to) {
			return ""
		}
		return "nothing is at the destination"
	case is(err, memory.ErrIsDir):
		if _, ok := pre[p]; !ok && dirAt(pre, p) && o.Kind != "delete" && o.Kind != "rename" && o.Kind != "view" {
			return ""
		}
		return p + " is not a directory"
	case is(err, memory.ErrNotDir):
		if (o.Kind == "create" && parentFile(pre, p)) || (o.Kind == "rename" && terr == nil && parentFile(pre, to)) {
			return ""
		}
		return "no parent is a file"
	case is(err, memory.ErrNoMatch):
		if o.Kind == "str_replace" && strings.Count(pre[p], o.Old) == 0 {
			return ""
		}
		return "old occurs"
	case is(err, memory.ErrMultipleMatches):
		if o.Kind == "str_replace" && strings.Count(pre[p], o.Old) > 1 {
			return ""
		}
		return "old does not occur more than once"
	case is(err, memory.ErrInvalidLine):
		if _, ok := insertModel(pre[p], o.Line, o.Text); o.Kind == "insert" && !ok {
			return ""
		}
		return "the line is in the file"
	case is(err, memory.ErrInvalid):
		if o.Kind == "str_replace" && o.Old == "" {
			return ""
		}
		return "the arguments are valid"
	case is(err, memory.ErrLimit):
		var text string
		writes := true
		switch o.Kind {
		case "create":
			text = o.Text
		case "str_replace":
			text = strings.Replace(pre[p], o.Old, o.New, 1)
		case "insert":
			text, _ = insertModel(pre[p], o.Line, o.Text)
		default:
			writes = false
		}
		if writes && overLimit(pre, l, p, text) != "" {
			return ""
		}
		return "no limit is passed"
	}
	return "unexpected error"
}
