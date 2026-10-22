// Package embed has the clients of embedding APIs that a memory.Index uses
// for semantic search: [NewOpenAI] for the OpenAI embeddings endpoint and
// every server that copies it (local model servers, gateways), and
// [NewVoyage] for Voyage AI. Both implement memory.Embedder.
//
// They follow the HTTP rules of connectors (docs/CONNECTORS.md §2): the
// injected client and clock, texts sent in batches (BatchSize texts and
// MaxBatchBytes bytes at most per request), one retry of a 429 after its
// Retry-After (at most 10 s, 1 s when absent) and none of anything else, a
// response body capped at MaxBodyBytes, and the key in the Authorization
// header and nowhere else: never in an error, a log line or a URL.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/memory"
)

// The defaults of Config.
const (
	DefaultBatchSize     = 64
	DefaultMaxBatchBytes = 512 << 10
	DefaultMaxBodyBytes  = 16 << 20
	DefaultTimeout       = 30 * time.Second
	// MaxRetryWait is the longest Retry-After honored; a longer one fails
	// at once.
	MaxRetryWait = 10 * time.Second
	// DefaultRetryWait is the wait after a 429 without a usable Retry-After.
	DefaultRetryWait = time.Second
	maxMessageChars  = 300
)

// The errors of a client. A vector of another dimension than the
// configured one is memory.ErrEmbedderMismatch.
var (
	// ErrRejected is a request the provider refused (a 4xx but 401, 403
	// and 429), with its message.
	ErrRejected = errors.New("embed: the provider rejected the request")
	// ErrUnauthorized is a 401 or 403: a missing, wrong or expired key.
	ErrUnauthorized = errors.New("embed: the provider rejected the API key")
	// ErrRateLimited is a 429 that a retry did not clear.
	ErrRateLimited = errors.New("embed: rate limited by the provider")
	// ErrUnavailable is a 5xx or no response at all.
	ErrUnavailable = errors.New("embed: the provider is unavailable")
	// ErrBadResponse is a response that is not what the API documents:
	// not JSON, over MaxBodyBytes, or with the wrong vectors.
	ErrBadResponse = errors.New("embed: bad response from the provider")
)

// Config configures a client.
type Config struct {
	// Model is the provider's model name (required).
	Model string
	// Dimensions is the size of the vectors. For a model of KnownModels it
	// may be 0 (the model's default), and one other than the default is
	// sent to the provider (OpenAI's dimensions, Voyage's
	// output_dimension) for models that can shorten their vectors. For any
	// other model it is required, and not sent.
	Dimensions int
	// BaseURL is the API's base URL, ending before /embeddings; empty for
	// the provider's. It must be https unless the host is loopback.
	BaseURL string
	// Key is the API key. Voyage requires one; an OpenAI-compatible server
	// may not (a local one), and then no Authorization is sent.
	Key string
	// BatchSize caps the texts of one request (default DefaultBatchSize,
	// at most the provider's limit); MaxBatchBytes their bytes (default
	// DefaultMaxBatchBytes; a longer text goes alone).
	BatchSize     int
	MaxBatchBytes int
	// MaxBodyBytes caps a response body (default DefaultMaxBodyBytes).
	MaxBodyBytes int64
	// Timeout caps one request (default DefaultTimeout).
	Timeout time.Duration
}

// Option sets what a client gets from its environment.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (default: a new http.Client).
func WithHTTPClient(c *http.Client) Option { return func(x *Client) { x.http = c } }

// WithClock sets the clock of retry waits (default clock.Real).
func WithClock(c clock.Clock) Option { return func(x *Client) { x.clock = c } }

// WithLogger sets the logger of request lines (default: discard).
func WithLogger(l *slog.Logger) Option { return func(x *Client) { x.log = l } }

// Client is an embedding API client: a memory.Embedder. It is safe for
// concurrent use.
type Client struct {
	p     provider
	cfg   Config
	dim   int
	send  bool // send cfg.Dimensions to the provider
	http  *http.Client
	clock clock.Clock
	log   *slog.Logger
}

var _ memory.Embedder = (*Client)(nil)

// provider is what differs between the APIs.
type provider struct {
	name       string
	baseURL    string
	maxBatch   int
	needsKey   bool
	known      map[string]int // model -> default dimension
	dimField   string         // the request field of a shortened dimension
	inputType  bool           // send input_type (query or document)
	errMessage func(body []byte) string
}

// KnownModels are the models whose default dimension a client knows, by
// provider ("openai", "voyage").
var KnownModels = map[string]map[string]int{
	"openai": {"text-embedding-3-small": 1536, "text-embedding-3-large": 3072, "text-embedding-ada-002": 1536},
	"voyage": {
		"voyage-3.5": 1024, "voyage-3.5-lite": 1024, "voyage-3-large": 1024, "voyage-code-3": 1024,
		"voyage-finance-2": 1024, "voyage-law-2": 1024, "voyage-3": 1024, "voyage-3-lite": 512,
	},
}

var (
	openAI = provider{
		name: "openai", baseURL: "https://api.openai.com/v1", maxBatch: 2048,
		known: KnownModels["openai"], dimField: "dimensions", errMessage: openAIMessage,
	}
	voyage = provider{
		name: "voyage", baseURL: "https://api.voyageai.com/v1", maxBatch: 1000, needsKey: true,
		known: KnownModels["voyage"], dimField: "output_dimension", inputType: true, errMessage: voyageMessage,
	}
)

// NewOpenAI returns a client of the OpenAI embeddings API
// (POST {BaseURL}/embeddings), which OpenAI-compatible servers copy. It makes
// no request; a bad cfg is memory.ErrInvalid.
func NewOpenAI(cfg Config, opts ...Option) (*Client, error) { return newClient(openAI, cfg, opts) }

// NewVoyage returns a client of the Voyage AI embeddings API, which embeds
// queries and documents differently (input_type). It makes no request; a
// bad cfg is memory.ErrInvalid.
func NewVoyage(cfg Config, opts ...Option) (*Client, error) { return newClient(voyage, cfg, opts) }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: embed: "+format, append([]any{memory.ErrInvalid}, args...)...)
}

func newClient(p provider, cfg Config, opts []Option) (*Client, error) {
	c := &Client{p: p, cfg: cfg, clock: clock.Real(), log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	switch {
	case strings.TrimSpace(cfg.Model) == "":
		return nil, invalid("%s: no model", p.name)
	case cfg.Dimensions < 0 || cfg.Dimensions > memory.MaxEmbedderDim:
		return nil, invalid("%s: dimensions %d: want 1 to %d", p.name, cfg.Dimensions, memory.MaxEmbedderDim)
	case p.needsKey && cfg.Key == "":
		return nil, invalid("%s: no API key", p.name)
	case cfg.BatchSize < 0 || cfg.BatchSize > p.maxBatch:
		return nil, invalid("%s: batch size %d: want 1 to %d", p.name, cfg.BatchSize, p.maxBatch)
	case cfg.MaxBatchBytes < 0 || cfg.MaxBodyBytes < 0 || cfg.Timeout < 0:
		return nil, invalid("%s: a negative limit", p.name)
	case c.clock == nil || c.log == nil:
		return nil, invalid("%s: nil clock or logger", p.name)
	}
	def, known := p.known[cfg.Model]
	switch {
	case cfg.Dimensions == 0 && !known:
		return nil, invalid("%s: model %q: give its dimensions (known models: %v)", p.name, cfg.Model, knownNames(p))
	case cfg.Dimensions == 0:
		c.dim = def
	default:
		c.dim = cfg.Dimensions
		c.send = known && cfg.Dimensions != def
	}
	if c.cfg.BaseURL == "" {
		c.cfg.BaseURL = p.baseURL
	}
	if err := checkBaseURL(c.cfg.BaseURL); err != nil {
		return nil, invalid("%s: base URL: %v", p.name, err)
	}
	c.cfg.BaseURL = strings.TrimRight(c.cfg.BaseURL, "/")
	if c.cfg.BatchSize == 0 {
		c.cfg.BatchSize = min(DefaultBatchSize, p.maxBatch)
	}
	if c.cfg.MaxBatchBytes == 0 {
		c.cfg.MaxBatchBytes = DefaultMaxBatchBytes
	}
	if c.cfg.MaxBodyBytes == 0 {
		c.cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.cfg.Timeout == 0 {
		c.cfg.Timeout = DefaultTimeout
	}
	return c, nil
}

func knownNames(p provider) []string {
	var out []string
	for m := range p.known {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// checkBaseURL requires https, or http to a loopback host.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("not a URL")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return errors.New("no user, query or fragment allowed")
	case u.Scheme == "https" && u.Host != "":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return errors.New("want https (http only to a loopback host)")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Model implements memory.Embedder.
func (c *Client) Model() (string, int) { return c.cfg.Model, c.dim }

// Embed implements memory.Embedder: the texts in batches, one request each,
// in order.
func (c *Client) Embed(ctx context.Context, texts []string, purpose memory.Purpose) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); {
		end, size := start, 0
		for end < len(texts) && end-start < c.cfg.BatchSize && (end == start || size+len(texts[end]) <= c.cfg.MaxBatchBytes) {
			size += len(texts[end])
			end++
		}
		vs, err := c.batch(ctx, texts[start:end], purpose)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
		start = end
	}
	return out, nil
}

type request struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	InputType string   `json:"input_type,omitempty"`
	// Encoding is OpenAI's encoding_format.
	Encoding string `json:"encoding_format,omitempty"`
}

type response struct {
	Data []struct {
		Index     *int      `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// batch embeds one request's texts.
func (c *Client) batch(ctx context.Context, texts []string, purpose memory.Purpose) ([][]float32, error) {
	req := request{Model: c.cfg.Model, Input: texts}
	if c.p.inputType {
		req.InputType = string(purpose)
	} else {
		req.Encoding = "float"
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("embed: encode request: %w", err)
	}
	if c.send {
		// The dimension field, added without a second struct per provider.
		payload = append(payload[:len(payload)-1], []byte(`,"`+c.p.dimField+`":`+strconv.Itoa(c.dim)+`}`)...)
	}
	status, header, body, err := c.attempt(ctx, payload, len(texts))
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		wait, ok := retryAfter(ctx, header.Get("Retry-After"), c.clock.Now())
		if !ok {
			return nil, ErrRateLimited
		}
		c.log.DebugContext(ctx, "embedding request rate limited, retrying", "provider", c.p.name, "wait", wait)
		if err := sleep(ctx, c.clock, wait); err != nil {
			return nil, err
		}
		if status, _, body, err = c.attempt(ctx, payload, len(texts)); err != nil {
			return nil, err
		}
	}
	if err := c.statusError(status, body); err != nil {
		return nil, err
	}
	return c.decode(body, len(texts))
}

// attempt sends one request. It returns an error only when no complete
// response came.
func (c *Client) attempt(ctx context.Context, payload []byte, n int) (int, http.Header, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.cfg.BaseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: build request", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Key)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, c.transportError(ctx, actx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1))
	switch {
	case err != nil:
		return 0, nil, nil, c.transportError(ctx, actx, err)
	case int64(len(body)) > c.cfg.MaxBodyBytes:
		return 0, nil, nil, fmt.Errorf("%w: %s's response is over %d bytes", ErrBadResponse, c.p.name, c.cfg.MaxBodyBytes)
	}
	c.log.DebugContext(ctx, "embedding request", "provider", c.p.name, "model", c.cfg.Model, "texts", n,
		"status", resp.StatusCode, "bytes", len(body), "duration", time.Since(start))
	return resp.StatusCode, resp.Header, body, nil
}

// transportError reports a request that got no complete response, without
// its cause (the client's text, which may name a proxy or a URL); the cause
// is logged, redacted. The caller's own cancellation is returned as such.
func (c *Client) transportError(ctx, actx context.Context, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.log.WarnContext(ctx, "embedding request failed", "provider", c.p.name, "err", c.redact(cause.Error()))
	if actx.Err() != nil {
		return fmt.Errorf("%w: %s did not answer within %v", ErrUnavailable, c.p.name, c.cfg.Timeout)
	}
	return fmt.Errorf("%w: could not reach %s", ErrUnavailable, c.p.name)
}

func (c *Client) statusError(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (%s, status %d)", ErrUnauthorized, c.p.name, status)
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status >= 400 && status < 500:
		msg := truncate(c.redact(c.p.errMessage(body)), maxMessageChars)
		if msg == "" {
			msg = "no details"
		}
		return fmt.Errorf("%w (%s, status %d): %s", ErrRejected, c.p.name, status, msg)
	default:
		return fmt.Errorf("%w: %s returned status %d", ErrUnavailable, c.p.name, status)
	}
}

// decode returns the n vectors of body, in input order.
func (c *Client) decode(body []byte, n int) ([][]float32, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: %s's response is not the documented JSON", ErrBadResponse, c.p.name)
	}
	if len(r.Data) != n {
		return nil, fmt.Errorf("%w: %s returned %d vectors for %d texts", ErrBadResponse, c.p.name, len(r.Data), n)
	}
	out := make([][]float32, n)
	for i, d := range r.Data {
		at := i
		if d.Index != nil {
			at = *d.Index
		}
		switch {
		case at < 0 || at >= n || out[at] != nil:
			return nil, fmt.Errorf("%w: %s returned a vector with index %d", ErrBadResponse, c.p.name, at)
		case len(d.Embedding) != c.dim:
			return nil, fmt.Errorf("%w: %s returned %d dimensions; %s is configured with %d",
				memory.ErrEmbedderMismatch, c.p.name, len(d.Embedding), c.cfg.Model, c.dim)
		}
		out[at] = d.Embedding
	}
	return out, nil
}

func (c *Client) redact(s string) string {
	if c.cfg.Key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.cfg.Key, "[redacted]")
}

// openAIMessage is the message of {"error": {"message": ...}}.
func openAIMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Message
}

// voyageMessage is the message of {"detail": ...}.
func voyageMessage(body []byte) string {
	var e struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(e.Detail, &s) == nil {
		return s
	}
	return string(e.Detail)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// retryAfter is how long a 429 asks to wait (seconds or an HTTP date), and
// whether to wait at all: not past MaxRetryWait or ctx's deadline.
func retryAfter(ctx context.Context, v string, now time.Time) (time.Duration, bool) {
	d := DefaultRetryWait
	v = strings.TrimSpace(v)
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs > int64(MaxRetryWait/time.Second) {
			return 0, false
		}
		if secs >= 0 {
			d = time.Duration(secs) * time.Second
		}
	} else if t, err := http.ParseTime(v); err == nil {
		d = max(t.Sub(now), 0)
	}
	if d > MaxRetryWait {
		return 0, false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return 0, false
	}
	return d, true
}

// sleep waits d on clk, or until ctx is done.
func sleep(ctx context.Context, clk clock.Clock, d time.Duration) error {
	t := clk.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
