package anthropic

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// DefaultModel is the Claude model used unless [WithModel] says otherwise.
const DefaultModel = "claude-haiku-5-5"

// DefaultMaxTokens limits a reply's output when the request does not
// (llm.Request.MaxTokens is zero). Thinking counts toward it.
const DefaultMaxTokens = 64000

// Model is an llm.Model backed by the Claude Messages API. It is safe for
// concurrent use.
type Model struct {
	client    anthropic.Client
	model     string
	maxTokens int
	defaults  Options
}

var _ llm.Model = (*Model)(nil)

// Option configures [New].
type Option func(*config)

type config struct {
	model      string
	maxTokens  int
	defaults   Options
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
// llm.ErrInvalidRequest on an empty model ID, a negative output limit or
// invalid default options.
func New(opts ...Option) (*Model, error) {
	c := config{model: DefaultModel, maxTokens: DefaultMaxTokens}
	for _, o := range opts {
		o(&c)
	}
	if c.model == "" {
		return nil, fmt.Errorf("%w: empty model ID", llm.ErrInvalidRequest)
	}
	if c.maxTokens <= 0 {
		return nil, fmt.Errorf("%w: max tokens %d is not positive", llm.ErrInvalidRequest, c.maxTokens)
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
		// Ending the stream early, whichever way, cancels the request.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stream := m.client.Beta.Messages.NewStreaming(ctx, params,
			option.WithRequestBody("application/json", body))
		defer stream.Close()

		var r reader
		for stream.Next() {
			if err := ctx.Err(); err != nil {
				yield(llm.Event{}, err)
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

// errIncomplete is a stream that ended before message_stop.
var errIncomplete = errors.New("anthropic: stream ended before message_stop")
