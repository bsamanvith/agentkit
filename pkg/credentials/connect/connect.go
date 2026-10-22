package connect

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// DefaultStateTTL is how long a connect flow may take, from Connect to the
// callback.
const DefaultStateTTL = 10 * time.Minute

// CallbackPath is where [Service.Callback] is mounted, under the server's
// public URL: the redirect URI an App names ends with it.
const CallbackPath = "/oauth/callback"

// Slot is a credential slot the server's toolsets use, with the scope they
// use it with. A slot used with both scopes is two Slots.
type Slot struct {
	Name  string
	Scope credentials.Scope
	// Toolsets are the toolsets that use it so, for listing.
	Toolsets []string
}

// App is the OAuth2 client a slot's accounts are connected with: the
// authorization code flow with PKCE (S256).
type App struct {
	ClientID     string
	ClientSecret credentials.Secret
	// AuthURL and TokenURL are the provider's endpoints; RedirectURL is the
	// server's callback ([CallbackPath]) as registered with the provider.
	AuthURL, TokenURL, RedirectURL string
	// Scopes are asked for.
	Scopes []string
	// AuthStyle is how the client authenticates to the token endpoint.
	AuthStyle credentials.AuthStyle
	// AuthParams are added to the authorization URL, such as Google's
	// access_type=offline and prompt=consent (a provider profile's).
	AuthParams map[string]string
}

// Config configures [New].
type Config struct {
	// Vault stores the credentials. Store and Sealer are the vault's
	// BlobStore and Sealer: pending flows are kept there, sealed, under a
	// reserved tenant, so that every replica sharing the vault can finish
	// a flow.
	Vault  credentials.Vault
	Store  credentials.BlobStore
	Sealer *credentials.Sealer
	// Slots are the slots the server knows; requests about any other are
	// refused.
	Slots []Slot
	// Apps are the OAuth2 clients of the slots that can be connected, by
	// slot.
	Apps map[string]App
	// ReturnURLs are the URLs a flow may return the browser to: a
	// return URL must have the scheme, host and path of one of them (its
	// query may differ). Empty allows none.
	ReturnURLs []string
	// CheckTokenURL checks the token URL of a client credentials
	// credential before it is stored: the server sends the client secret
	// there, so give the SSRF guard's check (netguard.Guard.CheckURL). Nil
	// requires https only.
	CheckTokenURL func(raw string) error
	// HTTPClient makes the code exchange (nil: http.DefaultClient's
	// transport). Redirects are never followed: they would carry the code
	// and the client secret elsewhere.
	HTTPClient *http.Client
	// Clock dates flows and tokens (nil: the real clock); StateTTL is how
	// long a flow may take (zero: [DefaultStateTTL]).
	Clock    clock.Clock
	StateTTL time.Duration
	// Logger gets each connection and failure, never a value (nil: none).
	Logger *slog.Logger
}

// Service is the agent API's credentials ([agentapi.Credentials]) and the
// OAuth callback ([Service.Callback]). It is safe for concurrent use.
type Service struct {
	cfg    Config
	slots  map[string][]credentials.Scope
	sets   map[slotScope][]string
	allow  []*url.URL
	flows  *flows
	http   *http.Client
	clk    clock.Clock
	log    *slog.Logger
	ttl    time.Duration
	random func([]byte) // fills with random bytes (crypto/rand)
}

type slotScope struct {
	slot  string
	scope credentials.Scope
}

var _ agentapi.Credentials = (*Service)(nil)

// New returns the service, checking the config.
func New(cfg Config) (*Service, error) {
	if cfg.Vault == nil || cfg.Store == nil || cfg.Sealer == nil {
		return nil, errors.New("connect: needs the vault, its BlobStore and its Sealer")
	}
	s := &Service{
		cfg: cfg, slots: map[string][]credentials.Scope{}, sets: map[slotScope][]string{},
		clk: cmp.Or[clock.Clock](cfg.Clock, clock.Real()), log: cfg.Logger, ttl: cmp.Or(cfg.StateTTL, DefaultStateTTL),
		random: func(b []byte) { _, _ = rand.Read(b) },
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.ttl < 0 {
		return nil, errors.New("connect: negative state TTL")
	}
	for _, sl := range cfg.Slots {
		scope := cmp.Or(sl.Scope, credentials.ScopeTenant)
		if err := credentials.CheckSlot(sl.Name); err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		if err := credentials.CheckScope(scope); err != nil {
			return nil, fmt.Errorf("connect: slot %q: %w", sl.Name, err)
		}
		if strings.HasPrefix(sl.Name, "_") {
			return nil, fmt.Errorf("connect: slot %q: names starting with _ are reserved", sl.Name)
		}
		if !slices.Contains(s.slots[sl.Name], scope) {
			s.slots[sl.Name] = append(s.slots[sl.Name], scope)
		}
		k := slotScope{sl.Name, scope}
		for _, ts := range sl.Toolsets {
			if !slices.Contains(s.sets[k], ts) {
				s.sets[k] = append(s.sets[k], ts)
			}
		}
	}
	for slot, app := range cfg.Apps {
		if s.slots[slot] == nil {
			return nil, fmt.Errorf("connect: OAuth2 app of slot %q: no toolset uses the slot", slot)
		}
		if err := checkApp(app); err != nil {
			return nil, fmt.Errorf("connect: OAuth2 app of slot %q: %w", slot, err)
		}
	}
	for _, raw := range cfg.ReturnURLs {
		u, err := parseReturnURL(raw)
		if err != nil {
			return nil, fmt.Errorf("connect: return URL %q: %w", raw, err)
		}
		s.allow = append(s.allow, u)
	}
	base := cmp.Or(cfg.HTTPClient, http.DefaultClient)
	hc := *base
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if hc.Timeout == 0 {
		hc.Timeout = credentials.DefaultTokenTimeout
	}
	s.http = &hc
	var err error
	if s.flows, err = newFlows(cfg.Store, cfg.Sealer); err != nil {
		return nil, err
	}
	return s, nil
}

func checkApp(a App) error {
	if a.ClientID == "" || a.ClientSecret.IsZero() {
		return errors.New("needs a client id and secret")
	}
	for name, raw := range map[string]string{"auth URL": a.AuthURL, "token URL": a.TokenURL, "redirect URL": a.RedirectURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("%s %q: want an absolute http(s) URL without user info or fragment", name, raw)
		}
	}
	if a.AuthStyle != credentials.AuthInHeader && a.AuthStyle != credentials.AuthInParams {
		return fmt.Errorf("unknown auth style %d", a.AuthStyle)
	}
	return nil
}

// parseReturnURL parses an absolute http(s) URL with no user info and no
// fragment.
func parseReturnURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, errors.New("not a URL")
	case u.Scheme != "https" && u.Scheme != "http", u.Host == "", u.Opaque != "":
		return nil, errors.New("not an absolute http(s) URL")
	case u.User != nil, u.Fragment != "" || strings.Contains(raw, "#"):
		return nil, errors.New("has user info or a fragment")
	}
	return u, nil
}

// allowed reports whether raw is on the return URL allowlist: the same
// scheme, host and path as one entry.
func (s *Service) allowed(raw string) bool {
	u, err := parseReturnURL(raw)
	if err != nil {
		return false
	}
	for _, a := range s.allow {
		if a.Scheme == u.Scheme && strings.EqualFold(a.Host, u.Host) && a.EscapedPath() == u.EscapedPath() {
			return true
		}
	}
	return false
}

// Scopes implements [agentapi.Credentials].
func (s *Service) Scopes(slot string) []string {
	var out []string
	for _, sc := range s.slots[slot] {
		out = append(out, string(sc))
	}
	return out
}

// scopeOf is the scope of a request whose user is user.
func scopeOf(user string) credentials.Scope {
	if user == "" {
		return credentials.ScopeTenant
	}
	return credentials.ScopeUser
}

// info is what listing says of slot for user, given the stored metadata
// (nil for none).
func (s *Service) info(slot string, scope credentials.Scope, m *credentials.Meta) wire.CredentialInfo {
	_, oauth := s.cfg.Apps[slot]
	v := wire.CredentialInfo{
		Slot: slot, Scope: string(scope), Status: wire.CredentialNotConnected, OAuth: oauth,
		Toolsets: slices.Clone(s.sets[slotScope{slot, scope}]),
	}
	if v.Toolsets == nil {
		v.Toolsets = []string{}
	}
	if m == nil {
		return v
	}
	v.Status = wire.CredentialConnected
	if m.NeedsReconnect {
		v.Status = wire.CredentialNeedsReconnect
	}
	v.Kind, v.Version, v.CreatedAt, v.UpdatedAt, v.Expiry = string(m.Kind), m.Version, m.CreatedAt, m.UpdatedAt, m.Expiry
	return v
}

// metas lists user's stored credentials by slot.
func (s *Service) metas(ctx context.Context, user string) (map[string]credentials.Meta, error) {
	list, err := s.cfg.Vault.List(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make(map[string]credentials.Meta, len(list))
	for _, m := range list {
		out[m.Slot] = m
	}
	return out, nil
}

// notReserved refuses a request in [FlowTenant], where the flows are kept:
// no principal acts as it, whatever its API key, token or header says.
func notReserved(ctx context.Context) error {
	if t, ok := tenancy.From(ctx); ok && t.ID() == FlowTenant {
		return fmt.Errorf("%w: the tenant %q is reserved for the connect flows", agentapi.ErrCredentialForbidden, FlowTenant)
	}
	return nil
}

// List implements [agentapi.Credentials].
func (s *Service) List(ctx context.Context, user string) ([]wire.CredentialInfo, error) {
	if err := notReserved(ctx); err != nil {
		return nil, err
	}
	stored := map[credentials.Scope]map[string]credentials.Meta{}
	var err error
	if stored[credentials.ScopeTenant], err = s.metas(ctx, ""); err != nil {
		return nil, err
	}
	if user != "" {
		if stored[credentials.ScopeUser], err = s.metas(ctx, user); err != nil {
			return nil, err
		}
	}
	out := []wire.CredentialInfo{}
	for _, slot := range slices.Sorted(maps.Keys(s.slots)) {
		for _, scope := range []credentials.Scope{credentials.ScopeTenant, credentials.ScopeUser} {
			if !slices.Contains(s.slots[slot], scope) || (scope == credentials.ScopeUser && user == "") {
				continue
			}
			var mp *credentials.Meta
			if m, ok := stored[scope][slot]; ok {
				mp = &m
			}
			out = append(out, s.info(slot, scope, mp))
		}
	}
	return out, nil
}

// Status implements [agentapi.Credentials].
func (s *Service) Status(ctx context.Context, user, slot string) (string, error) {
	if err := notReserved(ctx); err != nil {
		return "", err
	}
	ms, err := s.metas(ctx, user)
	if err != nil {
		return "", err
	}
	m, ok := ms[slot]
	if !ok {
		return wire.CredentialNotConnected, nil
	}
	return s.info(slot, scopeOf(user), &m).Status, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{agentapi.ErrCredentialInvalid}, args...)...)
}

// credential is the credential a PUT asks to store, checked.
func (s *Service) credential(req wire.PutCredentialRequest) (credentials.Credential, error) {
	set := func(fields ...string) []string {
		var out []string
		for i := 0; i < len(fields); i += 2 {
			if fields[i+1] != "" {
				out = append(out, fields[i])
			}
		}
		return out
	}
	other := func(kind string, allowed ...string) error {
		all := set("api_key", req.APIKey, "username", req.Username, "password", req.Password,
			"client_id", req.ClientID, "client_secret", req.ClientSecret, "token_url", req.TokenURL)
		if len(req.Scopes) > 0 {
			all = append(all, "scopes")
		}
		for _, f := range all {
			if !slices.Contains(allowed, f) {
				return invalid("kind %q takes no %s", kind, f)
			}
		}
		return nil
	}
	switch req.Kind {
	case wire.KindAPIKey:
		if err := other(req.Kind, "api_key"); err != nil {
			return nil, err
		}
		if req.APIKey == "" {
			return nil, invalid("kind %q needs api_key", req.Kind)
		}
		return credentials.APIKey{Key: credentials.NewSecret(req.APIKey)}, nil
	case wire.KindBasic:
		if err := other(req.Kind, "username", "password"); err != nil {
			return nil, err
		}
		if req.Username == "" && req.Password == "" {
			return nil, invalid("kind %q needs username or password", req.Kind)
		}
		return credentials.Basic{User: req.Username, Password: credentials.NewSecret(req.Password)}, nil
	case wire.KindClientCredentials:
		if err := other(req.Kind, "client_id", "client_secret", "token_url", "scopes"); err != nil {
			return nil, err
		}
		if req.ClientID == "" || req.ClientSecret == "" || req.TokenURL == "" {
			return nil, invalid("kind %q needs client_id, client_secret and token_url", req.Kind)
		}
		if err := s.checkTokenURL(req.TokenURL); err != nil {
			return nil, invalid("token_url: %v", err)
		}
		return credentials.ClientCredentials{
			ClientID: req.ClientID, ClientSecret: credentials.NewSecret(req.ClientSecret),
			TokenURL: req.TokenURL, Scopes: slices.Clone(req.Scopes),
		}, nil
	case wire.KindOAuth2:
		return nil, invalid("OAuth2 tokens come from connecting the account (POST /v1/credentials/{slot}/connect)")
	}
	return nil, invalid("kind %q: want %q, %q or %q", req.Kind, wire.KindAPIKey, wire.KindBasic, wire.KindClientCredentials)
}

// checkTokenURL checks a stored client credentials token URL: https, and
// the configured check (the SSRF guard). Its error never holds the URL's
// user info.
func (s *Service) checkTokenURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("want an https URL with a host and no user info")
	}
	if s.cfg.CheckTokenURL != nil {
		if err := s.cfg.CheckTokenURL(raw); err != nil {
			return errors.New("the server does not send credentials to that address")
		}
	}
	return nil
}

// Put implements [agentapi.Credentials].
func (s *Service) Put(ctx context.Context, user, slot string, req wire.PutCredentialRequest) (wire.CredentialInfo, error) {
	if err := notReserved(ctx); err != nil {
		return wire.CredentialInfo{}, err
	}
	cred, err := s.credential(req)
	if err != nil {
		return wire.CredentialInfo{}, err
	}
	m, err := s.store(ctx, user, slot, cred)
	if err != nil {
		return wire.CredentialInfo{}, err
	}
	return s.info(slot, scopeOf(user), &m), nil
}

// store writes cred at (user, slot), replacing what is there: it reads
// the current version and writes over it, again if another writer came
// between.
func (s *Service) store(ctx context.Context, user, slot string, cred credentials.Credential) (credentials.Meta, error) {
	var last error
	for range 5 {
		ms, err := s.metas(ctx, user)
		if err != nil {
			return credentials.Meta{}, err
		}
		m, err := s.cfg.Vault.Put(ctx, user, slot, cred, ms[slot].Version)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, credentials.ErrConflict) && !errors.Is(err, credentials.ErrExists) {
			return credentials.Meta{}, err
		}
		last = err
	}
	return credentials.Meta{}, fmt.Errorf("storing credential %q: written by others meanwhile, five times: %w", slot, last)
}

// Delete implements [agentapi.Credentials].
func (s *Service) Delete(ctx context.Context, user, slot string) error {
	if err := notReserved(ctx); err != nil {
		return err
	}
	err := s.cfg.Vault.Delete(ctx, user, slot, 0)
	if errors.Is(err, credentials.ErrNotFound) {
		return fmt.Errorf("%w: nothing is stored at slot %q", agentapi.ErrCredentialNotFound, slot)
	}
	return err
}

// Connect implements [agentapi.Credentials]: it keeps a new flow under a
// new state and returns the provider's authorization URL for it, and the
// cookie that binds the flow to the browser that started it (RFC 9700
// §4.7). The cookie is random (256 bits), HttpOnly, SameSite=Lax, limited
// to the callback's path and the flow's lifetime; the flow keeps only its
// SHA-256, and the callback refuses a browser without it. The API sets it
// Secure when the request came over TLS.
func (s *Service) Connect(ctx context.Context, user, slot string, req wire.ConnectRequest) (wire.ConnectResponse, *http.Cookie, error) {
	app, ok := s.cfg.Apps[slot]
	if !ok {
		return wire.ConnectResponse{}, nil, invalid("slot %q has no OAuth2 app to connect an account with (credentials.oauth2)", slot)
	}
	if req.ReturnURL != "" && !s.allowed(req.ReturnURL) {
		return wire.ConnectResponse{}, nil, invalid("return_url is not one this server returns to")
	}
	t, err := tenancy.Require(ctx)
	if err != nil {
		return wire.ConnectResponse{}, nil, err
	}
	if err := notReserved(ctx); err != nil {
		return wire.ConnectResponse{}, nil, err
	}
	b := make([]byte, 32) // 256 bits
	s.random(b)
	state := newState(t.ID(), user, b)
	s.random(b)
	binding := base64.RawURLEncoding.EncodeToString(b)
	clear(b)
	verifier := oauth2.GenerateVerifier()
	now := s.clk.Now().UTC()
	f := flow{
		Tenant: t.ID(), User: user, Slot: slot, Verifier: verifier, ReturnURL: req.ReturnURL,
		Binding: bindingHash(binding), CookiePath: cookiePath(app.RedirectURL),
		Created: now, Expires: now.Add(s.ttl),
	}
	if err := s.flows.put(ctx, state, f); err != nil {
		return wire.ConnectResponse{}, nil, fmt.Errorf("keeping the flow: %w", err)
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	for _, k := range slices.Sorted(maps.Keys(app.AuthParams)) {
		opts = append(opts, oauth2.SetAuthURLParam(k, app.AuthParams[k]))
	}
	cookie := &http.Cookie{
		Name: CookieName(state), Value: binding, Path: f.CookiePath,
		MaxAge: int(s.ttl / time.Second), HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	s.log.InfoContext(ctx, "connect flow started", "slot", slot, "tenant", f.Tenant, "user", user)
	return wire.ConnectResponse{AuthorizeURL: oauthConfig(app).AuthCodeURL(state, opts...), ExpiresAt: f.Expires}, cookie, nil
}

// CookieName is the name of the cookie that binds the flow of state to its
// browser: one per flow, so that flows started at once in one browser do
// not replace each other's.
func CookieName(state string) string {
	sum := sha256.Sum256([]byte("agentkit oauth cookie\x00" + state))
	return cookiePrefix + hex.EncodeToString(sum[:8])
}

// cookiePrefix starts the name of every binding cookie.
const cookiePrefix = "agentkit_oauth_"

// cookiePath is the path of the redirect URL, which the binding cookie is
// limited to.
func cookiePath(redirectURL string) string {
	u, err := url.Parse(redirectURL)
	if err != nil || u.EscapedPath() == "" {
		return CallbackPath
	}
	return u.EscapedPath()
}

// bindingHash is what a flow keeps of its binding cookie's value.
func bindingHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func oauthConfig(a App) *oauth2.Config {
	style := oauth2.AuthStyleInHeader
	if a.AuthStyle == credentials.AuthInParams {
		style = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID: a.ClientID, ClientSecret: a.ClientSecret.Reveal(), RedirectURL: a.RedirectURL, Scopes: slices.Clone(a.Scopes),
		Endpoint: oauth2.Endpoint{AuthURL: a.AuthURL, TokenURL: a.TokenURL, AuthStyle: style},
	}
}
