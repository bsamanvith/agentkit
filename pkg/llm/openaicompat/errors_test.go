package openaicompat_test

import (
	"errors"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
)

// TestErrorMapping checks that the errors of OpenAI and of the servers that
// imitate it, before and inside a stream, map to the llm sentinels, with the
// SDK's error still reachable where there is one.
func TestErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		want    error
		apiErr  bool // an *openai.Error is reachable
	}{
		{"conformance/error_rate_limited", llm.ErrRateLimited, true},
		{"conformance/error_overloaded", llm.ErrOverloaded, true},
		{"conformance/error_invalid_request", llm.ErrInvalidRequest, true},
		{"conformance/error_auth", llm.ErrAuth, true},
		{"conformance/error_context_too_long", llm.ErrContextTooLong, true},
		{"conformance/error_mid_stream", llm.ErrOverloaded, false},
		{"errors/insufficient_quota", llm.ErrAuth, true},
		{"errors/plain_429", llm.ErrRateLimited, true},
		{"errors/forbidden", llm.ErrInvalidRequest, true},
		{"errors/ollama_not_found", llm.ErrInvalidRequest, true},
		{"errors/vllm_context", llm.ErrContextTooLong, true},
		{"errors/vllm_not_found", llm.ErrInvalidRequest, true},
		{"errors/string_error", llm.ErrInvalidRequest, false},
		{"errors/bad_gateway", llm.ErrOverloaded, true},
		{"errors/mid_stream_rate_limit", llm.ErrRateLimited, false},
		{"errors/mid_stream_vllm", llm.ErrContextTooLong, false},
		{"errors/no_finish", llm.ErrOverloaded, false},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			t.Parallel()
			_, srv := newReplay(t, "testdata/"+c.fixture+".http")
			_, err := llm.Collect(newModel(t, srv).Generate(t.Context(), userText("Hello.")))
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			for _, other := range []error{llm.ErrRateLimited, llm.ErrOverloaded, llm.ErrInvalidRequest, llm.ErrAuth, llm.ErrContextTooLong} {
				if !errors.Is(c.want, other) && errors.Is(err, other) {
					t.Errorf("err also matches %v", other)
				}
			}
			var apiErr *openai.Error
			if errors.As(err, &apiErr) != c.apiErr {
				t.Errorf("errors.As(*openai.Error) = %v, want %v", !c.apiErr, c.apiErr)
			}
		})
	}
}

// TestKeyOverPlainHTTPIsInvalid checks that a request the SDK refuses to
// send (an API key over HTTP to a host that is not loopback) is not
// retryable: the same request fails the same way every time.
func TestKeyOverPlainHTTPIsInvalid(t *testing.T) {
	t.Parallel()
	m, err := openaicompat.New(
		openaicompat.WithModel("m"),
		openaicompat.WithBaseURL("http://vllm.invalid:8000/v1"),
		openaicompat.WithClientOptions(option.WithAPIKey("test-key")),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = llm.Collect(m.Generate(t.Context(), userText("Hello.")))
	if !errors.Is(err, llm.ErrInvalidRequest) || llm.IsRetryable(err) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}

// TestConnectionRefusedIsOverloaded checks that a server that cannot be
// reached is a transient failure.
func TestConnectionRefusedIsOverloaded(t *testing.T) {
	t.Parallel()
	_, srv := newReplay(t)
	m := newModel(t, srv)
	srv.Close()
	if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); !errors.Is(err, llm.ErrOverloaded) {
		t.Errorf("err = %v, want ErrOverloaded", err)
	}
}

// TestRetriesStayOn checks that the SDK's default retries are on: a 503
// followed by a reply succeeds.
func TestRetriesStayOn(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, "testdata/retry.http", "testdata/conformance/text.http")
	m, err := openaicompat.New(
		openaicompat.WithModel("gpt-replay"),
		openaicompat.WithBaseURL(srv.URL+"/v1"),
		openaicompat.WithClientOptions(option.WithAPIKey("test-key"), option.WithUnsafeAllowHTTP()),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Collect(m.Generate(t.Context(), userText("Hello.")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Message.Text() == "" || len(rp.sent()) != 2 {
		t.Errorf("got %q after %d requests, want the reply after 2", resp.Message.Text(), len(rp.sent()))
	}
}
