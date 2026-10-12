package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RequestIDHeader is the header that carries a request's id, both ways: a
// valid one sent by the client is kept, and every response carries the id
// used.
const RequestIDHeader = "X-Request-Id"

// maxRequestIDLength is the longest client request id kept, in bytes.
const maxRequestIDLength = 128

// Option configures [Middleware].
type Option func(*config)

type config struct {
	realm            string
	resourceMetadata string
	writeError       func(w http.ResponseWriter, r *http.Request, status int)
	log              *slog.Logger
}

// WithRealm sets the realm of the WWW-Authenticate challenge (default
// "agentkit").
func WithRealm(realm string) Option { return func(c *config) { c.realm = realm } }

// WithResourceMetadata adds resource_metadata=url to the WWW-Authenticate
// challenge: where an OAuth client finds the protected resource metadata
// (RFC 9728) that names the authorization server.
func WithResourceMetadata(url string) Option {
	return func(c *config) { c.resourceMetadata = url }
}

// WithErrorWriter sets how a failure's body is written: status is 401 or
// 500, and the WWW-Authenticate and [RequestIDHeader] headers are already
// set. The default writes the status text as text/plain.
func WithErrorWriter(f func(w http.ResponseWriter, r *http.Request, status int)) Option {
	return func(c *config) { c.writeError = f }
}

// WithLogger sets the logger (default: discard). Failures are logged with the
// request id and the reason, at debug level for no credentials, info for
// invalid ones and error for internal errors; successes at debug level with
// the tenant, user and credential id. Credentials are never logged.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// Middleware returns middleware that authenticates every request with a.
//
// On success the handler gets a request whose context carries the principal
// ([From]), its tenant (tenancy.With), its user if any (tenancy.WithUser) and
// the request id (tenancy.WithRequest). The request has no Authorization
// header: handlers never see the credential. Headers a client sends to name a
// tenant or user are not read.
//
// With no credentials or invalid ones it answers 401 with a WWW-Authenticate
// Bearer challenge, adding error="invalid_token" when a credential was
// presented. An internal error, or a principal with no tenant, answers 500.
// The handler is not called in either case.
func Middleware(a Authenticator, opts ...Option) func(http.Handler) http.Handler {
	c := config{realm: "agentkit", writeError: writeText, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&c)
	}
	challenge := "Bearer realm=" + quote(c.realm)
	if c.resourceMetadata != "" {
		challenge += ", resource_metadata=" + quote(c.resourceMetadata)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := requestID(r.Header.Get(RequestIDHeader))
			w.Header().Set(RequestIDHeader, id)
			ctx := r.Context()
			p, err := a.Authenticate(r)
			if err == nil && p.Tenant == (tenancy.Tenant{}) {
				err = errors.New("the authenticator returned a principal with no tenant")
			}
			switch {
			case err == nil:
			case errors.Is(err, ErrInvalidCredentials):
				c.log.LogAttrs(ctx, slog.LevelInfo, "auth: invalid credentials", slog.String("request_id", id), slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.String("reason", err.Error()))
				w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
				c.writeError(w, r, http.StatusUnauthorized)
				return
			case errors.Is(err, ErrNoCredentials):
				c.log.LogAttrs(ctx, slog.LevelDebug, "auth: no credentials", slog.String("request_id", id), slog.String("method", r.Method), slog.String("path", r.URL.Path))
				w.Header().Set("WWW-Authenticate", challenge)
				c.writeError(w, r, http.StatusUnauthorized)
				return
			default:
				c.log.LogAttrs(ctx, slog.LevelError, "auth: authentication failed", slog.String("request_id", id), slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.String("error", err.Error()))
				c.writeError(w, r, http.StatusInternalServerError)
				return
			}
			c.log.LogAttrs(ctx, slog.LevelDebug, "auth: authenticated", slog.String("request_id", id), slog.String("tenant", p.Tenant.ID()), slog.String("user", p.User), slog.String("via", p.Via.Kind+":"+p.Via.ID))

			ctx = tenancy.With(ctx, p.Tenant)
			if p.User != "" {
				ctx = tenancy.WithUser(ctx, p.User)
			}
			ctx = tenancy.WithRequest(ctx, id)
			ctx = context.WithValue(ctx, principalKey{}, p.Clone())
			r = r.Clone(ctx)
			r.Header.Del("Authorization")
			next.ServeHTTP(w, r)
		})
	}
}

// requestID returns the client's id if it is valid (1 to 128 letters, digits,
// '.', '_', ':' or '-': safe in a log line), else a new random one.
func requestID(sent string) string {
	ok := sent != "" && len(sent) <= maxRequestIDLength
	for i := 0; ok && i < len(sent); i++ {
		c := sent[i]
		ok = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._:-", c) >= 0
	}
	if ok {
		return sent
	}
	return rand.Text()
}

// quote returns s as an HTTP quoted-string, without control characters.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < ' ' || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func writeText(w http.ResponseWriter, _ *http.Request, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, http.StatusText(status)+"\n")
}
