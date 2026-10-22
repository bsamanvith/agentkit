package connect

import (
	"context"
	"crypto/subtle"
	"errors"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// maxQuery caps the callback's query: a code and a state are short.
const maxQuery = 8 << 10

// Callback serves GET [CallbackPath], where the provider sends the browser
// back with a code (or an error) and the state. It needs no
// authentication: the state names the flow, and the flow's own tenant and
// user, never the request's, are where the tokens are stored. The browser
// must carry the cookie [Service.Connect] set (the flow is bound to it), or
// the callback refuses it with a page. The state is used up on the first
// request that names it, even one that fails, and the cookie is cleared.
func (s *Service) Callback() http.Handler {
	return http.HandlerFunc(s.callback)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		page(w, http.StatusMethodNotAllowed, "This address only takes the provider's redirect.")
		return
	}
	if len(r.URL.RawQuery) > maxQuery {
		page(w, http.StatusRequestURITooLong, "The address is too long.")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	state := q.Get("state")
	if err != nil || state == "" || len(q["state"]) != 1 {
		page(w, http.StatusBadRequest, "This sign-in is unknown or has expired. Start connecting the account again.")
		return
	}
	// Whatever the request carries (a tenant, a principal), the flow's
	// own tenant is where the tokens go: nothing else is read from ctx.
	ctx := tenancy.With(context.WithoutCancel(r.Context()), tenancy.Tenant{})
	now := s.clk.Now().UTC()
	f, err := s.flows.take(ctx, state, now)
	if errors.Is(err, errNoFlow) {
		page(w, http.StatusBadRequest, "This sign-in is unknown, was used already, or has expired. Start connecting the account again.")
		return
	} else if err != nil {
		s.log.ErrorContext(ctx, "reading a connect flow", "err", err)
		page(w, http.StatusInternalServerError, "Connecting the account failed. Try again.")
		return
	}
	log := s.log.With("slot", f.Slot, "tenant", f.Tenant, "user", f.User)
	name := f.cookieName(state)
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: f.CookiePath, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: f.CookieSecure || overTLS(r)})
	if !boundTo(r, name, f.Binding) {
		// Not the browser that started the flow: someone else's
		// authorization URL (RFC 9700 §4.7). Nothing is exchanged, and the
		// flow's return URL is not followed.
		log.WarnContext(ctx, "connect flow finished in a browser that did not start it")
		page(w, http.StatusForbidden, "This sign-in was not started in this browser, so the account was not connected. "+
			"If you meant to connect it, start connecting the account again here, and finish in this same browser.")
		return
	}
	switch {
	case !now.Before(f.Expires):
		log.InfoContext(ctx, "connect flow expired")
		s.finish(w, r, f, wire.CallbackExpired)
		return
	case q.Get("error") != "":
		log.InfoContext(ctx, "connect flow denied by the provider", "error", safeCode(q.Get("error")))
		s.finish(w, r, f, wire.CallbackDenied)
		return
	case q.Get("code") == "" || len(q["code"]) != 1:
		log.InfoContext(ctx, "connect flow came back with no code")
		s.finish(w, r, f, wire.CallbackFailed)
		return
	}
	if err := s.complete(ctx, f, q); err != nil {
		log.WarnContext(ctx, "connecting an account failed", "reason", err.Error())
		s.finish(w, r, f, wire.CallbackFailed)
		return
	}
	log.InfoContext(ctx, "account connected")
	s.finish(w, r, f, wire.CallbackConnected)
}

// boundTo reports whether r carries the cookie name whose value's SHA-256
// is want (a flow's Binding), compared in constant time. A flow with no
// binding (kept before flows had one) matches no browser.
func boundTo(r *http.Request, name, want string) bool {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(bindingHash(c.Value)), []byte(want)) == 1
}

// overTLS reports whether r came over TLS, to this server or to a proxy
// that says so (X-Forwarded-Proto: https), as the agent API decides the
// binding cookie's Secure. Believing any proxy is safe here: Secure only
// narrows where the cookie goes.
func overTLS(r *http.Request) bool {
	p := r.Header.Values("X-Forwarded-Proto")
	return r.TLS != nil || (len(p) == 1 && strings.EqualFold(strings.TrimSpace(p[0]), "https"))
}

// reasonError is a failure whose message is safe to log: no body, no value.
type reasonError string

func (e reasonError) Error() string { return string(e) }

// complete finishes a flow in its tenant, for its user: the app's code
// exchange, or a dynamic slot's.
func (s *Service) complete(ctx context.Context, f flow, q url.Values) error {
	tn, err := tenancy.NewTenant(f.Tenant)
	if err != nil {
		return reasonError("the flow's tenant is not valid")
	}
	tctx := tenancy.With(ctx, tn)
	if f.User != "" {
		tctx = tenancy.WithUser(tctx, f.User)
	}
	if !f.Dynamic {
		return s.exchange(tctx, f, q.Get("code"))
	}
	if s.cfg.Dynamic == nil {
		return reasonError("this server no longer connects the toolsets added at run time")
	}
	if _, ok := s.dynamicSlot(tctx, f.Slot); !ok {
		return reasonError("the toolset was removed, or no longer signs in with OAuth")
	}
	cctx, cancel := context.WithTimeout(tctx, credentials.DefaultTokenTimeout)
	defer cancel()
	return s.cfg.Dynamic.Complete(cctx, f.User, f.Slot, f.Data, q)
}

// exchange trades code for tokens at the flow's app, sending the flow's
// verifier, and stores them for the flow's tenant and user (tctx).
func (s *Service) exchange(tctx context.Context, f flow, code string) error {
	app, ok := s.cfg.Apps[f.Slot]
	if !ok {
		return reasonError("the slot no longer has an OAuth2 app")
	}
	ectx, cancel := context.WithTimeout(context.WithValue(tctx, oauth2.HTTPClient, s.http), credentials.DefaultTokenTimeout)
	defer cancel()
	tok, err := oauthConfig(app).Exchange(ectx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return reasonError("the code exchange failed: " + tokenReason(err))
	}
	cred := credentials.OAuth2{
		AccessToken: credentials.NewSecret(tok.AccessToken), RefreshToken: credentials.NewSecret(tok.RefreshToken),
		Scopes: slices.Clone(app.Scopes), TokenType: tok.TokenType,
	}
	if tok.ExpiresIn > 0 {
		cred.Expiry = s.clk.Now().Add(timeSeconds(tok.ExpiresIn)).UTC()
	} else if !tok.Expiry.IsZero() {
		cred.Expiry = tok.Expiry.UTC()
	}
	if sc, ok := tok.Extra("scope").(string); ok && sc != "" {
		cred.Scopes = strings.Fields(sc)
	}
	if _, err := s.store(tctx, f.User, f.Slot, cred); err != nil {
		s.log.ErrorContext(tctx, "storing a connected account", "slot", f.Slot, "tenant", f.Tenant, "err", err)
		return reasonError("storing the tokens failed")
	}
	return nil
}

// finish ends a flow: back to its return URL with the result, or a page.
func (s *Service) finish(w http.ResponseWriter, r *http.Request, f flow, result string) {
	noLeak(w)
	if f.ReturnURL != "" && s.allowed(f.ReturnURL) {
		u, _ := url.Parse(f.ReturnURL)
		q := u.Query()
		q.Set("result", result)
		q.Set("slot", f.Slot)
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusSeeOther)
		return
	}
	switch result {
	case wire.CallbackConnected:
		page(w, http.StatusOK, "The account is connected. You can close this window.")
	case wire.CallbackDenied:
		page(w, http.StatusBadRequest, "The account was not connected: the provider did not allow it.")
	case wire.CallbackExpired:
		page(w, http.StatusBadRequest, "This sign-in has expired. Start connecting the account again.")
	default:
		page(w, http.StatusBadGateway, "Connecting the account failed. Try again.")
	}
}

// noLeak keeps the callback's URL, which holds the code, out of caches and
// Referer headers.
func noLeak(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
}

// page writes a minimal HTML page with message.
func page(w http.ResponseWriter, status int, message string) {
	noLeak(w)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>agentkit</title></head><body><p>` +
		html.EscapeString(message) + "</p></body></html>\n"))
}

// standardCodes are RFC 6749 §5.2's token error codes; tokenReason repeats
// only these from an answer whose other fields may echo what was sent.
var standardCodes = []string{
	"invalid_request", "invalid_client", "invalid_grant", "unauthorized_client",
	"unsupported_grant_type", "invalid_scope",
}

// tokenReason describes a failed exchange by its status and standard error
// code, never its body.
func tokenReason(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		reason := "the token endpoint answered"
		if re.Response != nil {
			reason += " " + http.StatusText(re.Response.StatusCode)
		}
		if slices.Contains(standardCodes, re.ErrorCode) {
			reason += " " + re.ErrorCode
		}
		return reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the token endpoint did not answer in time"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return "the token endpoint could not be reached"
	}
	return "the token endpoint's answer was not a token"
}

// safeCode keeps an error code from the provider to a short printable
// string, for the log.
func safeCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 64 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// timeSeconds is n seconds.
func timeSeconds(n int64) time.Duration { return time.Duration(n) * time.Second }
