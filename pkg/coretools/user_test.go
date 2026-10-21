package coretools_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/coretools"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ask_user never answers itself: it interrupts with the question and its
// choices, and refuses a question it could not show.
func TestAskUserInterrupts(t *testing.T) {
	t.Parallel()
	reg := build(t, `{"name":"user","type":"user"}`)
	defs, err := reg.List(context.Background())
	if err != nil || len(defs) != 1 || defs[0].Name != coretools.AskUserName || !defs[0].Interrupts || !defs[0].Classified() {
		t.Fatalf("definitions %+v, %v", defs, err)
	}
	for _, c := range []struct {
		in      string
		prompt  string
		choices []string
	}{
		{`{"question":" Which color? "}`, "Which color?", nil},
		{`{"question":"Which color?","choices":["red","blue"]}`, "Which color?", []string{"red", "blue"}},
	} {
		_, err := reg.Invoke(context.Background(), coretools.AskUserName, json.RawMessage(c.in))
		ie, ok := errors.AsType[*tool.InterruptError](err)
		if !ok || ie.Prompt != c.prompt || !slices.Equal(ie.Choices, c.choices) {
			t.Errorf("%s: %v, want an interrupt asking %q %q", c.in, err, c.prompt, c.choices)
		}
	}
	for _, in := range []string{
		`{"question":"  "}`,
		`{"question":"pick","choices":["one"]}`,
		`{"question":"pick","choices":["a","a"]}`,
		`{"question":"pick","choices":["a",""]}`,
		`{}`,
	} {
		_, err := reg.Invoke(context.Background(), coretools.AskUserName, json.RawMessage(in))
		if _, ok := errors.AsType[*tool.InterruptError](err); ok || err == nil {
			t.Errorf("%s: %v, want a plain failure", in, err)
		}
	}
	if _, err := tryBuild(`{"name":"user","type":"user","settings":{"x":1}}`); err == nil {
		t.Error("settings were accepted")
	}
}
