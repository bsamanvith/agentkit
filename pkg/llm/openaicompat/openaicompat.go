package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Provider is the provider name of the [llm.Opaque] blocks this adapter makes.
const Provider = "openaicompat"

// DefaultBaseURL is OpenAI's API, the server used when neither [WithBaseURL]
// nor OPENAI_BASE_URL names another.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultIdleTimeout is how long a reply's stream may go without a chunk
// before it is given up as stalled, unless [WithIdleTimeout] says otherwise.
// It is long because reasoning models on Chat Completions may think for
// minutes without sending a chunk; keep-alive comments do not count.
const DefaultIdleTimeout = 10 * time.Minute

// DefaultMaxRetries is how many times a request is retried after a
// connection failure or a 408, 409, 429 or 5xx response, unless
// [WithMaxRetries] says otherwise.
const DefaultMaxRetries = 2

const (
	// maxRetryAfter is the longest Retry-After a retry waits for; a longer
	// one returns the error instead.
	maxRetryAfter = 2 * time.Minute
	// maxBackoff caps the backoff between retries without a Retry-After.
	maxBackoff = 8 * time.Second
	// maxErrorBody is how much of an error response is read.
	maxErrorBody = 64 << 10
)

// Model is an llm.Model backed by an OpenAI-compatible Chat Completions
// endpoint. It is safe for concurrent use.
type Model struct {
	hc         *http.Client
	endpoint   string // {base URL}/chat/completions
	apiKey     string
	header     http.Header
	model      string
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
	header     http.Header
	maxRetries int
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

// WithBaseURL sends requests to url, an http or https URL ending before
// /chat/completions (for example http://localhost:11434/v1), instead of
// OPENAI_BASE_URL or [DefaultBaseURL]. Plain http is allowed, also with an
// API key: it suits a server on the same host or inside a cluster, and
// sends the key in the clear anywhere else.
func WithBaseURL(url string) Option { return func(c *config) { c.baseURL = url } }

// WithAPIKey sets the API key, sent as "Authorization: Bearer <key>",
// instead of OPENAI_API_KEY. An empty key sends no Authorization header,
// which local servers accept, even when OPENAI_API_KEY is set.
func WithAPIKey(key string) Option { return func(c *config) { c.apiKey = &key } }

// WithHeader adds a header to every request, for example OpenAI-Organization
// or a gateway's own. It cannot replace Authorization, Content-Type or
// Accept.
func WithHeader(key, value string) Option {
	return func(c *config) {
		if c.header == nil {
			c.header = http.Header{}
		}
		c.header.Add(key, value)
	}
}

// WithMaxRetries sets how many times a request is retried after a connection
// failure or a 408, 409, 429 or 5xx response before the stream starts
// (default [DefaultMaxRetries]; zero turns retries off). A Retry-After-Ms or
// Retry-After header sets the wait, up to two minutes; otherwise it backs off
// from half a second to eight.
func WithMaxRetries(n int) Option { return func(c *config) { c.maxRetries = n } }

// WithHTTPClient sets the HTTP client used for requests (default
// http.DefaultClient). Its Timeout, if set, bounds a whole reply.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// New returns a model on an OpenAI-compatible endpoint. It reads
// OPENAI_API_KEY and OPENAI_BASE_URL; options override them. New fails with
// llm.ErrInvalidRequest without a model ID, with a base URL that is not an
// http or https URL, or with a negative output limit, idle timeout or retry
// count or invalid default options.
func New(opts ...Option) (*Model, error) {
	c := config{idle: DefaultIdleTimeout, maxRetries: DefaultMaxRetries}
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
	if c.maxRetries < 0 {
		return nil, fmt.Errorf("%w: max retries %d is negative", llm.ErrInvalidRequest, c.maxRetries)
	}
	if err := c.defaults.validate(); err != nil {
		return nil, err
	}
	base := c.baseURL
	if base == "" {
		base = os.Getenv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: base URL %q: want http(s)://host[/path], without credentials", llm.ErrInvalidRequest, base)
	}
	key := os.Getenv("OPENAI_API_KEY")
	if c.apiKey != nil {
		key = *c.apiKey
	}
	hc := c.httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Model{
		hc:         hc,
		endpoint:   u.JoinPath("chat/completions").String(),
		apiKey:     key,
		header:     c.header,
		model:      c.model,
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
		resp, err := m.send(ctx, body)
		if err != nil {
			yield(llm.Event{}, classify(ctx, err))
			return
		}
		defer resp.Body.Close()

		var r reader
		evs := newEvents(resp.Body)
		for {
			data, err := evs.next()
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
			if bytes.HasPrefix(data, []byte("[DONE]")) {
				break
			}
			c, err := parseChunk(data)
			if err != nil {
				yield(llm.Event{}, classify(ctx, err))
				return
			}
			for _, ev := range r.read(c) {
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
var errStalled = errors.New("openaicompat: stream stalled")

// send posts body and returns the response once it is a 2xx, retrying
// connection failures and retryable statuses. Any other response is
// returned as an *Error; a connection failure as the client's error.
func (m *Model) send(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := m.post(ctx, body)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		var wait time.Duration
		retry := attempt < m.maxRetries && ctx.Err() == nil
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			_ = resp.Body.Close()
			err = responseError(resp, data)
			wait, retry = retryAfter(resp, attempt, retry)
		} else {
			wait = backoff(attempt)
		}
		if !retry {
			return nil, err
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, err
		case <-t.C:
		}
	}
}

// post sends one attempt.
func (m *Model) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range m.header {
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	return m.hc.Do(req)
}

// retryAfter says whether a failed response is retried and after how long.
// A retry is allowed when the attempts are not used up (retry); it happens
// for 408, 409, 429 and 5xx, or as an X-Should-Retry header says, after the
// wait the server asks for (Retry-After-Ms, then Retry-After, in seconds or
// as a date), unless that is longer than maxRetryAfter.
func retryAfter(resp *http.Response, attempt int, retry bool) (time.Duration, bool) {
	if !retry {
		return 0, false
	}
	switch resp.Header.Get("X-Should-Retry") {
	case "true":
	case "false":
		return 0, false
	default:
		s := resp.StatusCode
		if s != http.StatusRequestTimeout && s != http.StatusConflict && s != http.StatusTooManyRequests && s < 500 {
			return 0, false
		}
	}
	for _, h := range []struct {
		name string
		unit time.Duration
	}{{"Retry-After-Ms", time.Millisecond}, {"Retry-After", time.Second}} {
		v := resp.Header.Get(h.name)
		if v == "" {
			continue
		}
		var d time.Duration
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			if math.IsNaN(n) || n < 0 {
				continue
			}
			if n > float64(maxRetryAfter/h.unit) {
				return 0, false
			}
			d = time.Duration(n * float64(h.unit))
		} else if t, err := http.ParseTime(v); err == nil && h.unit == time.Second {
			d = max(time.Until(t), 0)
			if d > maxRetryAfter {
				return 0, false
			}
		} else {
			continue
		}
		return d, true
	}
	return backoff(attempt), true
}

// backoff is the wait before retry attempt+1 when the server names none:
// half a second doubling up to maxBackoff, less up to a quarter at random.
func backoff(attempt int) time.Duration {
	d := maxBackoff
	if attempt < 5 {
		d = min(time.Duration(float64(500*time.Millisecond)*math.Pow(2, float64(attempt))), maxBackoff)
	}
	return d - rand.N(d/4+1)
}
