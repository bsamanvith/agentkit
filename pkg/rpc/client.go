package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	base  string // without a trailing slash
	http  *http.Client
	token string
}

var _ Conn = (*Client)(nil)

// DialOption configures [Dial].
type DialOption func(*Client)

// WithBearer sends "Authorization: Bearer token" with every call: the
// server's [WithToken].
func WithBearer(token string) DialOption { return func(c *Client) { c.token = token } }

// WithHTTPClient sends calls with hc (default: a client with no timeout, so
// streams last as long as their context).
func WithHTTPClient(hc *http.Client) DialOption { return func(c *Client) { c.http = hc } }

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
	return c, nil
}

// Call implements [Conn].
func (c *Client) Call(ctx context.Context, method string, req json.RawMessage) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		fail := func(err error) {
			if ctx.Err() != nil {
				err = context.Cause(ctx)
			}
			yield(nil, err)
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
		resp, err := c.http.Do(hreq)
		if err != nil {
			fail(fmt.Errorf("%w: %w", ErrUnavailable, err))
			return
		}
		defer resp.Body.Close()
		if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != ContentType {
			fail(fmt.Errorf("%w: %s answered %s (%s), not this protocol", ErrUnavailable, c.base, resp.Status, mt))
			return
		}
		dec := json.NewDecoder(resp.Body)
		for {
			var f frame
			if err := dec.Decode(&f); err != nil {
				fail(fmt.Errorf("%w: stream cut short: %w", ErrUnavailable, err))
				return
			}
			switch {
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
