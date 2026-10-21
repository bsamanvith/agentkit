package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Provider is the provider name of the [llm.Opaque] blocks this adapter makes.
const Provider = "openaicompat"

// DefaultIdleTimeout is how long a reply's stream may go without a chunk
// before it is given up as stalled, unless [WithIdleTimeout] says otherwise.
// It is long because reasoning models on Chat Completions may think for
// minutes without sending anything, and the SDK hides the keep-alive comments
// some servers send.
const DefaultIdleTimeout = 10 * time.Minute

// Model is an llm.Model backed by an OpenAI-compatible Chat Completions
// endpoint. It is safe for concurrent use.
type Model struct {
	client    openai.Client
	model     string
	maxTokens int
	defaults  Options
	idle      time.Duration
}

var _ llm.Model = (*Model)(nil)

// Option configures [New].
type Option func(*config)

type config struct {
	model      string
	maxTokens  int
	defaults   Options
	idle       time.Duration
	baseURL    string
	httpClient *http.Client
	sdk        []option.RequestOption
}

// WithModel sets the model ID, as the server names it ("gpt-5.2",
// "llama3.2", "openai/gpt-oss-120b"). It is required: there is no default
// that every server has.
func WithModel(id string) Option { return func(c *config) { c.model = id } }

// WithMaxTokens sets the output limit for requests that do not set one. The
// default, zero, sends no limit and leaves it to the server.
func WithMaxTokens(n int) Option { return func(c *config) { c.maxTokens = n } }

// WithOptions sets the model's default [Options]. A request's own Options are
// merged over them.
func WithOptions(o Options) Option { return func(c *config) { c.defaults = o } }

// WithIdleTimeout sets how long a reply's stream may go without a chunk before
// Generate gives it up with a retryable llm.ErrOverloaded; the default is
// [DefaultIdleTimeout], and zero turns the limit off.
func WithIdleTimeout(d time.Duration) Option { return func(c *config) { c.idle = d } }

// WithBaseURL sends requests to url (for example http://localhost:11434/v1)
// instead of OpenAI's API or OPENAI_BASE_URL.
func WithBaseURL(url string) Option { return func(c *config) { c.baseURL = url } }

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// WithClientOptions passes options to the SDK client, after the defaults: for
// example option.WithAPIKey, option.WithMaxRetries, option.WithHeader or
// option.WithMiddleware.
func WithClientOptions(opts ...option.RequestOption) Option {
	return func(c *config) { c.sdk = append(c.sdk, opts...) }
}

// New returns a model on an OpenAI-compatible endpoint. The SDK client reads
// OPENAI_API_KEY and OPENAI_BASE_URL; options override them. New fails with
// llm.ErrInvalidRequest without a model ID, or with a negative output limit or
// idle timeout or invalid default options.
func New(opts ...Option) (*Model, error) {
	c := config{idle: DefaultIdleTimeout}
	for _, o := range opts {
		o(&c)
	}
	if c.model == "" {
		return nil, fmt.Errorf("%w: no model ID (WithModel)", llm.ErrInvalidRequest)
	}
	if c.maxTokens < 0 {
		return nil, fmt.Errorf("%w: max tokens %d is negative", llm.ErrInvalidRequest, c.maxTokens)
	}
	if c.idle < 0 {
		return nil, fmt.Errorf("%w: idle timeout %v is negative", llm.ErrInvalidRequest, c.idle)
	}
	if err := c.defaults.validate(); err != nil {
		return nil, err
	}
	var sdk []option.RequestOption
	if c.baseURL != "" {
		sdk = append(sdk, option.WithBaseURL(c.baseURL))
	}
	if c.httpClient != nil {
		sdk = append(sdk, option.WithHTTPClient(c.httpClient))
	}
	sdk = append(sdk, c.sdk...)
	return &Model{
		client:    openai.NewClient(sdk...),
		model:     c.model,
		maxTokens: c.maxTokens,
		defaults:  c.defaults,
		idle:      c.idle,
	}, nil
}

// Generate streams the model's reply to req. See [llm.Model].
func (m *Model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(llm.Event{}, err)
			return
		}
		params, err := m.build(req)
		if err != nil {
			yield(llm.Event{}, err)
			return
		}
		// Ending the stream early, whichever way, cancels the request. So
		// does a stream that goes quiet for the idle timeout: the cause
		// tells classify it was not the caller's cancellation. The clock
		// runs only while waiting on the server.
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		pauseIdle, resumeIdle := func() {}, func() {}
		if m.idle > 0 {
			watchdog := time.AfterFunc(m.idle, func() {
				cancel(fmt.Errorf("%w: no chunk for %v", errStalled, m.idle))
			})
			defer watchdog.Stop()
			pauseIdle = func() { watchdog.Stop() }
			resumeIdle = func() { watchdog.Reset(m.idle) }
		}
		// The status of the last HTTP response, for errors whose body the
		// SDK cannot read as an OpenAI error (vLLM's, for one).
		var status atomic.Int64
		stream := m.client.Chat.Completions.NewStreaming(ctx, params,
			option.WithMiddleware(func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
				resp, err := next(r)
				if resp != nil {
					status.Store(int64(resp.StatusCode))
				}
				return resp, err
			}))
		defer stream.Close()

		var r reader
		for stream.Next() {
			pauseIdle()
			if err := ctx.Err(); err != nil {
				yield(llm.Event{}, classify(ctx, err, int(status.Load())))
				return
			}
			for _, ev := range r.read(stream.Current()) {
				if !yield(ev, nil) {
					return
				}
			}
			resumeIdle()
		}
		if err := stream.Err(); err != nil {
			yield(llm.Event{}, classify(ctx, err, int(status.Load())))
			return
		}
		if err := ctx.Err(); err != nil {
			yield(llm.Event{}, classify(ctx, err, int(status.Load())))
			return
		}
		events, resp, err := r.finish()
		if err != nil {
			yield(llm.Event{}, classify(ctx, err, int(status.Load())))
			return
		}
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
		yield(llm.Event{Kind: llm.EventMessageDone, Response: &resp}, nil)
	}
}

// errStalled is the cause of a stream given up after the idle timeout.
var errStalled = errors.New("openaicompat: stream stalled")
