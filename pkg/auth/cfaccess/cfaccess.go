package cfaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Header is the request header Cloudflare Access puts its assertion in.
const Header = "Cf-Access-Jwt-Assertion"

// CertsPath is where a team domain publishes its keys (a JWKS).
const CertsPath = "/cdn-cgi/access/certs"

// KindCFAccess is the auth.Credential Kind of a principal authenticated by
// an assertion. Its ID is the issuer, the team domain's URL.
const KindCFAccess = "cf_access"

// teamSuffix completes a bare team name into its team domain.
const teamSuffix = ".cloudflareaccess.com"

// Config configures an [Authenticator].
type Config struct {
	// TeamDomain is the Zero Trust team domain: "myteam.cloudflareaccess.com",
	// the team name alone ("myteam"), or its URL. A URL may be http only on
	// a loopback host (tests).
	TeamDomain string `json:"team_domain"`
	// AUD is the Access application's audience tag, which its assertions
	// name in "aud".
	AUD string `json:"aud"`
	// Tenant is the tenant of every principal.
	Tenant string `json:"tenant,omitempty"`
	// Emails, when set, are the only emails let in (compared without case):
	// a second check on the Access policy.
	Emails []string `json:"emails,omitempty"`
	// Scopes are every principal's scopes (default "*").
	Scopes []string `json:"scopes,omitempty"`
	// Toolsets are every principal's toolsets (default "*").
	Toolsets []string `json:"toolsets,omitempty"`
}

// Option configures an [Authenticator].
type Option func(*options)

type options struct {
	jwt []jwt.Option
	log *slog.Logger
}

// WithClock sets the clock that assertion times and the key cache read
// (default clock.Real).
func WithClock(c clock.Clock) Option {
	return func(o *options) { o.jwt = append(o.jwt, jwt.WithClock(c)) }
}

// WithHTTPClient sets the client that fetches the keys (default: a client
// with a 10 s timeout).
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.jwt = append(o.jwt, jwt.WithHTTPClient(c)) }
}

// WithLogger sets the logger (default: discard). Refused requests are
// logged at info with the reason, never the assertion or its claims.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.log = l; o.jwt = append(o.jwt, jwt.WithLogger(l)) }
}

// Authenticator verifies Cloudflare Access assertions. Make one with [New].
// It is safe for concurrent use.
type Authenticator struct {
	tokens   *jwt.Authenticator
	issuer   string
	tenant   tenancy.Tenant
	emails   []string // lower case
	scopes   []string
	toolsets []string
	log      *slog.Logger
}

// New returns an Authenticator for cfg. It fetches nothing: the keys are
// fetched on the first assertion (or by [Authenticator.Refresh]).
func New(cfg Config, opts ...Option) (*Authenticator, error) {
	iss, err := issuer(cfg.TeamDomain)
	if err != nil {
		return nil, fmt.Errorf("auth/cfaccess: team_domain: %w", err)
	}
	if strings.TrimSpace(cfg.AUD) == "" {
		return nil, errors.New("auth/cfaccess: aud: missing (the Access application's audience tag)")
	}
	t, err := tenancy.NewTenant(cfg.Tenant)
	if err != nil {
		return nil, fmt.Errorf("auth/cfaccess: tenant: %w", err)
	}
	o := options{log: slog.New(slog.DiscardHandler)}
	for _, f := range opts {
		f(&o)
	}
	tokens, err := jwt.New(jwt.Config{
		Issuer: iss, Audience: cfg.AUD, JWKSURL: iss + CertsPath,
		Algorithms: []string{"RS256"}, Tenant: cfg.Tenant, UserClaim: "email",
	}, append(o.jwt, jwt.WithHeader(Header))...)
	if err != nil {
		return nil, fmt.Errorf("auth/cfaccess: %w", err)
	}
	a := &Authenticator{
		tokens: tokens, issuer: iss, tenant: t, log: o.log,
		scopes: slices.Clone(cfg.Scopes), toolsets: slices.Clone(cfg.Toolsets),
	}
	if len(a.scopes) == 0 {
		a.scopes = []string{"*"}
	}
	if len(a.toolsets) == 0 {
		a.toolsets = []string{"*"}
	}
	for _, e := range cfg.Emails {
		if e = strings.TrimSpace(e); e == "" {
			return nil, errors.New("auth/cfaccess: emails: an empty email")
		}
		a.emails = append(a.emails, strings.ToLower(e))
	}
	return a, nil
}

// issuer is the URL of a team domain, which assertions name in "iss".
func issuer(team string) (string, error) {
	team = strings.TrimSpace(team)
	if team == "" {
		return "", errors.New("missing")
	}
	if strings.Contains(team, "://") {
		u, err := url.Parse(team)
		if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("want a team domain such as myteam.cloudflareaccess.com, or its https URL")
		}
		return u.Scheme + "://" + strings.ToLower(u.Host), nil
	}
	if strings.ContainsAny(team, "/?#@: ") {
		return "", errors.New("want a team domain such as myteam.cloudflareaccess.com")
	}
	if !strings.Contains(team, ".") {
		team += teamSuffix
	}
	return "https://" + strings.ToLower(team), nil
}

// Issuer is the team domain's URL: the "iss" assertions must have.
func (a *Authenticator) Issuer() string { return a.issuer }

// Refresh fetches the team's keys now, unless they were fetched less than a
// minute ago. Serve calls it at startup to find a wrong team domain early; a
// failure is not fatal, as the next assertion tries again.
func (a *Authenticator) Refresh(ctx context.Context) error { return a.tokens.Refresh(ctx) }

// Authenticate returns the principal of the request's assertion:
// auth.ErrNoCredentials without the header, auth.ErrInvalidCredentials for
// one that does not verify, has no email, or an email not in Emails.
func (a *Authenticator) Authenticate(r *http.Request) (auth.Principal, error) {
	p, err := a.tokens.Authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if p.User == "" {
		return auth.Principal{}, fmt.Errorf("%w: the assertion has no email (a service token?)", auth.ErrInvalidCredentials)
	}
	if len(a.emails) > 0 && !slices.Contains(a.emails, strings.ToLower(p.User)) {
		return auth.Principal{}, fmt.Errorf("%w: the email is not one of auth.cloudflare_access.emails", auth.ErrInvalidCredentials)
	}
	return auth.Principal{
		Tenant: a.tenant, User: p.User,
		Scopes: slices.Clone(a.scopes), Toolsets: slices.Clone(a.toolsets),
		Via: auth.Credential{Kind: KindCFAccess, ID: a.issuer},
	}, nil
}

// Require returns middleware that serves a request only if it carries a
// valid assertion, or public(r) holds (public may be nil). Others are
// answered 401, and unsafe cross-origin browser requests 403. The request
// reaches next unchanged: the handlers behind authenticate it again, with
// their own chain.
func (a *Authenticator) Require(public func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		csrf := http.NewCrossOriginProtection()
		protected := csrf.Handler(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public != nil && public(r) {
				next.ServeHTTP(w, r)
				return
			}
			if _, err := a.Authenticate(r); err != nil {
				level := slog.LevelInfo
				if errors.Is(err, auth.ErrNoCredentials) && !errors.Is(err, auth.ErrInvalidCredentials) {
					level = slog.LevelDebug
				}
				a.log.LogAttrs(r.Context(), level, "auth/cfaccess: request refused", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.String("reason", err.Error()))
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, "a valid Cloudflare Access assertion ("+Header+") is required\n")
				return
			}
			protected.ServeHTTP(w, r)
		})
	}
}
