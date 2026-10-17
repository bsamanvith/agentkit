package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is a [Conn] over HTTP to a server's base URL. Build it with [Dial].
type Client struct {
	base     string // without a trailing slash
	http     *http.Client
	token    string
	idle     time.Duration
	maxFrame int
}

// Client defaults.
const (
	// DefaultIdleTimeout is how long a call may go without hearing from the
	// server, pings included, before the client gives up on it
	// ([WithIdleTimeout]): four of the server's [DefaultPingInterval].
	DefaultIdleTimeout = 4 * DefaultPingInterval
	// DefaultMaxFrameBytes is the largest frame a client reads
	// ([WithMaxFrameBytes]): one message, such as a run's result.
	DefaultMaxFrameBytes = 64 << 20
)

var _ Conn = (*Client)(nil)

// DialOption configures [Dial].
type DialOption func(*Client)

// WithBearer sends "Authorization: Bearer token" with every call: the
// server's [WithToken].
func WithBearer(token string) DialOption { return func(c *Client) { c.token = token } }

// WithHTTPClient sends calls with hc (default: a client with no timeout, so
// streams last as long as their context).
func WithHTTPClient(hc *http.Client) DialOption { return func(c *Client) { c.http = hc } }

// WithIdleTimeout sets how long a call may go without receiving a byte from
// the server, pings included, before it fails with [ErrUnavailable] (default
// [DefaultIdleTimeout]; zero is the default, a negative value waits as long
// as the context). It finds a server that hung or a connection that died
// without closing. Time the caller spends handling a message does not count.
func WithIdleTimeout(d time.Duration) DialOption { return func(c *Client) { c.idle = d } }

// WithMaxFrameBytes limits the size of one frame of an answer (default
// [DefaultMaxFrameBytes]); a larger one fails the call with [ErrUnavailable].
func WithMaxFrameBytes(n int) DialOption { return func(c *Client) { c.maxFrame = n } }

// Dial returns a client of the server at base, an http or https URL such as
// "http://runner:8080": a method "svc/Name" is called at
// base + [PathPrefix] + "svc/Name". It does not connect; calls do.
func Dial(base string, opts ...DialOption) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("rpc: base URL %q: want http(s)://host[:port][/path]", base)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("rpc: base URL %q: no query, fragment or user info", base)
	}
	c := &Client{base: strings.TrimSuffix(u.String(), "/"), http: &http.Client{}}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.idle == 0 {
		c.idle = DefaultIdleTimeout
	}
	if c.maxFrame <= 0 {
		c.maxFrame = DefaultMaxFrameBytes
	}
	return c, nil
}

// Call implements [Conn].
func (c *Client) Call(ctx context.Context, method string, req json.RawMessage) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		parent := ctx
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		fail := func(err error) {
			if ctx.Err() != nil {
				err = context.Cause(ctx)
			}
			yield(nil, err)
		}
		// The idle timer runs only while the client waits on the server: for
		// the answer's headers, then in each read of its body.
		idle := func() (stop func() bool) { return func() bool { return false } }
		if c.idle > 0 {
			stalled := fmt.Errorf("%w: %s sent nothing for %v", ErrUnavailable, c.base, c.idle)
			t := time.AfterFunc(c.idle, func() {
				if parent.Err() == nil {
					cancel(stalled)
				}
			})
			t.Stop()
			defer t.Stop()
			idle = func() func() bool {
				t.Reset(c.idle)
				return t.Stop
			}
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+PathPrefix+method, bytes.NewReader(req))
		if err != nil {
			yield(nil, fmt.Errorf("%w: %w", ErrUnavailable, err))
			return
		}
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Accept", ContentType)
		if c.token != "" {
			hreq.Header.Set("Authorization", "Bearer "+c.token)
		}
		if d, ok := ctx.Deadline(); ok {
			ms := max(time.Until(d).Milliseconds(), 1)
			hreq.Header.Set(TimeoutHeader, strconv.FormatInt(ms, 10))
		}
		stop := idle()
		resp, err := c.http.Do(hreq)
		stop()
		if err != nil {
			fail(fmt.Errorf("%w: %w", ErrUnavailable, err))
			return
		}
		defer resp.Body.Close()
		if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != ContentType {
			fail(fmt.Errorf("%w: %s answered %s (%s), not this protocol", ErrUnavailable, c.base, resp.Status, mt))
			return
		}
		lines := bufio.NewScanner(idleReader{resp.Body, idle})
		lines.Buffer(make([]byte, 0, min(64<<10, c.maxFrame)), c.maxFrame)
		for {
			if !lines.Scan() {
				err := lines.Err()
				switch {
				case err == nil:
					err = io.ErrUnexpectedEOF
				case errors.Is(err, bufio.ErrTooLong):
					err = fmt.Errorf("frame over %d bytes", c.maxFrame)
				}
				fail(fmt.Errorf("%w: stream cut short: %w", ErrUnavailable, err))
				return
			}
			var f frame
			if err := json.Unmarshal(lines.Bytes(), &f); err != nil {
				fail(fmt.Errorf("%w: bad frame: %w", ErrUnavailable, err))
				return
			}
			switch {
			case f.Ping:
				continue
			case f.Error != nil:
				yield(nil, f.Error)
				return
			case f.End:
				return
			case f.Data == nil:
				fail(fmt.Errorf("%w: empty frame", ErrUnavailable))
				return
			}
			if !yield(f.Data, nil) {
				return
			}
		}
	}
}

// idleReader runs the idle timer during each read: a large frame that
// keeps arriving is not a stall.
type idleReader struct {
	r    io.Reader
	idle func() (stop func() bool)
}

func (r idleReader) Read(p []byte) (int, error) {
	stop := r.idle()
	defer stop()
	return r.r.Read(p)
}

// Pipe returns a [Conn] that calls s in process: no network, but requests,
// messages and errors are encoded as over HTTP, so a component behaves the
// same through a Pipe as through [Dial]. No token is checked.
func Pipe(s *Server) Conn { return pipe{s} }

type pipe struct{ s *Server }

func (p pipe) Call(ctx context.Context, method string, req json.RawMessage) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stopped := false
		werr := p.s.dispatch(ctx, method, bytes.Clone(req), func(raw json.RawMessage) error {
			if stopped || !yield(raw, nil) {
				stopped = true
				cancel()
				return errStop
			}
			return nil
		})
		switch {
		case stopped:
		case werr != nil:
			// What the caller gets over HTTP: the error decoded from JSON.
			raw, err := json.Marshal(werr)
			if err == nil {
				var e Error
				if err = json.Unmarshal(raw, &e); err == nil {
					werr = &e
				}
			}
			yield(nil, werr)
		}
	}
}
