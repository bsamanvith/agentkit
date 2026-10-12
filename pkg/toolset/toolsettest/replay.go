package toolsettest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
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
//
// The tools are built once per target; each input is a response the
// in-process upstream gives every request of that input's call.
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
	fz := newFuzzRig(f, s, *call)
	f.Fuzz(func(t *testing.T, body []byte) {
		out, err := fz.invoke(t, body)
		switch {
		case errors.Is(err, tool.ErrPanic):
			t.Fatalf("%s panicked on a 200 with body %q: %v", call.Tool, body, err)
		case err != nil:
			if !errors.Is(err, tool.ErrToolFailed) && !errors.Is(err, tool.ErrInvalidInput) {
				t.Errorf("%s on a 200 with body %q = %v, want tool.ErrToolFailed or tool.ErrInvalidInput", call.Tool, body, err)
			}
		default:
			if verr := validate(fz.def.OutputSchema, out); verr != nil {
				t.Errorf("%s on a 200 with body %q returned output its schema rejects: %v\n%s", call.Tool, body, verr, out)
			}
		}
	})
}

// fuzzHost is the upstream host a fuzzed connector is wired to.
const fuzzHost = "fuzz.toolsettest.invalid"

// fuzzRig is a connector built once for a fuzz target. Its client answers
// every request of the input being run with that input's body, in process;
// everything it finds wrong is reported to that input's t (never to f, which
// must not be used inside the fuzz function).
type fuzzRig struct {
	s    *suite
	call Call
	tl   tool.Tool
	def  tool.Definition
	logs *logSink

	mu  sync.Mutex
	cur *fuzzInput // the input being run, nil between inputs
}

type fuzzInput struct {
	t        testing.TB
	body     []byte
	requests []Request
}

func newFuzzRig(f *testing.F, s *suite, call Call) *fuzzRig {
	f.Helper()
	fz := &fuzzRig{s: s, call: call, logs: &logSink{}}
	env := Env{
		BaseURL:    "https://" + fuzzHost,
		HTTPClient: &http.Client{Transport: fz},
		LookupEnv: func(name string) (string, bool) {
			if s.c.KeyEnv != "" && name == s.c.KeyEnv {
				return s.canary, true
			}
			return "", false
		},
		Clock:  clocktest.NewFake(time.Time{}),
		Logger: slog.New(slog.NewTextHandler(fz.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	ctx, cancel := context.WithTimeout(context.Background(), watchdog)
	defer cancel()
	settings := s.settingsFor(call)
	tools, err := s.c.New(env).Build(ctx, settings)
	if err != nil {
		f.Fatalf("toolsettest: FuzzResponses: Build(%s): %v", settings, err)
	}
	if fz.tl = find(tools, call.Tool); fz.tl == nil {
		f.Fatalf("toolsettest: FuzzResponses: Build returned no tool %s (have %v)", call.Tool, toolNames(tools))
	}
	fz.def = fz.tl.Definition()
	return fz
}

// invoke makes the call with every request answered by body, and checks that
// the key is in no output, error or log line and in exactly one header of
// each request.
func (fz *fuzzRig) invoke(t *testing.T, body []byte) (json.RawMessage, error) {
	t.Helper()
	in := &fuzzInput{t: t, body: body}
	fz.mu.Lock()
	fz.cur = in
	fz.mu.Unlock()
	fz.logs.Reset()
	defer func() {
		fz.mu.Lock()
		fz.cur = nil
		fz.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(t.Context(), watchdog)
	defer cancel()
	out, err := fz.tl.Invoke(ctx, fz.call.Input)

	canary := fz.s.canary
	if bytes.Contains(out, []byte(canary)) {
		t.Errorf("[canary] the API key appears in the output of %s", fz.call.Tool)
	}
	if err != nil && strings.Contains(err.Error(), canary) {
		t.Errorf("[canary] the API key appears in the error of %s: %v", fz.call.Tool, err)
	}
	if log := fz.logs.String(); strings.Contains(log, canary) {
		t.Errorf("[canary] the API key appears in a log line:\n%s", lineWith(log, canary))
	}
	fz.mu.Lock()
	reqs := in.requests
	fz.mu.Unlock()
	for i, req := range reqs {
		if err := fz.s.checkRequest(modeKeyed, req); err != nil {
			t.Errorf("[key] request %d (%s %s): %v", i+1, req.Method, req.Path, err)
		}
	}
	return out, err
}

// RoundTrip answers a request of the current input with its body.
func (fz *fuzzRig) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	fz.mu.Lock()
	defer fz.mu.Unlock()
	in := fz.cur
	if in == nil {
		return nil, errors.New("toolsettest: a request outside a fuzz input")
	}
	if req.URL.Host != fuzzHost {
		in.t.Errorf("[hermetic] refused %s %s: Env.HTTPClient reaches only Env.BaseURL", req.Method, req.URL.Redacted())
		return nil, fmt.Errorf("%w: %s", errRefused, req.URL.Host)
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	in.requests = append(in.requests, Request{
		Method: req.Method, Path: req.URL.Path, Query: req.URL.Query(), Header: req.Header.Clone(), Body: reqBody,
	})
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        jsonHeader(),
		Body:          io.NopCloser(bytes.NewReader(in.body)),
		ContentLength: int64(len(in.body)),
		Request:       req,
	}, nil
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
