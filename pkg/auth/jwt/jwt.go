package jwt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// KindJWT is the auth.Credential Kind of a principal authenticated by a
// token. Its ID is the issuer.
const KindJWT = "jwt"

// Defaults of [Config].
const (
	DefaultLeeway             = 60 * time.Second
	DefaultMinRefreshInterval = time.Minute
	DefaultMaxCacheAge        = time.Hour
	DefaultTenantClaim        = "tenant"
	DefaultUserClaim          = "sub"
	DefaultToolsetScopePrefix = "mcp:"
)

// DefaultAlgorithms are the signature algorithms allowed when
// [Config.Algorithms] is empty.
var DefaultAlgorithms = []string{"RS256", "ES256"}

// supported are the algorithms a configuration may allow: asymmetric ones
// only. "none" and the HMACs (HS256, ...) are refused: with an HMAC, a public
// key's bytes would be the secret (RFC 8725 §2.1, §3.1).
var supported = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

// Config configures an [Authenticator].
type Config struct {
	// Issuer is the identity provider's issuer URL, compared exactly with the
	// "iss" claim. It must be https, or http on a loopback host.
	Issuer string `json:"issuer"`
	// Audience is this server's resource URL, such as
	// "https://agents.example.com". A token's "aud" must name it, or a URL
	// under it that holds the requested path ("https://agents.example.com/mcp/crm"
	// for a request to /mcp/crm).
	Audience string `json:"audience"`
	// JWKSURL is where the provider publishes its keys. Empty means the
	// jwks_uri of {Issuer}/.well-known/openid-configuration.
	JWKSURL string `json:"jwks_url,omitempty"`
	// Algorithms are the signature algorithms accepted (default
	// [DefaultAlgorithms]): any of RS256, RS384, RS512, PS256, PS384, PS512,
	// ES256, ES384, ES512 and EdDSA.
	Algorithms []string `json:"algorithms,omitempty"`
	// Leeway is how far "exp", "nbf" and "iat" may be off, for clock skew
	// (default [DefaultLeeway]; at most 5 minutes).
	Leeway time.Duration `json:"-"`

	// Tenant, when set, is the tenant of every token, and no claim is read
	// for it: for a provider that serves one tenant.
	Tenant string `json:"tenant,omitempty"`
	// TenantClaim is the claim holding the tenant id (default "tenant").
	TenantClaim string `json:"tenant_claim,omitempty"`
	// UserClaim is the claim holding the user id (default "sub"). A token
	// without it has no user.
	UserClaim string `json:"user_claim,omitempty"`
	// ScopesClaim is the claim holding the scopes, a space-separated string
	// or an array of strings. Empty reads both "scope" and "scp".
	ScopesClaim string `json:"scopes_claim,omitempty"`
	// ToolsetsClaim is a claim holding the toolsets, a space-separated
	// string or an array of strings. Empty reads none.
	ToolsetsClaim string `json:"toolsets_claim,omitempty"`
	// ToolsetScopePrefix makes each scope "<prefix><toolset>" grant that
	// toolset (default "mcp:").
	ToolsetScopePrefix string `json:"toolset_scope_prefix,omitempty"`

	// MinRefreshInterval is the least time between two fetches of the keys
	// (default [DefaultMinRefreshInterval]; at least a second).
	MinRefreshInterval time.Duration `json:"-"`
	// MaxCacheAge is the longest the keys are used before they are fetched
	// again, whatever the provider's cache headers say (default
	// [DefaultMaxCacheAge]).
	MaxCacheAge time.Duration `json:"-"`
}

// Option configures an [Authenticator].
type Option func(*Authenticator)

// WithClock sets the clock that token times and the key cache read (default
// clock.Real).
func WithClock(c clock.Clock) Option { return func(a *Authenticator) { a.clock = c } }

// WithHTTPClient sets the client that fetches the keys and the OpenID
// configuration (default: a client with a 10 s timeout). Fetches never
// follow the token: the URLs are the configuration's.
func WithHTTPClient(c *http.Client) Option { return func(a *Authenticator) { a.keys.client = c } }

// WithLogger sets the logger (default: discard). Failed key fetches are
// logged at warn; tokens never are.
func WithLogger(l *slog.Logger) Option { return func(a *Authenticator) { a.log = l } }

// Authenticator is an auth.Authenticator of JWT bearer tokens. Make one with
// [New]. It is safe for concurrent use.
type Authenticator struct {
	cfg   Config
	algs  []jose.SignatureAlgorithm
	clock clock.Clock
	log   *slog.Logger
	keys  *keyCache
}

// ConfigError is a configuration [New] refused.
type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return "auth/jwt: " + e.Err.Error() }

func (e *ConfigError) Unwrap() error { return e.Err }

// New returns an Authenticator for cfg. It fetches nothing: the keys are
// fetched on the first token (or by [Authenticator.Refresh]).
func New(cfg Config, opts ...Option) (*Authenticator, error) {
	bad := func(format string, args ...any) (*Authenticator, error) {
		return nil, &ConfigError{fmt.Errorf(format, args...)}
	}
	cfg.Algorithms = slices.Clone(cfg.Algorithms)
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = slices.Clone(DefaultAlgorithms)
	}
	setDefault(&cfg.Leeway, DefaultLeeway)
	setDefault(&cfg.MinRefreshInterval, DefaultMinRefreshInterval)
	setDefault(&cfg.MaxCacheAge, DefaultMaxCacheAge)
	setDefault(&cfg.TenantClaim, DefaultTenantClaim)
	setDefault(&cfg.UserClaim, DefaultUserClaim)
	setDefault(&cfg.ToolsetScopePrefix, DefaultToolsetScopePrefix)

	if err := checkURL(cfg.Issuer); err != nil {
		return bad("issuer: %w", err)
	}
	if cfg.Audience == "" {
		return bad("audience: missing")
	}
	if cfg.JWKSURL != "" {
		if err := checkURL(cfg.JWKSURL); err != nil {
			return bad("jwks_url: %w", err)
		}
	}
	var algs []jose.SignatureAlgorithm
	for _, a := range cfg.Algorithms {
		if !slices.Contains(supported, a) {
			return bad("algorithm %q is not allowed: use asymmetric ones (%s)", a, strings.Join(supported, ", "))
		}
		algs = append(algs, jose.SignatureAlgorithm(a))
	}
	switch {
	case cfg.Leeway < 0 || cfg.Leeway > 5*time.Minute:
		return bad("leeway %v: want 0 to 5m", cfg.Leeway)
	case cfg.MinRefreshInterval < time.Second:
		return bad("min_refresh_interval %v: want at least 1s", cfg.MinRefreshInterval)
	case cfg.MaxCacheAge < cfg.MinRefreshInterval:
		return bad("max_cache_age %v is under min_refresh_interval %v", cfg.MaxCacheAge, cfg.MinRefreshInterval)
	}
	if cfg.Tenant != "" {
		if _, err := tenancy.NewTenant(cfg.Tenant); err != nil {
			return bad("tenant: %w", err)
		}
	}
	a := &Authenticator{
		cfg: cfg, algs: algs,
		clock: clock.Real(), log: slog.New(slog.DiscardHandler),
		keys: &keyCache{client: &http.Client{Timeout: fetchTimeout}},
	}
	for _, o := range opts {
		o(a)
	}
	a.keys.init(a)
	return a, nil
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}

// checkURL accepts an absolute https URL, or http on a loopback host (a
// provider on the same machine, a test server).
func checkURL(s string) error {
	u, err := url.Parse(s)
	switch {
	case s == "":
		return errors.New("missing")
	case err != nil:
		return errors.New("not a URL")
	case u.Host == "" || u.User != nil:
		return errors.New("want an absolute URL with a host and no user info")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return errors.New("want https (http only on a loopback host)")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Refresh fetches the keys now, unless they were fetched less than
// MinRefreshInterval ago. Serve calls it at startup to find a wrong URL
// early; a failure is not fatal, as the next token tries again.
func (a *Authenticator) Refresh(ctx context.Context) error { return a.keys.refresh(ctx) }

// Authenticate returns the principal of the request's bearer token. See the
// package documentation for what is checked.
func (a *Authenticator) Authenticate(r *http.Request) (auth.Principal, error) {
	tok, err := auth.BearerToken(r.Header)
	if err != nil {
		return auth.Principal{}, err
	}
	if !jwtShaped(tok) {
		return auth.Principal{}, fmt.Errorf("%w: the bearer token is not a JWT", auth.ErrNoCredentials)
	}
	p, err := a.verify(r.Context(), tok, r.URL.Path)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %w", auth.ErrInvalidCredentials, err)
	}
	return p, nil
}

// jwtShaped reports whether tok is three base64url segments, the first not
// empty: the compact form of a JWS. An API key never is.
func jwtShaped(tok string) bool {
	if strings.Count(tok, ".") != 2 || tok[0] == '.' {
		return false
	}
	for i := range len(tok) {
		if !base64URLOrDot(tok[i]) {
			return false
		}
	}
	return true
}

func base64URLOrDot(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// verify checks tok and maps its claims to a principal. Its errors name the
// check that failed, never a value from the token.
func (a *Authenticator) verify(ctx context.Context, tok, reqPath string) (auth.Principal, error) {
	jws, err := jose.ParseSignedCompact(tok, a.algs)
	if err != nil {
		return auth.Principal{}, errors.New("malformed, or an algorithm that is not allowed")
	}
	if len(jws.Signatures) != 1 {
		return auth.Principal{}, errors.New("not one signature")
	}
	h := jws.Signatures[0].Header
	payload, err := a.keys.verify(ctx, jws, h.KeyID, h.Algorithm)
	if err != nil {
		return auth.Principal{}, err
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return auth.Principal{}, errors.New("the claims are not a JSON object")
	}
	if err := a.checkClaims(c, reqPath); err != nil {
		return auth.Principal{}, err
	}
	return a.principal(c)
}

// claims are a token's claims, each kept raw until read.
type claims map[string]json.RawMessage

// str reads a string claim: "" and false when absent, an error when not a
// string.
func (c claims) str(name string) (string, bool, error) {
	raw, ok := c[name]
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, fmt.Errorf("claim %q is not a string", name)
	}
	return s, true, nil
}

// list reads a claim that is a string of space-separated values or an array
// of strings.
func (c claims) list(name string) ([]string, error) {
	raw, ok := c[name]
	if !ok {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.Fields(s), nil
	}
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("claim %q is neither a string nor an array of strings", name)
	}
	return l, nil
}

// time reads a NumericDate claim (seconds since the epoch, maybe with a
// fraction).
func (c claims) time(name string) (time.Time, bool, error) {
	raw, ok := c[name]
	if !ok {
		return time.Time{}, false, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || math.IsNaN(f) || math.Abs(f) > 1<<40 {
		return time.Time{}, false, fmt.Errorf("claim %q is not a date", name)
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), true, nil
}

func (a *Authenticator) checkClaims(c claims, reqPath string) error {
	iss, _, err := c.str("iss")
	if err != nil {
		return err
	}
	if iss != a.cfg.Issuer {
		return errors.New("wrong issuer")
	}
	aud, err := c.list("aud")
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(aud, func(v string) bool { return a.covers(v, reqPath) }) {
		return errors.New("not issued for this resource (aud)")
	}

	now, leeway := a.clock.Now(), a.cfg.Leeway
	exp, ok, err := c.time("exp")
	switch {
	case err != nil:
		return err
	case !ok:
		return errors.New("no expiry (exp)")
	case !now.Before(exp.Add(leeway)):
		return errors.New("expired")
	}
	nbf, ok, err := c.time("nbf")
	switch {
	case err != nil:
		return err
	case ok && now.Before(nbf.Add(-leeway)):
		return errors.New("not valid yet (nbf)")
	}
	iat, ok, err := c.time("iat")
	switch {
	case err != nil:
		return err
	case ok && iat.After(now.Add(leeway)):
		return errors.New("issued in the future (iat)")
	}
	return nil
}

// covers reports whether the audience value aud names this resource: the
// configured audience, or a URL under it whose path holds reqPath (RFC 8707
// resource indicators: a client asks for the MCP endpoint's own URL).
func (a *Authenticator) covers(aud, reqPath string) bool {
	base := a.cfg.Audience
	if aud == base {
		return true
	}
	prefix := strings.TrimSuffix(base, "/") + "/"
	if !strings.HasPrefix(aud, prefix) {
		return false
	}
	u, err := url.Parse(aud)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return false
	}
	// Only a clean request path is under it: "/mcp/crm/../gmail" starts
	// with "/mcp/crm/" as text, not as a path.
	if c := path.Clean(reqPath); c != reqPath && c != strings.TrimSuffix(reqPath, "/") {
		return false
	}
	p := strings.TrimSuffix(u.Path, "/")
	return reqPath == p || strings.HasPrefix(reqPath, p+"/")
}

func (a *Authenticator) principal(c claims) (auth.Principal, error) {
	p := auth.Principal{Via: auth.Credential{Kind: KindJWT, ID: a.cfg.Issuer}}

	tenant := a.cfg.Tenant
	if tenant == "" {
		var ok bool
		var err error
		if tenant, ok, err = c.str(a.cfg.TenantClaim); err != nil {
			return auth.Principal{}, err
		} else if !ok {
			return auth.Principal{}, fmt.Errorf("no tenant claim %q", a.cfg.TenantClaim)
		}
	}
	t, err := tenancy.NewTenant(tenant)
	if err != nil {
		return auth.Principal{}, errors.New("the tenant is not a valid tenant id")
	}
	p.Tenant = t

	user, _, err := c.str(a.cfg.UserClaim)
	if err != nil {
		return auth.Principal{}, err
	}
	if !validUser(user) {
		return auth.Principal{}, errors.New("the user is not a valid user id")
	}
	p.User = user

	scopeClaims := []string{"scope", "scp"}
	if a.cfg.ScopesClaim != "" {
		scopeClaims = []string{a.cfg.ScopesClaim}
	}
	for _, name := range scopeClaims {
		l, err := c.list(name)
		if err != nil {
			return auth.Principal{}, err
		}
		p.Scopes = appendNames(p.Scopes, l...)
	}
	for _, s := range p.Scopes {
		if ts, ok := strings.CutPrefix(s, a.cfg.ToolsetScopePrefix); ok {
			p.Toolsets = appendNames(p.Toolsets, ts)
		}
	}
	if a.cfg.ToolsetsClaim != "" {
		l, err := c.list(a.cfg.ToolsetsClaim)
		if err != nil {
			return auth.Principal{}, err
		}
		p.Toolsets = appendNames(p.Toolsets, l...)
	}
	return p, nil
}

// appendNames appends each of vs that is a name and not yet in dst. Values
// with path.Match metacharacters are dropped: a principal's scopes and
// toolsets are patterns, and a token grants names.
func appendNames(dst []string, vs ...string) []string {
	for _, v := range vs {
		if v != "" && !strings.ContainsAny(v, `*?[\`) && !slices.Contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}

// validUser is the rule auth.APIKey has for users: at most 256 bytes of
// UTF-8 with no control characters (it lands in logs).
func validUser(s string) bool {
	if len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}
