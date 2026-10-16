package oauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Paths [Connector.Handler] serves and the callback the console serves, under
// [Config].PublicURL.
const (
	ClientMetadataPath = "/oauth/client-metadata.json"
	CallbackPath       = "/oauth/callback"
)

// Config configures a [Connector].
type Config struct {
	// PublicURL is agentkit's public base URL (AGENTKIT_PUBLIC_URL). The
	// redirect URI is PublicURL + CallbackPath. When it is https, PublicURL
	// + ClientMetadataPath is our CIMD client_id.
	PublicURL string
	// ClientName is shown on consent screens; "" is "agentkit".
	ClientName string

	Tokens  TokenStore
	Clients ClientStore
	States  StateStore

	// HTTPClient makes every request to MCP and authorization servers; nil
	// is http.DefaultClient. T096's SSRF-guarded client goes here.
	HTTPClient *http.Client
	// Clock is for state expiry and token freshness; nil is the real clock.
	Clock clock.Clock
	// StateTTL is how long a flow may take; zero is 10 minutes.
	StateTTL time.Duration
	// MetadataTTL is how long discovered metadata is cached; zero is an
	// hour.
	MetadataTTL time.Duration
	// Logger; nil discards.
	Logger *slog.Logger
}

// Connector connects owners' accounts to MCP servers and hands out their
// tokens. It is safe for concurrent use.
type Connector struct {
	cfg         Config
	http        *http.Client
	clk         clock.Clock
	log         *slog.Logger
	redirectURL string
	cimdURL     string

	mu    sync.Mutex
	cache map[string]*discovered

	refreshes flight[Key, Secret]
	registers flight[[2]string, Client]
}

// New returns a Connector.
func New(cfg Config) (*Connector, error) {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("oauth: PublicURL must be an absolute http(s) URL")
	}
	if cfg.Tokens == nil || cfg.Clients == nil || cfg.States == nil {
		return nil, errors.New("oauth: Tokens, Clients and States are required")
	}
	base := strings.TrimRight(cfg.PublicURL, "/")
	c := &Connector{
		cfg:         cfg,
		http:        cfg.HTTPClient,
		clk:         cfg.Clock,
		log:         cfg.Logger,
		redirectURL: base + CallbackPath,
		cache:       map[string]*discovered{},
	}
	if u.Scheme == "https" {
		c.cimdURL = base + ClientMetadataPath
	}
	if c.http == nil {
		c.http = http.DefaultClient
	}
	if c.clk == nil {
		c.clk = clock.Real()
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.cfg.StateTTL == 0 {
		c.cfg.StateTTL = 10 * time.Minute
	}
	if c.cfg.MetadataTTL == 0 {
		c.cfg.MetadataTTL = time.Hour
	}
	if c.cfg.ClientName == "" {
		c.cfg.ClientName = "agentkit"
	}
	return c, nil
}

// RedirectURL is the redirect URI sent to authorization servers.
func (c *Connector) RedirectURL() string { return c.redirectURL }

// Handler serves the client metadata document at ClientMetadataPath. Mount it
// at the root of PublicURL; it needs no authentication.
func (c *Connector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+ClientMetadataPath, func(w http.ResponseWriter, _ *http.Request) {
		if c.cimdURL == "" {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":                  c.cimdURL,
			"client_name":                c.cfg.ClientName,
			"client_uri":                 strings.TrimRight(c.cfg.PublicURL, "/"),
			"redirect_uris":              []string{c.redirectURL},
			"grant_types":                []string{"authorization_code", "refresh_token"},
			"response_types":             []string{"code"},
			"token_endpoint_auth_method": "none",
			"application_type":           "web",
		})
	})
	return mux
}

// discovered is what discovery found for one MCP server URL.
type discovered struct {
	resource        string
	challengeScopes []string
	resourceScopes  []string
	asm             *oauthex.AuthServerMeta
	expires         time.Time
}

// discover finds srv's authorization server: the 401's resource_metadata,
// else the well-known locations (RFC 9728), then the server's metadata
// (RFC 8414 or OIDC). Results are cached for MetadataTTL; the challenge's
// scopes are not, since they can change per request.
func (c *Connector) discover(ctx context.Context, srv Server) (*discovered, error) {
	c.mu.Lock()
	d, ok := c.cache[srv.URL]
	c.mu.Unlock()
	if ok && c.clk.Now().Before(d.expires) {
		return d, nil
	}

	challenges, err := c.probe(ctx, srv.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	var prm *oauthex.ProtectedResourceMetadata
	for _, cand := range prmURLs(challengeParam(challenges, "resource_metadata"), srv.URL) {
		p, err := oauthex.GetProtectedResourceMetadata(ctx, cand.url, cand.resource, c.http)
		if err != nil || p == nil {
			continue
		}
		prm = p
		break
	}
	if prm == nil {
		return nil, fmt.Errorf("%w: no protected resource metadata for %s", ErrDiscovery, srv.URL)
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("%w: protected resource metadata names no authorization server", ErrDiscovery)
	}
	asm, err := sdkauth.GetAuthServerMetadata(ctx, prm.AuthorizationServers[0], c.http)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	if asm == nil {
		return nil, fmt.Errorf("%w: no metadata at authorization server %s", ErrDiscovery, prm.AuthorizationServers[0])
	}
	d = &discovered{
		resource:       prm.Resource,
		resourceScopes: prm.ScopesSupported,
		asm:            asm,
		expires:        c.clk.Now().Add(c.cfg.MetadataTTL),
	}
	c.mu.Lock()
	c.cache[srv.URL] = d
	c.mu.Unlock()
	cp := *d
	if s := challengeParam(challenges, "scope"); s != "" {
		cp.challengeScopes = strings.Fields(s)
	}
	return &cp, nil
}

// probe sends srv an unauthenticated initialize and returns the
// WWW-Authenticate challenges of its 401 or 403, if any.
func (c *Connector) probe(ctx context.Context, serverURL string) ([]oauthex.Challenge, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"agentkit","version":"0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil, nil
	}
	cs, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return nil, nil // a malformed header: fall back to the well-known locations
	}
	return cs, nil
}

type prmURL struct{ url, resource string }

// prmURLs lists where protected resource metadata may be, in the spec's
// order: the challenge's URL, the well-known URL with the server's path,
// then at the root.
func prmURLs(fromHeader, resource string) []prmURL {
	var out []prmURL
	if fromHeader != "" {
		out = append(out, prmURL{fromHeader, resource})
	}
	u, err := url.Parse(resource)
	if err != nil {
		return out
	}
	m := *u
	m.RawQuery, m.Fragment = "", ""
	if p := strings.Trim(u.Path, "/"); p != "" {
		m.Path = "/.well-known/oauth-protected-resource/" + p
		out = append(out, prmURL{m.String(), resource})
	}
	m.Path = "/.well-known/oauth-protected-resource"
	root := *u
	root.Path, root.RawQuery, root.Fragment = "", "", ""
	return append(out, prmURL{m.String(), root.String()})
}

func challengeParam(cs []oauthex.Challenge, name string) string {
	for _, ch := range cs {
		if ch.Scheme == "bearer" && ch.Params[name] != "" {
			return ch.Params[name]
		}
	}
	return ""
}

// Begin starts connecting owner's account to srv and returns the URL to
// send the user's browser to. returnTo is handed back by Complete, for the
// console to go back to; it is not checked here.
func (c *Connector) Begin(ctx context.Context, owner Owner, srv Server, returnTo string) (string, error) {
	if err := checkServer(srv); err != nil {
		return "", err
	}
	prof := srv.Profile
	now := c.clk.Now()
	p := Pending{
		Owner:    owner,
		Server:   srv.Name,
		ReturnTo: returnTo,
		Created:  now,
		Expires:  now.Add(c.cfg.StateTTL),
		Resource: srv.URL,
		Issuer:   prof.Issuer,
	}
	var scopes []string
	addRefresh := false
	if prof.AuthURL != "" {
		p.AuthURL, p.TokenURL = prof.AuthURL, prof.TokenURL
		scopes = slices.Clone(prof.Scopes)
		addRefresh = true
	} else {
		d, err := c.discover(ctx, srv)
		if err != nil {
			return "", err
		}
		p.AuthURL, p.TokenURL = d.asm.AuthorizationEndpoint, d.asm.TokenEndpoint
		p.Issuer, p.IssParam = d.asm.Issuer, d.asm.AuthorizationResponseIssParameterSupported
		if d.resource != "" {
			p.Resource = d.resource
		}
		switch {
		case len(prof.Scopes) > 0:
			scopes = slices.Clone(prof.Scopes)
		case len(d.challengeScopes) > 0:
			scopes = slices.Clone(d.challengeScopes)
		default:
			scopes = slices.Clone(d.resourceScopes)
		}
		addRefresh = slices.Contains(d.asm.ScopesSupported, prof.refreshScope())
		if err := c.register(ctx, owner, srv, d.asm, &p); err != nil {
			return "", err
		}
	}
	if prof.AuthURL != "" {
		p.ClientID, p.Secret = prof.ClientID, prof.ClientSecret
		p.AuthStyle = int(oauth2.AuthStyleAutoDetect)
	}
	if rs := prof.refreshScope(); addRefresh && rs != "" && !slices.Contains(scopes, rs) {
		scopes = append(scopes, rs)
	}
	p.Scopes = scopes
	if prof.OmitResource {
		p.Resource = ""
	}

	verifier := oauth2.GenerateVerifier()
	p.Verifier = NewSecret(verifier)
	state := rand.Text()
	if err := c.cfg.States.PutState(ctx, state, p); err != nil {
		return "", fmt.Errorf("oauth: saving state: %w", err)
	}

	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if p.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", p.Resource))
	}
	keys := make([]string, 0, len(prof.AuthParams))
	for k := range prof.AuthParams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		opts = append(opts, oauth2.SetAuthURLParam(k, prof.AuthParams[k]))
	}
	return c.config(p).AuthCodeURL(state, opts...), nil
}

func checkServer(srv Server) error {
	if srv.Name == "" {
		return errors.New("oauth: server has no name")
	}
	if u, err := url.Parse(srv.URL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("oauth: server %q: URL must be an absolute http(s) URL", srv.Name)
	}
	if srv.Scope != "" && srv.Scope != ScopeUser && srv.Scope != ScopeTenant {
		return fmt.Errorf("oauth: server %q: unknown scope %q", srv.Name, srv.Scope)
	}
	return srv.Profile.validate()
}

// register picks the client for this authorization server: the profile's
// pre-registered one, else our CIMD URL if the server accepts it, else a
// dynamically registered one, kept per tenant and issuer.
func (c *Connector) register(ctx context.Context, owner Owner, srv Server, asm *oauthex.AuthServerMeta, p *Pending) error {
	prof := srv.Profile
	switch {
	case prof.ClientID != "":
		if prof.Issuer != "" && strings.TrimRight(prof.Issuer, "/") != strings.TrimRight(asm.Issuer, "/") {
			return fmt.Errorf("oauth: server %q: authorization server %q is not the client's issuer", srv.Name, asm.Issuer)
		}
		p.ClientID, p.Secret = prof.ClientID, prof.ClientSecret
		p.AuthStyle = int(authStyle(asm.TokenEndpointAuthMethodsSupported, !prof.ClientSecret.IsZero()))
		return nil
	case c.cimdURL != "" && asm.ClientIDMetadataDocumentSupported:
		p.ClientID = c.cimdURL
		p.AuthStyle = int(oauth2.AuthStyleInParams)
		return nil
	case asm.RegistrationEndpoint != "":
		k := [2]string{owner.Tenant, asm.Issuer}
		cl, err := c.registers.do(ctx, k, func(ctx context.Context) (Client, error) {
			if cl, err := c.cfg.Clients.GetClient(ctx, owner.Tenant, asm.Issuer); err == nil {
				return cl, nil
			} else if !errors.Is(err, ErrNotFound) {
				return Client{}, err
			}
			resp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
				RedirectURIs:    []string{c.redirectURL},
				GrantTypes:      []string{"authorization_code", "refresh_token"},
				ResponseTypes:   []string{"code"},
				ClientName:      c.cfg.ClientName,
				ApplicationType: "web",
			}, c.http)
			if err != nil {
				return Client{}, fmt.Errorf("oauth: dynamic registration at %s: %w", asm.Issuer, err)
			}
			cl := Client{ID: resp.ClientID, Secret: NewSecret(resp.ClientSecret), AuthStyle: int(styleOf(resp.TokenEndpointAuthMethod))}
			if err := c.cfg.Clients.PutClient(ctx, owner.Tenant, asm.Issuer, cl); err != nil {
				return Client{}, err
			}
			return cl, nil
		})
		if err != nil {
			return err
		}
		p.ClientID, p.Secret, p.AuthStyle = cl.ID, cl.Secret, cl.AuthStyle
		return nil
	}
	return fmt.Errorf("%w: server %q", ErrNoRegistration, srv.Name)
}

func authStyle(supported []string, hasSecret bool) oauth2.AuthStyle {
	if !hasSecret {
		return oauth2.AuthStyleInParams
	}
	if slices.Contains(supported, "client_secret_post") && !slices.Contains(supported, "client_secret_basic") {
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

func styleOf(method string) oauth2.AuthStyle {
	switch method {
	case "client_secret_basic":
		return oauth2.AuthStyleInHeader
	case "":
		return oauth2.AuthStyleAutoDetect
	}
	return oauth2.AuthStyleInParams
}

func (c *Connector) config(p Pending) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.Secret.Reveal(),
		Endpoint:     oauth2.Endpoint{AuthURL: p.AuthURL, TokenURL: p.TokenURL, AuthStyle: oauth2.AuthStyle(p.AuthStyle)},
		RedirectURL:  c.redirectURL,
		Scopes:       p.Scopes,
	}
}

// Callback is what the authorization server sent to the redirect URI.
type Callback struct {
	Code, State, Iss string
	// Error is the error code the authorization server sent instead of a
	// code, such as access_denied.
	Error string
}

// CallbackFrom reads a Callback from the redirect's query.
func CallbackFrom(q url.Values) Callback {
	return Callback{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss"), Error: q.Get("error")}
}

// Result is a finished connection.
type Result struct {
	Server   string
	ReturnTo string
}

// Complete finishes a flow Begin started: it checks the state belongs to
// owner and is unused and unexpired, exchanges the code and stores the
// token. lookup returns the server the flow was for, by name, so its profile
// applies to the token.
func (c *Connector) Complete(ctx context.Context, owner Owner, cb Callback, lookup func(name string) (Server, error)) (Result, error) {
	if cb.State == "" {
		return Result{}, ErrBadState
	}
	p, err := c.cfg.States.TakeState(ctx, cb.State)
	if errors.Is(err, ErrNotFound) {
		return Result{}, ErrBadState
	} else if err != nil {
		return Result{}, fmt.Errorf("oauth: reading state: %w", err)
	}
	if !c.clk.Now().Before(p.Expires) || p.Owner != owner {
		return Result{}, ErrBadState
	}
	res := Result{Server: p.Server, ReturnTo: p.ReturnTo}
	if cb.Error != "" {
		return res, fmt.Errorf("%w: %s", ErrDenied, safeCode(cb.Error))
	}
	// RFC 9207: a response from another issuer is a mix-up attack. Without
	// a known issuer (a profile's fixed endpoints) there is nothing to check.
	if (p.IssParam || (cb.Iss != "" && p.Issuer != "")) && cb.Iss != p.Issuer {
		return res, fmt.Errorf("%w: issuer mismatch", ErrBadState)
	}
	if cb.Code == "" {
		return res, fmt.Errorf("%w: no code", ErrDenied)
	}
	srv, err := lookup(p.Server)
	if err != nil {
		return res, fmt.Errorf("oauth: server %q: %w", p.Server, err)
	}
	opts := []oauth2.AuthCodeOption{oauth2.VerifierOption(p.Verifier.Reveal())}
	if p.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", p.Resource))
	}
	tok, err := c.config(p).Exchange(context.WithValue(ctx, oauth2.HTTPClient, c.http), cb.Code, opts...)
	if err != nil {
		return res, fmt.Errorf("oauth: server %q: code exchange: %w", p.Server, tokenError(err))
	}
	t := Token{
		TokenURL:     p.TokenURL,
		ClientID:     p.ClientID,
		ClientSecret: p.Secret,
		AuthStyle:    p.AuthStyle,
		Resource:     p.Resource,
		Issuer:       p.Issuer,
		Scopes:       p.Scopes,
	}
	if err := c.fill(&t, tok, srv.Profile); err != nil {
		return res, err
	}
	if err := c.cfg.Tokens.Put(ctx, Key{owner, p.Server}, t); err != nil {
		return res, fmt.Errorf("oauth: storing token: %w", err)
	}
	c.log.InfoContext(ctx, "mcp oauth: connected", "server", p.Server, "tenant", owner.Tenant, "user", owner.User)
	return res, nil
}

// fill copies a token response into t, applying the profile.
func (c *Connector) fill(t *Token, tok *oauth2.Token, prof Profile) error {
	t.Access = NewSecret(tok.AccessToken)
	if tok.RefreshToken != "" {
		t.Refresh = NewSecret(tok.RefreshToken)
	}
	// The lifetime is counted on our clock, not the one oauth2 reads.
	t.Expiry = time.Time{}
	if tok.ExpiresIn > 0 {
		t.Expiry = c.clk.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	} else if !tok.Expiry.IsZero() {
		t.Expiry = tok.Expiry
	}
	if t.Expiry.IsZero() && prof.DefaultTTL > 0 {
		t.Expiry = c.clk.Now().Add(prof.DefaultTTL)
	}
	if s, ok := tok.Extra("scope").(string); ok && s != "" {
		t.Scopes = strings.Fields(s)
	}
	for _, name := range prof.KeepExtras {
		if v, ok := tok.Extra(name).(string); ok {
			if t.Extra == nil {
				t.Extra = map[string]string{}
			}
			t.Extra[name] = v
		}
	}
	t.ReconnectNeeded = false
	if prof.OnToken != nil {
		if err := prof.OnToken(t); err != nil {
			return fmt.Errorf("oauth: profile refused the token: %w", err)
		}
	}
	return nil
}

// safeCode keeps an error code from the outside to its RFC 6749 charset and
// a short length, so it can be logged and shown.
func safeCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 64 {
			break
		}
		if r >= 0x20 && r <= 0x7e && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tokenError turns an error from a token request into one with no response
// body in it: only the error code, which is never secret.
func tokenError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		code := safeCode(re.ErrorCode)
		if code == "" && re.Response != nil {
			code = fmt.Sprintf("HTTP %d", re.Response.StatusCode)
		}
		return &TokenError{Code: code}
	}
	return err
}

// TokenError is an error answer from a token endpoint.
type TokenError struct{ Code string }

func (e *TokenError) Error() string { return "token endpoint: " + e.Code }

// Token returns owner's access token for srv, refreshing it first if it has
// expired. Concurrent calls for the same connection refresh once. It returns
// [ErrNotConnected] or [ErrReconnectNeeded] when there is no usable
// connection.
func (c *Connector) Token(ctx context.Context, owner Owner, srv Server) (Secret, error) {
	k := Key{owner, srv.Name}
	t, err := c.cfg.Tokens.Get(ctx, k)
	if err != nil {
		return Secret{}, c.missing(srv, err)
	}
	if t.ReconnectNeeded {
		return Secret{}, fmt.Errorf("%s: %w", srv.Name, ErrReconnectNeeded)
	}
	if c.fresh(t) {
		return t.Access, nil
	}
	return c.refresh(ctx, k, srv, Secret{})
}

func (c *Connector) missing(srv Server, err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s: %w", srv.Name, ErrNotConnected)
	}
	return fmt.Errorf("oauth: reading token: %w", err)
}

// expirySkew refreshes a token this long before it expires.
const expirySkew = 30 * time.Second

func (c *Connector) fresh(t Token) bool {
	return t.Expiry.IsZero() || c.clk.Now().Add(expirySkew).Before(t.Expiry)
}

// refresh refreshes k's token once for every concurrent caller. With
// rejected set, the token the server rejected is refreshed even if it looks
// fresh, unless another caller already replaced it.
func (c *Connector) refresh(ctx context.Context, k Key, srv Server, rejected Secret) (Secret, error) {
	return c.refreshes.do(ctx, k, func(ctx context.Context) (Secret, error) {
		t, err := c.cfg.Tokens.Get(ctx, k)
		if err != nil {
			return Secret{}, c.missing(srv, err)
		}
		if t.ReconnectNeeded {
			return Secret{}, fmt.Errorf("%s: %w", srv.Name, ErrReconnectNeeded)
		}
		if rejected.IsZero() && c.fresh(t) {
			return t.Access, nil // refreshed by someone else meanwhile
		}
		if !rejected.IsZero() && t.Access.Reveal() != rejected.Reveal() {
			return t.Access, nil
		}
		if t.Refresh.IsZero() {
			return Secret{}, c.markReconnect(ctx, k, t, srv)
		}
		cfg := &oauth2.Config{
			ClientID:     t.ClientID,
			ClientSecret: t.ClientSecret.Reveal(),
			Endpoint:     oauth2.Endpoint{TokenURL: t.TokenURL, AuthStyle: oauth2.AuthStyle(t.AuthStyle)},
		}
		tok, err := cfg.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, c.http), &oauth2.Token{RefreshToken: t.Refresh.Reveal()}).Token()
		if err != nil {
			var re *oauth2.RetrieveError
			if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
				return Secret{}, c.markReconnect(ctx, k, t, srv)
			}
			return Secret{}, fmt.Errorf("oauth: %s: refresh: %w", srv.Name, tokenError(err))
		}
		nt := cloneToken(t)
		nt.Refresh = t.Refresh // kept unless the server rotates it
		if err := c.fill(&nt, tok, srv.Profile); err != nil {
			return Secret{}, err
		}
		if err := c.cfg.Tokens.Swap(ctx, k, t, nt); errors.Is(err, ErrConflict) {
			// Another replica stored a newer token first: use it.
			cur, err := c.cfg.Tokens.Get(ctx, k)
			if err != nil {
				return Secret{}, c.missing(srv, err)
			}
			if cur.ReconnectNeeded {
				return Secret{}, fmt.Errorf("%s: %w", srv.Name, ErrReconnectNeeded)
			}
			return cur.Access, nil
		} else if err != nil {
			return Secret{}, fmt.Errorf("oauth: storing token: %w", err)
		}
		return nt.Access, nil
	})
}

func (c *Connector) markReconnect(ctx context.Context, k Key, t Token, srv Server) error {
	nt := cloneToken(t)
	nt.ReconnectNeeded = true
	if err := c.cfg.Tokens.Swap(ctx, k, t, nt); err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("oauth: storing token: %w", err)
	}
	c.log.InfoContext(ctx, "mcp oauth: reconnect needed", "server", srv.Name, "tenant", k.Tenant, "user", k.User)
	return fmt.Errorf("%s: %w", srv.Name, ErrReconnectNeeded)
}

// Disconnect forgets owner's connection to srv. It does not revoke the
// token at the provider.
func (c *Connector) Disconnect(ctx context.Context, owner Owner, srv Server) error {
	return c.cfg.Tokens.Delete(ctx, Key{owner, srv.Name})
}

// OAuthHandler returns the handler for the Go SDK's client transport
// (mcp.StreamableClientTransport.OAuthHandler) on owner's connection to srv.
// It adds owner's token to each request. On a 401 it refreshes the rejected
// token once and has the transport retry; if that cannot work, and on a 403
// insufficient_scope, the request fails with [ErrNotConnected],
// [ErrReconnectNeeded] or [ErrNeedsMoreAccess]: a tool error the console
// turns into Connect. It never starts a sign-in.
func (c *Connector) OAuthHandler(owner Owner, srv Server) sdkauth.OAuthHandler {
	return &handler{c: c, owner: owner, srv: srv}
}

type handler struct {
	c     *Connector
	owner Owner
	srv   Server
}

func (h *handler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	s, err := h.c.Token(ctx, h.owner, h.srv)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrReconnectNeeded) {
		return nil, nil // send without a token: the 401 reaches Authorize
	}
	if err != nil {
		return nil, err
	}
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: s.Reveal(), TokenType: "Bearer"}), nil
}

func (h *handler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	cs, _ := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if resp.StatusCode == http.StatusForbidden {
		if challengeParam(cs, "error") == "insufficient_scope" {
			return fmt.Errorf("%s: %w", h.srv.Name, ErrNeedsMoreAccess)
		}
		return fmt.Errorf("%s: server refused the request (HTTP 403)", h.srv.Name)
	}
	sent, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		// Sent without a token: there is no usable connection.
		_, err := h.c.Token(ctx, h.owner, h.srv)
		if err == nil {
			return nil // connected meanwhile: retry
		}
		return err
	}
	_, err := h.c.refresh(ctx, Key{h.owner, h.srv.Name}, h.srv, NewSecret(sent))
	return err // nil: retry with the new token
}

// flight runs one call per key at a time and gives its result to every
// caller that asked meanwhile. The call runs on a context that outlives the
// first caller's cancellation (bounded by flightTimeout), so one caller
// giving up does not fail the others.
type flight[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*flightCall[V]
}

type flightCall[V any] struct {
	done chan struct{}
	v    V
	err  error
}

const flightTimeout = 30 * time.Second

func (f *flight[K, V]) do(ctx context.Context, k K, fn func(context.Context) (V, error)) (V, error) {
	f.mu.Lock()
	call, ok := f.calls[k]
	if !ok {
		if f.calls == nil {
			f.calls = map[K]*flightCall[V]{}
		}
		call = &flightCall[V]{done: make(chan struct{})}
		f.calls[k] = call
		go func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
			defer cancel()
			call.v, call.err = fn(cctx)
			f.mu.Lock()
			delete(f.calls, k)
			f.mu.Unlock()
			close(call.done)
		}()
	}
	f.mu.Unlock()
	select {
	case <-call.done:
		return call.v, call.err
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	}
}
