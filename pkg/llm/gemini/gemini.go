package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/internal/llmhttp"
)

// Provider is the provider name of the [llm.Opaque] blocks this adapter makes.
const Provider = "gemini"

// DefaultModel is the model used when [WithModel] names none.
const DefaultModel = "gemini-3.5-flash"

// DefaultBaseURL is Google's Gemini API, the server used unless
// [WithBaseURL] names another.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// DefaultIdleTimeout is how long a reply's stream may go without a chunk
// before it is given up as stalled, unless [WithIdleTimeout] says otherwise.
// It is long because a model thinks before its first chunk, for minutes at a
// high thinking level.
const DefaultIdleTimeout = 10 * time.Minute

// DefaultMaxRetries is how many times a request is retried after a
// connection failure or a 408, 409, 429 or 5xx response, unless
// [WithMaxRetries] says otherwise.
const DefaultMaxRetries = 2

// modelID is what a model ID may hold: it goes into the URL's path.
var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Model is an llm.Model backed by the Gemini API. It is safe for concurrent
// use.
type Model struct {
	hc         *http.Client
	endpoint   string // {base URL}/models/{model}:streamGenerateContent?alt=sse
	apiKey     string
	maxTokens  int
	defaults   Options
	idle       time.Duration
	maxRetries int
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
	apiKey     *string
	httpClient *http.Client
	maxRetries int
}

// WithModel sets the model ID ("gemini-3.5-flash"); empty means
// [DefaultModel].
func WithModel(id string) Option { return func(c *config) { c.model = id } }

// WithMaxTokens sets the output limit (maxOutputTokens) for requests that do
// not set one. The default, zero, sends no limit and leaves it to the model.
func WithMaxTokens(n int) Option { return func(c *config) { c.maxTokens = n } }

// WithOptions sets the model's default [Options]. A request's own Options are
// merged over them.
func WithOptions(o Options) Option { return func(c *config) { c.defaults = o } }

// WithIdleTimeout sets how long a reply's stream may go without a chunk before
// Generate gives it up with a retryable llm.ErrOverloaded; the default is
// [DefaultIdleTimeout], and zero turns the limit off.
func WithIdleTimeout(d time.Duration) Option { return func(c *config) { c.idle = d } }

// WithBaseURL sends requests to url, an http or https URL ending before
// /models, instead of [DefaultBaseURL].
func WithBaseURL(url string) Option { return func(c *config) { c.baseURL = url } }

// WithAPIKey sets the API key instead of GEMINI_API_KEY or GOOGLE_API_KEY. An
// empty key sends none, and the API refuses the request ([llm.ErrAuth]).
func WithAPIKey(key string) Option { return func(c *config) { c.apiKey = &key } }

// WithMaxRetries sets how many times a request is retried after a connection
// failure or a 408, 409, 429 or 5xx response before the stream starts
// (default [DefaultMaxRetries]; zero turns retries off).
func WithMaxRetries(n int) Option { return func(c *config) { c.maxRetries = n } }

// WithHTTPClient sets the HTTP client used for requests (default
// http.DefaultClient). Its Timeout, if set, bounds a whole reply.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// New returns a model on the Gemini API. It reads GEMINI_API_KEY, else
// GOOGLE_API_KEY; options override them. New fails with
// llm.ErrInvalidRequest for a model ID that is not a plain name, a base URL
// that is not an http or https URL, a negative output limit, idle timeout
// or retry count, or invalid default options.
func New(opts ...Option) (*Model, error) {
	c := config{idle: DefaultIdleTimeout, maxRetries: DefaultMaxRetries}
	for _, o := range opts {
		o(&c)
	}
	if c.model == "" {
		c.model = DefaultModel
	}
	if !modelID.MatchString(c.model) {
		return nil, fmt.Errorf("%w: model ID %q: want a name such as %q", llm.ErrInvalidRequest, c.model, DefaultModel)
	}
	if c.maxTokens < 0 {
		return nil, fmt.Errorf("%w: max tokens %d is negative", llm.ErrInvalidRequest, c.maxTokens)
	}
	if c.idle < 0 {
		return nil, fmt.Errorf("%w: idle timeout %v is negative", llm.ErrInvalidRequest, c.idle)
	}
	if c.maxRetries < 0 {
		return nil, fmt.Errorf("%w: max retries %d is negative", llm.ErrInvalidRequest, c.maxRetries)
	}
	if err := c.defaults.validate(); err != nil {
		return nil, err
	}
	base := c.baseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("%w: base URL %q: want http(s)://host[/path], without credentials or a query", llm.ErrInvalidRequest, base)
	}
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		key = os.Getenv("GOOGLE_API_KEY")
	}
	if c.apiKey != nil {
		key = *c.apiKey
	}
	hc := c.httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	endpoint := u.JoinPath("models", c.model+":streamGenerateContent")
	endpoint.RawQuery = "alt=sse"
	return &Model{
		hc:         hc,
		endpoint:   endpoint.String(),
		apiKey:     key,
		maxTokens:  c.maxTokens,
		defaults:   c.defaults,
		idle:       c.idle,
		maxRetries: c.maxRetries,
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
		body, err := params.encode()
		if err != nil {
			yield(llm.Event{}, fmt.Errorf("%w: encoding the request: %w", llm.ErrInvalidRequest, err))
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
		resp, err := llmhttp.Send(ctx, m.maxRetries, func(ctx context.Context) (*http.Response, error) {
			return m.post(ctx, body)
		}, func(resp *http.Response, data []byte) error { return responseError(resp, data) })
		if err != nil {
			yield(llm.Event{}, classify(ctx, err))
			return
		}
		defer resp.Body.Close()

		var r reader
		evs := llmhttp.NewEvents(resp.Body)
		for {
			data, err := evs.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			pauseIdle()
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				yield(llm.Event{}, classify(ctx, err))
				return
			}
			c, err := parseChunk(data)
			if err != nil {
				yield(llm.Event{}, classify(ctx, err))
				return
			}
			events, err := r.read(c)
			if err != nil {
				yield(llm.Event{}, classify(ctx, err))
				return
			}
			for _, ev := range events {
				if !yield(ev, nil) {
					return
				}
			}
			resumeIdle()
		}
		if err := ctx.Err(); err != nil {
			yield(llm.Event{}, classify(ctx, err))
			return
		}
		events, final, err := r.finish()
		if err != nil {
			yield(llm.Event{}, classify(ctx, err))
			return
		}
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
		yield(llm.Event{Kind: llm.EventMessageDone, Response: &final}, nil)
	}
}

// errStalled is the cause of a stream given up after the idle timeout.
var errStalled = errors.New("gemini: stream stalled")

// post sends one attempt.
func (m *Model) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if m.apiKey != "" {
		req.Header.Set("X-Goog-Api-Key", m.apiKey)
	}
	return m.hc.Do(req)
}
