package toolsettest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

var (
	typePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,30}$`)
	verbPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
)

// definitions: the type and tool names follow the rules, every tool has a
// description, an object input schema and an output schema, every tool has a
// happy call, and Build made no request.
func (s *suite) definitions(t *testing.T) {
	t.Parallel()
	r := s.newRig(t, modeKeyed)
	f := s.c.New(r.env)
	typ := f.Type()
	if !typePattern.MatchString(typ) {
		t.Errorf("[definitions] type %q must match %s", typ, typePattern)
	}
	tools := r.mustBuild()
	if n := len(r.up.Requests()); n != 0 {
		t.Errorf("[definitions] Build sent %d request(s); Build must make no request", n)
	}
	if len(tools) == 0 {
		t.Fatal("[definitions] Build returned no tools")
	}
	if _, err := tool.NewRegistry(tools); err != nil {
		t.Errorf("[definitions] the tools do not form a registry: %v", err)
	}
	covered := map[string]bool{}
	for _, c := range s.c.Calls {
		covered[c.Tool] = true
	}
	for _, tl := range tools {
		d := tl.Definition()
		verb, ok := strings.CutPrefix(d.Name, typ+"_")
		if !ok || !verbPattern.MatchString(verb) {
			t.Errorf("[definitions] tool %q: name must be %s_<verb>, the verb matching %s", d.Name, typ, verbPattern)
		}
		if strings.TrimSpace(d.Description) == "" {
			t.Errorf("[definitions] tool %q has no description", d.Name)
		}
		if _, err := schemaOf(d.InputSchema); err != nil {
			t.Errorf("[definitions] tool %q: input schema: %v", d.Name, err)
		}
		if _, err := schemaOf(d.OutputSchema); err != nil {
			t.Errorf("[definitions] tool %q: output schema: %v (every connector tool declares one)", d.Name, err)
		}
		if !covered[d.Name] {
			t.Errorf("[definitions] tool %q has no happy call in Connector.Calls", d.Name)
		}
	}
	for _, c := range slices.Concat(s.c.Calls, s.c.Invalid) {
		if find(tools, c.Tool) == nil {
			t.Errorf("[definitions] call %s names tool %q, which Build did not return (have %v)", c.Name, c.Tool, toolNames(tools))
		}
	}
}

// settings: strict settings, no literal key, the missing key named, and a
// base_url that is https unless loopback and that routes requests.
func (s *suite) settings(t *testing.T) {
	t.Run("unknown_field", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		_, err := r.build(withSetting(s.c.Settings, "toolsettest_unknown_field", true))
		if !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("Build with an unknown settings field = %v, want an error matching toolset.ErrInvalidSettings", err)
		}
	})
	t.Run("literal_api_key", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		_, err := r.build(withSetting(s.c.Settings, "api_key", s.canary))
		if !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("Build with a literal api_key = %v, want an error matching toolset.ErrInvalidSettings (keys come from api_key_env)", err)
		}
	})
	if s.c.KeyEnv != "" && !s.c.KeyOptional {
		t.Run("missing_key", func(t *testing.T) {
			t.Parallel()
			r := s.newRig(t, modeNoKey)
			_, err := r.build(s.c.Settings)
			// The one check of message text: naming the variable is the
			// behavior asked for.
			if err == nil || !strings.Contains(err.Error(), s.c.KeyEnv) {
				t.Errorf("Build without %s = %v, want an error naming %s", s.c.KeyEnv, err, s.c.KeyEnv)
			}
		})
	}
	t.Run("plain_http_base_url", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		_, err := r.build(withSetting(s.c.Settings, "base_url", "http://api.toolsettest.invalid"))
		if !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("Build with base_url over plain http to a non-loopback host = %v, want toolset.ErrInvalidSettings", err)
		}
		for _, ok := range []string{"https://api.toolsettest.invalid", "http://127.0.0.1:9", "http://localhost:9", "http://[::1]:9"} {
			if _, err := r.build(withSetting(s.c.Settings, "base_url", ok)); err != nil {
				t.Errorf("Build with base_url %s = %v, want success", ok, err)
			}
		}
		if n := len(r.up.Requests()); n != 0 {
			t.Errorf("Build sent %d request(s); Build must make no request", n)
		}
	})
	t.Run("base_url", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		r.env.BaseURL = "" // the setting alone must route requests
		call := s.c.Calls[0]
		tools, err := r.build(withSetting(s.c.Settings, "base_url", r.up.URL()))
		if err != nil {
			t.Fatalf("Build with base_url %s: %v", r.up.URL(), err)
		}
		out, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
		if err != nil {
			t.Fatalf("%s with the base_url setting pointing at the upstream: %v", call.Name, err)
		}
		if err := sameAsGolden(call, out); err != nil {
			t.Error(err)
		}
	})
}

// calls: each happy call's output is golden and schema-valid, and it
// requested exactly its cassettes.
func (s *suite) calls(t *testing.T, mode keyMode) {
	for _, call := range s.c.Calls {
		t.Run(call.Name, func(t *testing.T) {
			t.Parallel()
			r := s.newRig(t, mode)
			tools := r.mustBuild()
			out, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
			if err != nil {
				t.Fatalf("%s(%s): %v", call.Tool, call.Input, err)
			}
			if mode == modeKeyed {
				r.checkOutput(tools, call, out)
			} else if err := sameAsGolden(call, out); err != nil {
				t.Error(err)
			}
			want := sorted(call.Cassettes)
			if s.fake {
				want = nil
			}
			if got := r.cassettesRequested(); !slices.Equal(got, want) {
				t.Errorf("[cassettes] %s requested %q, want exactly %q", call.Name, got, want)
			}
		})
	}
}

// invalid: each invalid input fails with ErrInvalidInput and sends nothing.
func (s *suite) invalid(t *testing.T) {
	for _, call := range s.c.Invalid {
		t.Run(call.Name, func(t *testing.T) {
			t.Parallel()
			r := s.newRig(t, modeKeyed)
			tools := r.mustBuild()
			_, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
			if !errors.Is(err, tool.ErrInvalidInput) {
				t.Errorf("%s(%s) = %v, want tool.ErrInvalidInput", call.Tool, call.Input, err)
			}
			if n := len(r.up.Requests()); n != 0 {
				t.Errorf("%s(%s) sent %d request(s); invalid input must be refused before any request", call.Tool, call.Input, n)
			}
		})
	}
}

// providerMessage is the message the suite's 400s carry: the canary inside
// it (the connector must redact it) and long enough to need the 300-rune cap.
func (s *suite) providerMessage() string {
	return "toolsettest-provider-says: the query is not accepted near " + s.canary + " " + strings.Repeat("x", 400)
}

func jsonHeader() http.Header { return http.Header{"Content-Type": {"application/json"}} }

func (s *suite) errorResponse(status int, message string) Response {
	return Response{Status: status, Header: jsonHeader(), Body: s.c.ErrorBody(message)}
}

func (s *suite) rateLimited(retryAfter int) Response {
	r := s.errorResponse(http.StatusTooManyRequests, "rate limited for key "+s.canary)
	r.Header.Set("Retry-After", fmt.Sprint(retryAfter))
	return r
}

type result struct {
	out json.RawMessage
	err error
}

// errorMatrix runs the first call against a scripted upstream.
func (s *suite) errorMatrix(t *testing.T) {
	call := s.c.Calls[0]
	failed := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, tool.ErrToolFailed) || errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s = %v, want tool.ErrToolFailed and not tool.ErrInvalidInput", call.Name, err)
		}
	}
	requests := func(t *testing.T, r *rig, want int) {
		t.Helper()
		if n := len(r.up.Requests()); n != want {
			t.Errorf("%s sent %d request(s), want %d", call.Name, n, want)
		}
	}
	scripted := func(t *testing.T, rs ...Response) (*rig, error) {
		t.Helper()
		r := s.newRig(t, modeKeyed)
		tools := r.mustBuild()
		r.up.Script(rs...)
		_, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
		return r, err
	}

	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			r, err := scripted(t, s.errorResponse(status, s.providerMessage()))
			if !errors.Is(err, tool.ErrInvalidInput) {
				t.Fatalf("%s answered %d = %v, want tool.ErrInvalidInput", call.Name, status, err)
			}
			if !strings.Contains(err.Error(), "toolsettest-provider-says") {
				t.Errorf("the error does not carry the provider's message, so the model cannot fix its input: %v", err)
			}
			if strings.Contains(err.Error(), strings.Repeat("x", 301)) {
				t.Errorf("the provider's message is not capped at 300 characters: %v", err)
			}
			requests(t, r, 1)
		})
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			r, err := scripted(t, s.errorResponse(status, "invalid API key "+s.canary))
			failed(t, err)
			requests(t, r, 1)
		})
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			r, err := scripted(t, s.errorResponse(status, "internal error serving key "+s.canary))
			failed(t, err)
			requests(t, r, 1) // no retry on 5xx: a paid call may have run
		})
	}
	t.Run("429_retry_after", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		tools := r.mustBuild()
		r.up.Script(s.rateLimited(2), s.rateLimited(2))
		done := r.start(tools, call)
		if !r.awaitTimer(done) {
			return
		}
		requests(t, r, 1)
		r.clk.Advance(2*time.Second - time.Millisecond)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if err := r.clk.WaitForTimers(ctx, 1); err != nil {
			t.Errorf("[retry] the connector stopped waiting before Retry-After (2s) had passed")
		}
		r.clk.Advance(time.Millisecond)
		res := r.await(done)
		failed(t, res.err)
		requests(t, r, 2)
	})
	t.Run("429_then_200", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		tools := r.mustBuild()
		r.up.Script(s.rateLimited(1))
		done := r.start(tools, call)
		if !r.awaitTimer(done) {
			return
		}
		r.clk.Advance(time.Second)
		res := r.await(done)
		if res.err != nil {
			t.Fatalf("%s after a 429 then a 200 = %v, want success", call.Name, res.err)
		}
		if err := sameAsGolden(call, res.out); err != nil {
			t.Error(err)
		}
		requests(t, r, 1+len(call.Cassettes))
	})
	t.Run("malformed_json", func(t *testing.T) {
		t.Parallel()
		r, err := scripted(t, Response{Header: jsonHeader(), Body: []byte(`{"results": [{"title": "cut sh`)})
		failed(t, err)
		requests(t, r, 1)
	})
	t.Run("oversized_body", func(t *testing.T) {
		t.Parallel()
		limit := s.c.MaxBodyBytes
		body := make([]byte, limit+4096)
		copy(body, `{"results": [{"content": "`)
		for i := 26; i < len(body); i++ {
			body[i] = 'a'
		}
		r, err := scripted(t, Response{Header: jsonHeader(), Body: body})
		if err == nil {
			t.Errorf("%s with a %d-byte body (limit %d) succeeded, want an error", call.Name, len(body), limit)
		} else {
			failed(t, err)
		}
		if _, read := r.up.hermetic.stats(); read > limit+1 {
			t.Errorf("%s read %d bytes of the body, want at most limit+1 = %d: read through io.LimitReader", call.Name, read, limit+1)
		}
	})
	t.Run("hang", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		tools := r.mustBuild()
		r.up.Script(Response{Hang: true})
		const deadline = 100 * time.Millisecond
		ctx, cancel := context.WithTimeout(t.Context(), deadline)
		defer cancel()
		done := make(chan result, 1)
		go func() {
			out, err := r.invoke(ctx, tools, call.Tool, call.Input)
			done <- result{out, err}
		}()
		var res result
		select {
		case res = <-done:
		case <-time.After(deadline + 2*time.Second):
			t.Errorf("%s did not return within 2s of its context's deadline: honor ctx in every request", call.Name)
			r.up.releaseHangs()
			res = r.await(done)
		}
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Errorf("%s with an expired context = %v, want context.DeadlineExceeded", call.Name, res.err)
		}
	})
	t.Run("connection_refused", func(t *testing.T) {
		t.Parallel()
		r := s.newRig(t, modeKeyed)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		h := newHermetic(t, r.up.via, addr)
		t.Cleanup(h.close)
		r.env.BaseURL, r.env.HTTPClient = "http://"+addr, &http.Client{Transport: h}
		tools := r.mustBuild()
		_, err = r.invoke(t.Context(), tools, call.Tool, call.Input)
		failed(t, err)
	})
}

// start runs a call in the background.
func (r *rig) start(tools []tool.Tool, call Call) <-chan result {
	done := make(chan result, 1)
	go func() {
		out, err := r.invoke(context.Background(), tools, call.Tool, call.Input)
		done <- result{out, err}
	}()
	return done
}

// awaitTimer waits until the call arms a timer on the fake clock (it is
// waiting for Retry-After). It fails the test if the call returns first or no
// timer is armed within a few seconds.
func (r *rig) awaitTimer(done <-chan result) bool {
	r.t.Helper()
	armed := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { armed <- r.clk.WaitForTimers(ctx, 1) }()
	select {
	case res := <-done:
		r.t.Errorf("[retry] after a 429 with Retry-After the call returned (%v) after %d request(s) without waiting on Env.Clock: retry once, after Retry-After", res.err, len(r.up.Requests()))
		return false
	case err := <-armed:
		if err != nil {
			r.t.Errorf("[retry] no timer armed on Env.Clock after a 429 with Retry-After: wait with Env.Clock, never time.Sleep")
			r.await(done)
			return false
		}
		return true
	}
}

// await waits for a background call, bounded by the watchdog.
func (r *rig) await(done <-chan result) result {
	r.t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(watchdog + time.Second):
		r.t.Fatalf("the call did not return within %s", watchdog)
		return result{}
	}
}

// canaryCheck: the upstream echoes the key in error bodies; it must reach no
// output, error, MCP response or log line.
func (s *suite) canaryCheck(t *testing.T) {
	t.Parallel()
	r := s.newRig(t, modeKeyed)
	tools := r.mustBuild()
	call := s.c.Calls[0]
	cs := serveMCP(t, tools, r.env.Logger)
	echoes := []Response{
		s.errorResponse(http.StatusBadRequest, s.providerMessage()),
		s.errorResponse(http.StatusUnauthorized, "invalid API key "+s.canary),
		s.errorResponse(http.StatusInternalServerError, "internal error serving key "+s.canary),
	}
	for _, resp := range echoes {
		r.up.Script(resp)
		if _, err := r.invoke(t.Context(), tools, call.Tool, call.Input); err == nil {
			t.Errorf("%s answered %d succeeded, want an error", call.Name, resp.Status)
		}
		r.up.Script(resp)
		res := callMCP(t, cs, call.Tool, call.Input)
		if b, _ := json.Marshal(res); strings.Contains(string(b), s.canary) {
			t.Errorf("[canary] the API key appears in the MCP result of a %d: %s", resp.Status, b)
		}
	}
	res := callMCP(t, cs, call.Tool, call.Input)
	if b, _ := json.Marshal(res); strings.Contains(string(b), s.canary) {
		t.Errorf("[canary] the API key appears in an MCP result: %s", b)
	}
	if log := r.logs.String(); strings.Contains(log, s.canary) {
		t.Errorf("[canary] the API key appears in a log line:\n%s", lineWith(log, s.canary))
	}
}

// hermeticCheck: Env.HTTPClient refuses other hosts, and the connector's
// requests reach the upstream through it.
func (s *suite) hermeticCheck(t *testing.T) {
	t.Parallel()
	r := s.newRig(t, modeKeyed)
	if err := probe(t.Context(), r.env.HTTPClient); err != nil {
		t.Error(err)
	}
	tools := r.mustBuild()
	call := s.c.Calls[0]
	if _, err := r.invoke(t.Context(), tools, call.Tool, call.Input); err != nil {
		t.Errorf("%s: %v", call.Name, err)
	}
	sent, _ := r.up.hermetic.stats()
	if got := len(r.up.Requests()); got == 0 || sent < got {
		t.Errorf("[hermetic] %s: %d request(s) reached the upstream, %d through Env.HTTPClient: send every request with Env.HTTPClient to Env.BaseURL", call.Name, got, sent)
	}
}

// mcpCheck: served over MCP, the tools list the same definitions and return
// the same output.
func (s *suite) mcpCheck(t *testing.T) {
	t.Parallel()
	r := s.newRig(t, modeKeyed)
	tools := r.mustBuild()
	cs := serveMCP(t, tools, r.env.Logger)
	listed := listMCP(t, cs)
	for _, tl := range tools {
		d := tl.Definition()
		got, ok := listed[d.Name]
		if !ok {
			t.Errorf("[mcp] %s is not listed over MCP", d.Name)
			continue
		}
		if got.Description != d.Description || !jsonEqual(got.InputSchema, d.InputSchema) || !jsonEqual(got.OutputSchema, d.OutputSchema) {
			t.Errorf("[mcp] %s is listed over MCP as %+v, want %+v", d.Name, got, d)
		}
	}
	if len(listed) != len(tools) {
		t.Errorf("[mcp] %d tools listed over MCP, want %d", len(listed), len(tools))
	}
	call := s.c.Calls[0]
	res := callMCP(t, cs, call.Tool, call.Input)
	if res.IsError {
		t.Fatalf("[mcp] %s over MCP failed: %+v", call.Name, res)
	}
	if err := sameAsGolden(call, res.Structured); err != nil {
		t.Errorf("[mcp] structured content: %v", err)
	}
	if err := sameAsGolden(call, json.RawMessage(res.Text)); err != nil {
		t.Errorf("[mcp] text content: %v", err)
	}
	inv := s.c.Invalid[0]
	if res := callMCP(t, cs, inv.Tool, inv.Input); !res.IsError {
		t.Errorf("[mcp] invalid call %s over MCP succeeded, want an error result", inv.Name)
	}
}

// concurrency: 16 parallel happy calls on one set of tools all return the
// right output.
func (s *suite) concurrency(t *testing.T) {
	t.Parallel()
	r := s.newRig(t, modeKeyed)
	tools := r.mustBuild()
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		call := s.c.Calls[i%len(s.c.Calls)]
		wg.Go(func() {
			out, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", call.Name, err)
				return
			}
			errs[i] = sameAsGolden(call, out)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}
