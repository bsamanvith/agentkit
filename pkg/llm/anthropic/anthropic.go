package anthropic

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// DefaultModel is the Claude model used unless [WithModel] says otherwise.
const DefaultModel = "claude-haiku-5-5"

// DefaultMaxTokens limits a reply's output when the request does not
// (llm.Request.MaxTokens is zero). Thinking counts toward it.
const DefaultMaxTokens = 64000

// DefaultIdleTimeout is how long a reply's stream may go without an event
// (the API's pings count) before it is given up as stalled, unless
// [WithIdleTimeout] says otherwise.
const DefaultIdleTimeout = 5 * time.Minute

// Model is an llm.Model backed by the Claude Messages API. It is safe for
// concurrent use.
type Model struct {
	client    anthropic.Client
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

// WithModel sets the Claude model ID; the default is [DefaultModel].
func WithModel(id string) Option { return func(c *config) { c.model = id } }

// WithMaxTokens sets the output limit for requests that do not set one; the
// default is [DefaultMaxTokens].
func WithMaxTokens(n int) Option { return func(c *config) { c.maxTokens = n } }

// WithOptions sets the model's default [Options]. A request's own Options
// override them field by field.
func WithOptions(o Options) Option { return func(c *config) { c.defaults = o } }

// WithIdleTimeout sets how long a reply's stream may go without an event
// before Generate gives it up with a retryable llm.ErrOverloaded; the default
// is [DefaultIdleTimeout], and zero turns the limit off. Without it, a
// connection that stays open but stops sending (a half-open TCP connection)
// holds the call until its context ends.
func WithIdleTimeout(d time.Duration) Option { return func(c *config) { c.idle = d } }

// WithBaseURL sends requests to url instead of the API (or
// ANTHROPIC_BASE_URL), for example a replay server in tests.
func WithBaseURL(url string) Option { return func(c *config) { c.baseURL = url } }

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// WithClientOptions passes options to the SDK client, after the defaults:
// for example option.WithAPIKey, option.WithMaxRetries or
// option.WithMiddleware.
func WithClientOptions(opts ...option.RequestOption) Option {
	return func(c *config) { c.sdk = append(c.sdk, opts...) }
}

// New returns a Claude model. The SDK client is built with the SDK's default
// credential chain; options can override it. New fails with
// llm.ErrInvalidRequest on an empty model ID, a negative output limit, a
// negative idle timeout or invalid default options.
func New(opts ...Option) (*Model, error) {
	c := config{model: DefaultModel, maxTokens: DefaultMaxTokens, idle: DefaultIdleTimeout}
	for _, o := range opts {
		o(&c)
	}
	if c.model == "" {
		return nil, fmt.Errorf("%w: empty model ID", llm.ErrInvalidRequest)
	}
	if c.maxTokens <= 0 {
		return nil, fmt.Errorf("%w: max tokens %d is not positive", llm.ErrInvalidRequest, c.maxTokens)
	}
	if c.idle < 0 {
		return nil, fmt.Errorf("%w: idle timeout %v is negative", llm.ErrInvalidRequest, c.idle)
	}
	defaults := c.defaults.merge(builtin)
	if err := defaults.validate(); err != nil {
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
		client:    anthropic.NewClient(sdk...),
		model:     c.model,
		maxTokens: c.maxTokens,
		defaults:  defaults,
		idle:      c.idle,
	}, nil
}

// Generate streams Claude's reply to req. See [llm.Model].
func (m *Model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(llm.Event{}, err)
			return
		}
		params, body, err := m.build(req)
		if err != nil {
			yield(llm.Event{}, err)
			return
		}
		// Ending the stream early, whichever way, cancels the request. So
		// does a stream that goes quiet for the idle timeout: the cause
		// tells classify it was not the caller's cancellation.
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		resetIdle := func() {}
		if m.idle > 0 {
			watchdog := time.AfterFunc(m.idle, func() {
				cancel(fmt.Errorf("%w: no event for %v", errStalled, m.idle))
			})
			defer watchdog.Stop()
			resetIdle = func() { watchdog.Reset(m.idle) }
		}
		stream := m.client.Beta.Messages.NewStreaming(ctx, params,
			option.WithRequestBody("application/json", body))
		defer stream.Close()

		var r reader
		for stream.Next() {
			resetIdle()
			if err := ctx.Err(); err != nil {
				yield(llm.Event{}, classify(ctx, err))
				return
			}
			events, err := r.read(stream.Current())
			if err != nil {
				yield(llm.Event{}, classify(ctx, err))
				return
			}
			for _, ev := range events {
				if !yield(ev, nil) {
					return
				}
			}
			if r.stopped {
				break
			}
		}
		if err := stream.Err(); err != nil {
			yield(llm.Event{}, classify(ctx, err))
			return
		}
		resp, err := r.response()
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			yield(llm.Event{}, classify(ctx, err))
			return
		}
		yield(llm.Event{Kind: llm.EventMessageDone, Response: &resp}, nil)
	}
}

// errStalled is the cause of a stream given up after the idle timeout.
var errStalled = errors.New("anthropic: stream stalled")

// errIncomplete is a stream that ended before message_stop.
var errIncomplete = errors.New("anthropic: stream ended before message_stop")
