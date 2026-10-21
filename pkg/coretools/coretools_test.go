package coretools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/coretools"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// build builds one toolset from a JSON spec, as serve's config does.
func build(t *testing.T, spec string) tool.Registry {
	t.Helper()
	reg, err := tryBuild(spec)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func tryBuild(spec string) (tool.Registry, error) {
	catalog, err := toolset.NewCatalog(coretools.Math(), coretools.Files(), coretools.User())
	if err != nil {
		return nil, err
	}
	var s toolset.Spec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		return nil, err
	}
	regs, _, err := toolset.Build(context.Background(), catalog, []toolset.Spec{s})
	if err != nil {
		return nil, err
	}
	return regs[s.Name], nil
}

// call invokes a tool and decodes its output into a generic map.
func call(t *testing.T, reg tool.Registry, name, input string) (map[string]any, error) {
	t.Helper()
	out, err := reg.Invoke(context.Background(), name, json.RawMessage(input))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%s output %s: %v", name, out, err)
	}
	return m, nil
}

func mustCall(t *testing.T, reg tool.Registry, name, input string) map[string]any {
	t.Helper()
	m, err := call(t, reg, name, input)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, input, err)
	}
	return m
}

func wantErr(t *testing.T, reg tool.Registry, name, input, contains string) {
	t.Helper()
	_, err := call(t, reg, name, input)
	if err == nil {
		t.Fatalf("%s(%s): no error, want one containing %q", name, input, contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("%s(%s): error %q, want it to contain %q", name, input, err, contains)
	}
}

// Every tool is classified, so policies (T083) see what it does.
func TestToolsAreClassified(t *testing.T) {
	for _, spec := range []string{
		`{"name": "math", "type": "math"}`,
		`{"name": "files", "type": "files", "settings": {"root": "` + t.TempDir() + `"}}`,
	} {
		defs, err := build(t, spec).List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range defs {
			if !d.Classified() || d.Display.Present == "" {
				t.Errorf("%s: kind %q risk %q display %+v: want classified with a display", d.Name, d.Kind, d.Risk, d.Display)
			}
			if d.Kind != tool.KindRead && d.Name != "edit_file" {
				t.Errorf("%s is %s, want read", d.Name, d.Kind)
			}
		}
	}
}

func TestSettingsAreStrict(t *testing.T) {
	for spec, want := range map[string]string{
		`{"name": "m", "type": "math", "settings": {"precision": 3}}`:                        "unknown field",
		`{"name": "f", "type": "files"}`:                                                     "root is required",
		`{"name": "f", "type": "files", "settings": {"root": "/does/not/exist/here"}}`:       "open root",
		`{"name": "f", "type": "files", "settings": {"root": "/tmp", "max_file_bytes": -1}}`: "max_file_bytes",
	} {
		_, err := tryBuild(spec)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want one containing %q", spec, err, want)
		}
		if err != nil && !errors.Is(err, toolset.ErrInvalidSettings) && want != "open root" {
			t.Errorf("%s: %v does not match ErrInvalidSettings", spec, err)
		}
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func jsonMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
