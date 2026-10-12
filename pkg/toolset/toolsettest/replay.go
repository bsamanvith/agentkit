package toolsettest

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Replay returns the real connector served from its cassettes: real request
// and response code, no network. Tests that want the connector's real
// behavior without a script use it (a fake model calling the real tools).
// The suite's after-every-test checks apply. The tools are built with
// Connector.Settings; a call's own Settings do not apply.
func Replay(tb testing.TB, c Connector) tool.Registry {
	tb.Helper()
	s := newSuite(tb, c, false)
	r := s.newRig(tb, modeKeyed)
	reg, err := tool.NewRegistry(r.mustBuild())
	if err != nil {
		tb.Fatalf("toolsettest: Replay: %v", err)
	}
	return reg
}

// FuzzResponses fuzzes the bodies of a tool's 200 responses. Whatever the
// body, the call must return output valid against the tool's output schema
// or an error classed tool.ErrToolFailed or tool.ErrInvalidInput, and never
// panic. The seed corpus is the bodies of the cassettes of the tool's first
// call. Each connector adds one target per tool:
//
//	func FuzzSearchResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "tavily_search") }
func FuzzResponses(f *testing.F, c Connector, toolName string) {
	f.Helper()
	s := newSuite(f, c, false)
	var call *Call
	for i := range s.c.Calls {
		if s.c.Calls[i].Tool == toolName {
			call = &s.c.Calls[i]
			break
		}
	}
	if call == nil {
		f.Fatalf("toolsettest: FuzzResponses: no call of %s in Connector.Calls", toolName)
	}
	for _, name := range call.Cassettes {
		body, err := cassetteBody(filepath.Join(s.c.Cassettes, name+resSuffix))
		if err != nil {
			f.Fatalf("toolsettest: FuzzResponses: %v", err)
		}
		f.Add(body)
	}
	for _, seed := range []string{``, `{}`, `null`, `[]`, `{"results": null}`, `{"results": [{}]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r := s.newRig(t, modeKeyed)
		tools := r.toolsFor(*call)
		for range 8 {
			r.up.Script(Response{Header: jsonHeader(), Body: body})
		}
		out, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
		switch {
		case errors.Is(err, tool.ErrPanic):
			t.Fatalf("%s panicked on a 200 with body %q: %v", call.Tool, body, err)
		case err != nil:
			if !errors.Is(err, tool.ErrToolFailed) && !errors.Is(err, tool.ErrInvalidInput) {
				t.Errorf("%s on a 200 with body %q = %v, want tool.ErrToolFailed or tool.ErrInvalidInput", call.Tool, body, err)
			}
		default:
			if verr := validate(find(tools, call.Tool).Definition().OutputSchema, out); verr != nil {
				t.Errorf("%s on a 200 with body %q returned output its schema rejects: %v\n%s", call.Tool, body, verr, out)
			}
		}
	})
}

// cassetteBody returns the body of a .res.http file.
func cassetteBody(path string) ([]byte, error) {
	_, body, err := cassetteResponse(path)
	return body, err
}

// cassetteResponse returns the status and body of a .res.http file.
func cassetteResponse(path string) (int, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}
