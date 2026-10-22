package agenttools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the toolset type of the agentkit toolset in config.
const Type = "agentkit"

// DefaultTimeout is how long agent_run waits for a run by default.
const DefaultTimeout = 5 * time.Minute

// MaxTimeout is the longest "timeout" setting accepted.
const MaxTimeout = time.Hour

// ErrClosed is the error of agent_run after [Factory.Close].
var ErrClosed = errors.New("agenttools: closed")

// Settings are the toolset's settings in config ("settings" of its
// toolset.Spec). Unknown fields are refused.
type Settings struct {
	// URL is the agent API's base URL, such as "http://agent:8080". Empty
	// means the agent API of this process ([WithHandler]), which is then
	// required.
	URL string `json:"url,omitempty"`
	// Timeout is how long agent_run waits for a run to end, such as "2m"
	// (default [DefaultTimeout], at most [MaxTimeout]). It is cut to fit
	// the MCP server's call timeout ([WithCallTimeout]).
	Timeout string `json:"timeout,omitempty"`
}

// Option configures [New].
type Option func(*config)

type config struct {
	handler     http.Handler
	hc          *http.Client
	callTimeout time.Duration
	clock       clock.Clock
	log         *slog.Logger
}

// WithHandler sets the agent API of this process (its handler, which serves
// /v1/): a toolset with no "url" sends its requests there, in process.
func WithHandler(h http.Handler) Option { return func(c *config) { c.handler = h } }

// WithHTTPClient sets the HTTP client of a toolset with a "url" (default: a
// client with no timeout, since agent_run reads a stream). Its transport
// gets the forwarded credential added.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.hc = hc } }

// WithCallTimeout is the MCP server's limit on a tool call (0: none).
// agent_run then returns a little before it, with the run still going if it
// has not ended, rather than be cut off and lose the run's ID.
func WithCallTimeout(d time.Duration) Option { return func(c *config) { c.callTimeout = d } }

// WithClock sets the clock of agent_run's wait (default clock.Real).
func WithClock(clk clock.Clock) Option { return func(c *config) { c.clock = clk } }

// WithLogger sets the logger (default: discard).
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// Factory builds agentkit toolsets. It is a toolset.Factory of [Type].
// Close it at shutdown: it follows, in the background, the runs agent_run
// stopped waiting for.
type Factory struct {
	cfg config

	ctx    context.Context // done on Close: the background followers stop
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

var _ toolset.Factory = (*Factory)(nil)

// New returns a factory of agentkit toolsets. It fails for a negative call
// timeout.
func New(opts ...Option) (*Factory, error) {
	c := config{clock: clock.Real(), log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&c)
	}
	switch {
	case c.callTimeout < 0:
		return nil, errors.New("agenttools: negative call timeout")
	case c.clock == nil || c.log == nil:
		return nil, errors.New("agenttools: nil clock or logger")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Factory{cfg: c, ctx: ctx, cancel: cancel}, nil
}

// Type returns [Type].
func (f *Factory) Type() string { return Type }

// Build validates the settings and returns the five tools. It makes no
// request.
func (f *Factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	raw := in.Settings
	var s Settings
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return nil, err
	}
	wait := DefaultTimeout
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil || d <= 0 || d > MaxTimeout {
			return nil, fmt.Errorf("%w: timeout %q: want a duration above 0 and at most %v", toolset.ErrInvalidSettings, s.Timeout, MaxTimeout)
		}
		wait = d
	}
	if ct := f.cfg.callTimeout; ct > 0 && wait > ct-margin(ct) {
		wait = ct - margin(ct)
		f.cfg.log.Info("agent_run waits less than its timeout, within the MCP call timeout; raise mcp.call_timeout to wait longer",
			"timeout", s.Timeout, "wait", wait.String(), "call_timeout", ct.String())
	}
	var (
		base string
		hc   *http.Client
	)
	switch {
	case s.URL != "":
		base, hc = s.URL, f.httpClient()
	case f.cfg.handler != nil:
		base, hc = inProcessURL, &http.Client{Transport: inProcess{f.cfg.handler}}
	default:
		return nil, fmt.Errorf("%w: url is required: the agent API does not run in this process", toolset.ErrInvalidSettings)
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("%w: url %q: want the agent API's base URL, http(s)://host[:port][/prefix]", toolset.ErrInvalidSettings, s.URL)
	}
	hc.Transport = newForwarding(hc.Transport, u)
	api, err := agentclient.New(base, agentclient.WithHTTPClient(hc), agentclient.WithUserAgent("agentkit-agenttools"))
	if err != nil {
		return nil, fmt.Errorf("%w: url: %w", toolset.ErrInvalidSettings, err)
	}
	return newTools(f, api, wait)
}

// inProcessURL is the base URL of requests served in process: never
// dialled.
const inProcessURL = "http://agent-api.in-process"

// httpClient is a copy of the configured client (default: a new one), so
// that the forwarding transport wraps only ours.
func (f *Factory) httpClient() *http.Client {
	hc := &http.Client{}
	if f.cfg.hc != nil {
		*hc = *f.cfg.hc
	}
	if hc.Transport == nil {
		hc.Transport = http.DefaultTransport
	}
	return hc
}

// margin is how much before the call timeout agent_run returns: a tenth of
// it, at most 5s.
func margin(callTimeout time.Duration) time.Duration {
	return min(callTimeout/10, 5*time.Second)
}

// Close stops following the runs agent_run stopped waiting for (the API
// then treats them as runs whose client went away), waits for that, and
// makes agent_run fail with [ErrClosed]. The other tools still work.
func (f *Factory) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.cancel()
	f.wg.Wait()
	return nil
}

// spawn runs fn in a goroutine that Close waits for; false after Close.
func (f *Factory) spawn(fn func()) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		fn()
	}()
	return true
}

// detach returns a context with ctx's values (the forwarded credential) that
// is not done when ctx is, but on Close or cancel.
func (f *Factory) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	d, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(f.ctx, cancel)
	return d, func() {
		stop()
		cancel()
	}
}
