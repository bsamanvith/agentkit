package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport selects how [Connect] reaches the server.
type Transport string

// The transports.
const (
	// TransportAuto tries Streamable HTTP, then HTTP+SSE.
	TransportAuto  Transport = "auto"
	TransportHTTP  Transport = "http"  // Streamable HTTP
	TransportSSE   Transport = "sse"   // the older HTTP+SSE transport
	TransportStdio Transport = "stdio" // a command, over its stdin and stdout
)

// Defaults for the zero fields of [Config].
const (
	DefaultCallTimeout    = 60 * time.Second
	DefaultConnectTimeout = 15 * time.Second
	DefaultMaxResultBytes = 1 << 20
)

// ErrClosed reports a call on a closed [Client].
var ErrClosed = errors.New("mcp client closed")

// Config says how to reach one MCP server.
type Config struct {
	// URL is the server's endpoint, for the HTTP transports.
	URL string
	// Transport is how to connect; "" is [TransportAuto].
	Transport Transport
	// Command and Args run the server for [TransportStdio]. Env is the
	// command's whole environment ("NAME=value"): nothing is inherited.
	Command string
	Args    []string
	Env     []string
	// Header is sent with every HTTP request.
	Header http.Header
	// HTTPClient makes the HTTP requests (default: a client of its own).
	HTTPClient *http.Client
	// CallTimeout bounds one tool call (default [DefaultCallTimeout]).
	CallTimeout time.Duration
	// ConnectTimeout bounds connecting and initializing (default
	// [DefaultConnectTimeout]).
	ConnectTimeout time.Duration
	// MaxResultBytes caps a tool result, as JSON (default
	// [DefaultMaxResultBytes]).
	MaxResultBytes int
}

// Client is a connection to one MCP server. It is safe for concurrent use,
// and reconnects when the server drops its session.
type Client struct {
	cfg  Config
	impl *mcp.Client

	mu        sync.Mutex // guards the fields below; held while connecting
	cs        *mcp.ClientSession
	transport Transport // the one that last connected
	closed    bool

	changed chan struct{}
	done    chan struct{} // closed by Close
	once    sync.Once
}

// Connect connects to the server, so a server that does not answer fails
// here rather than on the first call. Close the client when done with it.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Transport == "" {
		cfg.Transport = TransportAuto
	}
	switch cfg.Transport {
	case TransportAuto, TransportHTTP, TransportSSE:
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp client: transport %q needs a URL", cfg.Transport)
		}
	case TransportStdio:
		if cfg.Command == "" {
			return nil, errors.New("mcp client: transport \"stdio\" needs a command")
		}
	default:
		return nil, fmt.Errorf("mcp client: unknown transport %q (auto, http, sse or stdio)", cfg.Transport)
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = DefaultMaxResultBytes
	}
	base := http.DefaultTransport
	if cfg.HTTPClient != nil && cfg.HTTPClient.Transport != nil {
		base = cfg.HTTPClient.Transport
	}
	hc := &http.Client{Transport: &headerTransport{header: cfg.Header, base: base, limit: wireLimit(cfg.MaxResultBytes)}}
	if cfg.HTTPClient != nil {
		hc.CheckRedirect, hc.Jar = cfg.HTTPClient.CheckRedirect, cfg.HTTPClient.Jar
	}
	cfg.HTTPClient = hc

	c := &Client{cfg: cfg, changed: make(chan struct{}, 1), done: make(chan struct{})}
	c.impl = mcp.NewClient(&mcp.Implementation{Name: "agentkit", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{}, // no roots, sampling or elicitation
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case c.changed <- struct{}{}:
			default: // one pending signal is enough
			}
		},
	})
	if _, err := c.session(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Transport reports the transport that last connected: [TransportHTTP],
// [TransportSSE] or [TransportStdio].
func (c *Client) Transport() Transport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport
}

// ToolsChanged receives a value after the server sends
// notifications/tools/list_changed (several notifications may arrive as one).
// Call [Client.Tools] to see what changed.
func (c *Client) ToolsChanged() <-chan struct{} { return c.changed }

// Done is closed when the client is closed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close ends the session (and stops a stdio server).
func (c *Client) Close() error {
	c.once.Do(func() { close(c.done) })
	c.mu.Lock()
	cs := c.cs
	c.cs, c.closed = nil, true
	c.mu.Unlock()
	if cs != nil {
		return cs.Close()
	}
	return nil
}

// Tools lists the server's tools, every page, as the server sent them
// (descriptions not sanitized).
func (c *Client) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	err := c.retry(ctx, func(cs *mcp.ClientSession) error {
		tools = nil
		for t, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			tools = append(tools, t)
		}
		return nil
	})
	return tools, err
}

// Call calls a tool with JSON arguments (an object) within the call timeout.
// A protocol error, a timeout or a broken connection is an error; a result
// with isError set is returned as it is.
func (c *Client) Call(ctx context.Context, name string, args []byte) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
	defer cancel()
	over := new(atomic.Bool)
	ctx = context.WithValue(ctx, overKey{}, over)
	var res *mcp.CallToolResult
	err := c.retry(ctx, func(cs *mcp.ClientSession) error {
		var err error
		res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: rawObject(args)})
		return err
	})
	if err != nil && over.Load() {
		err = fmt.Errorf("%w: %w", errTooLarge, err)
	}
	return res, err
}

// retry runs fn on the current session. When the server says the session is
// gone, it never ran the request, so fn runs once more on a new session.
// Any other broken connection is dropped so the next call reconnects.
func (c *Client) retry(ctx context.Context, fn func(*mcp.ClientSession) error) error {
	for attempt := 0; ; attempt++ {
		cs, err := c.session(ctx)
		if err != nil {
			return err
		}
		err = fn(cs)
		if err == nil {
			return nil
		}
		gone := errors.Is(err, mcp.ErrSessionMissing)
		if gone || errors.Is(err, mcp.ErrConnectionClosed) || errors.Is(err, io.EOF) {
			c.drop(cs)
		}
		if !gone || attempt > 0 || ctx.Err() != nil {
			return err
		}
	}
}

// session returns the open session, connecting if there is none.
func (c *Client) session(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	if c.cs != nil {
		return c.cs, nil
	}
	// The session lives on a context of its own: the SSE transport keeps
	// the context it connected with for its stream. Connecting is bounded by
	// the caller's context and the connect timeout.
	life, kill := context.WithCancel(context.WithoutCancel(ctx))
	var timedOut atomic.Bool
	timer := time.AfterFunc(c.cfg.ConnectTimeout, func() { timedOut.Store(true); kill() })
	stop := context.AfterFunc(ctx, kill)
	cs, tr, err := c.connect(life)
	connected := timer.Stop() && stop()
	if err != nil || !connected {
		kill()
		if cs != nil {
			_ = cs.Close()
		}
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case timedOut.Load() || err == nil:
			return nil, fmt.Errorf("mcp client: no answer within %s", c.cfg.ConnectTimeout)
		}
		return nil, err
	}
	c.cs, c.transport = cs, tr
	go func() {
		_ = cs.Wait() // the server went away, or Close
		kill()
		c.drop(cs)
	}()
	return cs, nil
}

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, Transport, error) {
	switch tr := c.cfg.Transport; tr {
	case TransportHTTP, TransportSSE, TransportStdio:
		cs, err := c.impl.Connect(ctx, c.mcpTransport(tr), nil)
		return cs, tr, err
	}
	cs, err := c.impl.Connect(ctx, c.mcpTransport(TransportHTTP), nil)
	if err == nil || ctx.Err() != nil {
		return cs, TransportHTTP, err
	}
	cs, sseErr := c.impl.Connect(ctx, c.mcpTransport(TransportSSE), nil)
	if sseErr != nil {
		return nil, "", fmt.Errorf("streamable HTTP: %w; HTTP+SSE: %w", err, sseErr)
	}
	return cs, TransportSSE, nil
}

func (c *Client) mcpTransport(tr Transport) mcp.Transport {
	switch tr {
	case TransportSSE:
		return &mcp.SSEClientTransport{Endpoint: c.cfg.URL, HTTPClient: c.cfg.HTTPClient, MaxEventSize: c.maxEvent()}
	case TransportStdio:
		cmd := exec.Command(c.cfg.Command, c.cfg.Args...) //nolint:gosec,noctx // the operator's command, from config; it outlives the connect context
		cmd.Env = append([]string{}, c.cfg.Env...)
		return &mcp.CommandTransport{Command: cmd}
	default:
		return &mcp.StreamableClientTransport{Endpoint: c.cfg.URL, HTTPClient: c.cfg.HTTPClient, MaxEventSize: c.maxEvent()}
	}
}

// wireLimit is the most a message carrying a result of maxResult bytes may
// take: the result is sent as content and may be sent again as
// structuredContent, plus the JSON-RPC envelope.
func wireLimit(maxResult int) int64 { return 2*int64(maxResult) + 64<<10 }

func (c *Client) maxEvent() int { return int(wireLimit(c.cfg.MaxResultBytes)) }

// drop forgets cs if it is still the current session, and closes it.
func (c *Client) drop(cs *mcp.ClientSession) {
	c.mu.Lock()
	if c.cs == cs {
		c.cs = nil
	}
	c.mu.Unlock()
	go func() { _ = cs.Close() }()
}

// headerTransport adds headers to every request and caps the bodies of the
// answers to POSTs, which carry tool results. (A GET is the long-lived
// stream of server notifications.)
type headerTransport struct {
	header http.Header
	base   http.RoundTripper
	limit  int64
}

func (h *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(h.header) > 0 {
		r = r.Clone(r.Context())
		for k, v := range h.header {
			r.Header[k] = append([]string(nil), v...)
		}
	}
	resp, err := h.base.RoundTrip(r)
	if err != nil || r.Method != http.MethodPost {
		return resp, err
	}
	over, _ := r.Context().Value(overKey{}).(*atomic.Bool)
	resp.Body = &cappedBody{rc: resp.Body, left: h.limit, over: over}
	return resp, nil
}

// errTooLarge ends a response body longer than the cap.
var errTooLarge = errors.New("the server's answer is larger than the result limit")

// overKey marks a call's context with the flag its capped body sets.
type overKey struct{}

type cappedBody struct {
	rc   io.ReadCloser
	left int64
	over *atomic.Bool // set when the cap is hit; nil outside a call
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		if b.over != nil {
			b.over.Store(true)
		}
		return 0, errTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *cappedBody) Close() error { return b.rc.Close() }

// redactURL drops a URL's query and user info, for messages and logs.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}
