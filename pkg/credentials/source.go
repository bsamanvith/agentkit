package credentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// ErrNotConnected reports a toolset with no usable credential for the
// caller: none is stored at its slot for the tenant (or user), or the one
// stored needs a person to connect the account again. It is what a tool
// returns for the model to read, so the error that matches it
// ([*NotConnectedError]) says only which toolset.
var ErrNotConnected = errors.New("credentials: not connected")

// NotConnectedError is the [ErrNotConnected] of one toolset. Its message is
// "<toolset> is not connected for this account", with no detail from the
// vault: it reaches models, MCP clients and API responses.
type NotConnectedError struct {
	Toolset string
}

func (e *NotConnectedError) Error() string {
	return e.Toolset + " is not connected for this account"
}

// Is reports whether target is [ErrNotConnected].
func (e *NotConnectedError) Is(target error) bool { return target == ErrNotConnected }

// SourceError is a credential a toolset could not get for another reason
// than [ErrNotConnected]: the vault failed, or the token endpoint did. Its
// message names the toolset and, for a token endpoint, its HTTP status and
// standard error code, never a body or a value; Err is the cause, for logs
// and errors.Is (it may hold an endpoint's body: do not show it).
type SourceError struct {
	Toolset string
	// Reason is a short, safe description ("the token endpoint answered 503").
	Reason string
	Err    error
}

func (e *SourceError) Error() string { return e.Toolset + ": " + e.Reason }

// Unwrap returns the cause.
func (e *SourceError) Unwrap() error { return e.Err }

// Scope says whose credential a toolset uses.
type Scope string

// The scopes of a credential slot.
const (
	// ScopeTenant uses the credential the tenant shares (an org's
	// connection): the context's tenant, no user.
	ScopeTenant Scope = "tenant"
	// ScopeUser uses the calling user's own credential (delegated access,
	// such as a mailbox): the context's tenant and user.
	ScopeUser Scope = "user"
)

// ErrInvalidScope reports a scope other than [ScopeTenant] and [ScopeUser].
var ErrInvalidScope = errors.New("credentials: invalid scope")

// CheckSlot reports whether slot is a valid slot name ([ErrInvalidKey]).
func CheckSlot(slot string) error { return checkSlot(slot) }

// CheckScope reports whether s is a scope; "" is [ScopeTenant].
func CheckScope(s Scope) error {
	switch s {
	case "", ScopeTenant, ScopeUser:
		return nil
	}
	return fmt.Errorf("%w %q: want %q or %q", ErrInvalidScope, s, ScopeTenant, ScopeUser)
}

// Source is the credential of one toolset's slot, resolved at call time:
// the tenant (and, for [ScopeUser], the user) come from the context, as
// pkg/tenancy carries them. It has no way to name another slot: a factory
// given one can read only the slot its spec names. Implementations are safe
// for concurrent use.
type Source interface {
	// Get returns the caller's credential: an [APIKey], a [Basic], or an
	// [OAuth2] whose access token is fresh (refreshed when it expires
	// within the margin, or fetched with a client credentials grant). It
	// fails with [ErrNotConnected] when there is none to use.
	Get(ctx context.Context) (Credential, error)
	// HTTPClient returns a copy of base (nil: http.DefaultClient) that
	// sends the caller's credential with every request: a bearer token
	// (OAuth2, client credentials, and API keys unless [APIKeyHeader]
	// says otherwise) or basic auth. It refreshes as Get does, and refuses
	// redirects to another host. It fails with [ErrNotConnected] at once
	// when there is no credential; a request whose credential fails later
	// fails with that error (wrapped in a *url.Error).
	HTTPClient(ctx context.Context, base *http.Client, opts ...AuthOption) (*http.Client, error)
}

// AuthOption configures how [Source.HTTPClient] sends a credential.
type AuthOption func(*authConfig)

type authConfig struct {
	apiKeyHeader string // "" for Authorization: Bearer
}

// APIKeyHeader sends an [APIKey] as the value of header name (such as
// "X-API-Key") instead of as a bearer token.
func APIKeyHeader(name string) AuthOption {
	return func(c *authConfig) { c.apiKeyHeader = http.CanonicalHeaderKey(name) }
}

// OAuth2App is the OAuth2 client a slot's tokens were issued to: what a
// refresh sends to the token endpoint.
type OAuth2App struct {
	ClientID     string
	ClientSecret Secret
	TokenURL     string
	// AuthStyle says how the client authenticates to the token endpoint.
	AuthStyle AuthStyle
}

// AuthStyle is how an OAuth2 client sends its id and secret.
type AuthStyle int

// The auth styles. There is no auto-detection: it would send a failed
// request twice.
const (
	// AuthInHeader is HTTP basic auth (RFC 6749 §2.3.1), the default.
	AuthInHeader AuthStyle = iota
	// AuthInParams puts client_id and client_secret in the form.
	AuthInParams
)

// Sources hands out the [Source] of each toolset's slot, over one vault.
// It refreshes OAuth2 tokens once per (tenant, user, slot) at a time in this
// process, writes a refreshed token back with compare-and-swap (so
// replicas sharing the vault end with one token chain), and caches client
// credentials tokens in memory. Make one with [NewSources].
type Sources struct {
	vault       Vault
	apps        map[string]OAuth2App
	clock       clock.Clock
	margin      time.Duration
	timeout     time.Duration
	log         *slog.Logger
	tokenClient *http.Client

	mu      sync.Mutex
	flights map[flightKey]*flight
	tokens  map[flightKey]cachedToken // client credentials tokens
}

// SourcesOption configures [NewSources].
type SourcesOption func(*Sources)

// DefaultRefreshMargin is how long before its expiry a token is refreshed.
const DefaultRefreshMargin = 60 * time.Second

// DefaultTokenTimeout bounds one exchange with a token endpoint.
const DefaultTokenTimeout = 30 * time.Second

// WithOAuth2App sets the OAuth2 client of slot, which refreshing its tokens
// needs. Without one, an OAuth2 credential at slot is used until it expires.
func WithOAuth2App(slot string, app OAuth2App) SourcesOption {
	return func(s *Sources) { s.apps[slot] = app }
}

// WithClock sets the clock token expiry is read against (default real).
func WithClock(c clock.Clock) SourcesOption { return func(s *Sources) { s.clock = c } }

// WithRefreshMargin sets how long before expiry a token is refreshed
// (default [DefaultRefreshMargin]).
func WithRefreshMargin(d time.Duration) SourcesOption { return func(s *Sources) { s.margin = d } }

// WithTokenTimeout bounds one exchange with a token endpoint (default
// [DefaultTokenTimeout]).
func WithTokenTimeout(d time.Duration) SourcesOption { return func(s *Sources) { s.timeout = d } }

// WithLogger sets the logger (default none). It gets refreshes and failures
// at debug, info and warn, and each authenticated request at debug: the
// toolset, slot, tenant, method, host and status, never a header or value.
func WithLogger(l *slog.Logger) SourcesOption { return func(s *Sources) { s.log = l } }

// WithTokenClient sets the HTTP client token endpoints are called with
// (default http.DefaultClient's transport). Whichever it is, redirects are
// not followed: a 307 or 308 would send the refresh token and the client
// secret, which are in the request body, to wherever it points.
func WithTokenClient(c *http.Client) SourcesOption { return func(s *Sources) { s.tokenClient = c } }

// NewSources returns the sources over v.
func NewSources(v Vault, opts ...SourcesOption) (*Sources, error) {
	if v == nil {
		return nil, errors.New("credentials: NewSources: nil vault")
	}
	s := &Sources{
		vault: v, apps: map[string]OAuth2App{}, clock: clock.Real(),
		margin: DefaultRefreshMargin, timeout: DefaultTokenTimeout,
		log: slog.New(slog.DiscardHandler), tokenClient: http.DefaultClient,
		flights: map[flightKey]*flight{}, tokens: map[flightKey]cachedToken{},
	}
	for _, o := range opts {
		o(s)
	}
	for slot, app := range s.apps {
		if err := checkSlot(slot); err != nil {
			return nil, fmt.Errorf("credentials: OAuth2 app: %w", err)
		}
		if app.ClientID == "" || app.TokenURL == "" {
			return nil, fmt.Errorf("credentials: OAuth2 app of slot %q needs a client id and a token URL", slot)
		}
		if app.AuthStyle != AuthInHeader && app.AuthStyle != AuthInParams {
			return nil, fmt.Errorf("credentials: OAuth2 app of slot %q: unknown auth style %d", slot, app.AuthStyle)
		}
	}
	if s.clock == nil || s.log == nil || s.tokenClient == nil {
		return nil, errors.New("credentials: NewSources: nil clock, logger or token client")
	}
	tc := *s.tokenClient
	tc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s.tokenClient = &tc
	if s.margin < 0 || s.timeout <= 0 {
		return nil, errors.New("credentials: NewSources: the margin must not be negative and the timeout must be positive")
	}
	return s, nil
}

// Source returns the source of slot for the toolset named toolset (which
// errors name): with [ScopeTenant] (or "") the tenant's shared credential,
// with [ScopeUser] the calling user's.
func (s *Sources) Source(toolset, slot string, scope Scope) (Source, error) {
	if err := checkSlot(slot); err != nil {
		return nil, err
	}
	if err := CheckScope(scope); err != nil {
		return nil, err
	}
	if scope == "" {
		scope = ScopeTenant
	}
	return &source{p: s, toolset: toolset, slot: slot, scope: scope}, nil
}

// flightKey is a credential's key in this process.
type flightKey struct{ tenant, user, slot string }

type flight struct {
	done chan struct{}
	cred Credential
	err  error
}

type cachedToken struct {
	version int64
	token   OAuth2
}

// once runs fn for k unless a call for k is already running, in which case
// it waits for that one's result. fn runs on its own goroutine with ctx's
// values but not its cancellation (bounded by the token timeout), so a
// caller that gives up does not fail the others waiting.
func (s *Sources) once(ctx context.Context, k flightKey, fn func(context.Context) (Credential, error)) (Credential, error) {
	s.mu.Lock()
	f, running := s.flights[k]
	if !running {
		f = &flight{done: make(chan struct{})}
		s.flights[k] = f
	}
	s.mu.Unlock()
	if !running {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		go func() {
			defer cancel()
			f.cred, f.err = fn(fctx)
			s.mu.Lock()
			delete(s.flights, k)
			s.mu.Unlock()
			close(f.done)
		}()
	}
	select {
	case <-f.done:
		return f.cred, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fresh reports whether o's access token is good past the margin.
func (s *Sources) fresh(o OAuth2) bool {
	return !o.AccessToken.IsZero() && (o.Expiry.IsZero() || s.clock.Now().Add(s.margin).Before(o.Expiry))
}

// usable reports whether o's access token has not expired yet.
func (s *Sources) usable(o OAuth2) bool {
	return !o.AccessToken.IsZero() && (o.Expiry.IsZero() || s.clock.Now().Before(o.Expiry))
}

type source struct {
	p       *Sources
	toolset string
	slot    string
	scope   Scope
}

func (s *source) notConnected() error { return &NotConnectedError{Toolset: s.toolset} }

// key is the caller's credential key: no tenant, or no user for a user
// scope, is not connected.
func (s *source) key(ctx context.Context) (flightKey, error) {
	t, ok := tenancy.From(ctx)
	if !ok {
		return flightKey{}, s.notConnected()
	}
	k := flightKey{tenant: t.ID(), slot: s.slot}
	if s.scope == ScopeUser {
		u, ok := tenancy.User(ctx)
		if !ok || u == "" {
			return flightKey{}, s.notConnected()
		}
		k.user = u
	}
	return k, nil
}

// ctxFor is ctx scoped to k's tenant and user, whatever ctx carried.
func ctxFor(ctx context.Context, k flightKey) context.Context {
	t, _ := tenancy.NewTenant(k.tenant) // came from a Tenant
	ctx = tenancy.With(ctx, t)
	if k.user != "" {
		ctx = tenancy.WithUser(ctx, k.user)
	}
	return ctx
}

func (s *source) Get(ctx context.Context) (Credential, error) {
	k, err := s.key(ctx)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, k)
}

// load reads k's credential: none, or one marked for reconnecting, is not
// connected; another failure is a SourceError that keeps the cause out of
// its message.
func (s *source) load(ctx context.Context, k flightKey) (Credential, Meta, error) {
	cred, meta, err := s.p.vault.Get(ctxFor(ctx, k), k.user, k.slot)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, Meta{}, s.notConnected()
	case err != nil:
		if cerr := ctx.Err(); cerr != nil {
			return nil, Meta{}, cerr
		}
		s.p.log.Error("reading a credential", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "err", err)
		return nil, Meta{}, &SourceError{Toolset: s.toolset, Reason: "its credential could not be read", Err: err}
	case meta.NeedsReconnect:
		return nil, Meta{}, s.notConnected()
	}
	return cred, meta, nil
}

func (s *source) get(ctx context.Context, k flightKey) (Credential, error) {
	cred, meta, err := s.load(ctx, k)
	if err != nil {
		return nil, err
	}
	switch c := cred.(type) {
	case OAuth2:
		if s.p.fresh(c) {
			return c, nil
		}
		if _, app := s.p.apps[k.slot]; (!app || c.RefreshToken.IsZero()) && s.p.usable(c) {
			return c, nil // cannot be refreshed: used until it expires
		}
		return s.own(s.p.once(ctx, k, func(ctx context.Context) (Credential, error) { return s.refresh(ctx, k) }))
	case ClientCredentials:
		if tok, ok := s.p.cached(k, meta.Version); ok {
			return tok, nil
		}
		return s.own(s.p.once(ctx, k, func(ctx context.Context) (Credential, error) { return s.clientToken(ctx, k) }))
	}
	return cred, nil
}

// own names this source's toolset in an error of a flight that another
// toolset sharing the slot started: flights are per credential, not per
// toolset.
func (s *source) own(c Credential, err error) (Credential, error) {
	var nc *NotConnectedError
	var se *SourceError
	switch {
	case errors.As(err, &nc) && nc.Toolset != s.toolset:
		return nil, s.notConnected()
	case errors.As(err, &se) && se.Toolset != s.toolset:
		return nil, &SourceError{Toolset: s.toolset, Reason: se.Reason, Err: se.Err}
	}
	return c, err
}

func (s *Sources) cached(k flightKey, version int64) (OAuth2, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.tokens[k]
	if !ok || c.version != version || !s.fresh(c.token) {
		return OAuth2{}, false
	}
	return c.token, true
}

// HTTPClient implements [Source].
func (s *source) HTTPClient(ctx context.Context, base *http.Client, opts ...AuthOption) (*http.Client, error) {
	k, err := s.key(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.get(ctx, k); err != nil {
		return nil, err
	}
	var cfg authConfig
	for _, o := range opts {
		o(&cfg)
	}
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = &authTransport{src: s, key: k, cfg: cfg, next: next}
	check := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("credentials: %s: refusing a redirect to another host, which would get the credential", s.toolset)
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("credentials: %s: refusing a redirect from https to %s, which would send the credential in the clear", s.toolset, req.URL.Scheme)
		}
		if check != nil {
			return check(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &c, nil
}

// authTransport adds a credential to each request.
type authTransport struct {
	src  *source
	key  flightKey
	cfg  authConfig
	next http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cred, err := t.src.get(req.Context(), t.key)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	out := req.Clone(req.Context())
	switch c := cred.(type) {
	case APIKey:
		if t.cfg.apiKeyHeader != "" {
			out.Header.Set(t.cfg.apiKeyHeader, c.Key.Reveal())
		} else {
			out.Header.Set("Authorization", "Bearer "+c.Key.Reveal())
		}
	case Basic:
		out.SetBasicAuth(c.User, c.Password.Reveal())
	case OAuth2:
		out.Header.Set("Authorization", tokenType(c.TokenType)+" "+c.AccessToken.Reveal())
	}
	resp, err := t.next.RoundTrip(out)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	t.src.p.log.Debug("credential sent", "toolset", t.src.toolset, "slot", t.key.slot, "tenant", t.key.tenant,
		"method", req.Method, "host", req.URL.Host, "status", status)
	return resp, err
}

// tokenType is the scheme an access token is sent with: "Bearer" unless the
// token endpoint named another, in x/oauth2's canonical case (RFC 6750 says
// the scheme is case insensitive; some servers are not).
func tokenType(t string) string { return (&oauth2.Token{TokenType: t}).Type() }
