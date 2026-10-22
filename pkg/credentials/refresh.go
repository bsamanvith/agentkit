package credentials

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// refresh refreshes k's OAuth2 token, in the one call for k that runs in
// this process (Sources.once). It re-reads the credential first: another
// call, or a replica, may have refreshed it already. The new token is
// written with compare-and-swap on the version read; a conflict means a
// replica wrote first, so the credential is read again and its token used
// if fresh, else the refresh is tried once more (with the refresh token
// that replica stored). invalid_grant marks the credential NeedsReconnect.
func (s *source) refresh(ctx context.Context, k flightKey) (Credential, error) {
	vctx := ctxFor(ctx, k)
	for attempt := 0; ; attempt++ {
		cred, meta, err := s.load(ctx, k)
		if err != nil {
			return nil, err
		}
		o, ok := cred.(OAuth2)
		if !ok { // replaced by another kind meanwhile
			if _, cc := cred.(ClientCredentials); cc {
				return s.clientToken(ctx, k)
			}
			return cred, nil
		}
		if s.p.fresh(o) {
			return o, nil
		}
		app, ok := s.p.apps[k.slot]
		if !ok || o.RefreshToken.IsZero() {
			if s.p.usable(o) {
				return o, nil
			}
			s.p.log.Info("credential expired and cannot be refreshed", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "oauth2_app", ok)
			return nil, s.notConnected()
		}
		tok, err := s.exchange(ctx, app, o.RefreshToken.Reveal())
		if err != nil {
			if code := errorCode(err); code == "invalid_grant" {
				_, merr := s.p.vault.MarkNeedsReconnect(vctx, k.user, k.slot, meta.Version)
				if errors.Is(merr, ErrConflict) && attempt == 0 {
					continue // a replica wrote meanwhile: its token may be good
				}
				if merr != nil && !errors.Is(merr, ErrConflict) {
					s.p.log.Error("marking a credential for reconnecting", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "err", merr)
				}
				s.p.log.Warn("credential needs reconnecting: the token endpoint refused its refresh token", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant)
				return nil, s.notConnected()
			}
			if s.p.usable(o) {
				s.p.log.Warn("refreshing a credential failed; using its token until it expires", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "reason", tokenReason(err))
				return o, nil
			}
			s.p.log.Warn("refreshing a credential failed", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "reason", tokenReason(err))
			return nil, &SourceError{Toolset: s.toolset, Reason: "refreshing its credential failed: " + tokenReason(err), Err: err}
		}
		next := OAuth2{
			AccessToken: NewSecret(tok.AccessToken), RefreshToken: NewSecret(tok.RefreshToken),
			Expiry: s.expiry(tok), Scopes: o.Scopes, TokenType: tok.TokenType,
		}
		if next.RefreshToken.IsZero() { // the endpoint kept the old one
			next.RefreshToken = o.RefreshToken
		}
		_, err = s.p.vault.Put(vctx, k.user, k.slot, next, meta.Version)
		switch {
		case err == nil:
			s.p.log.Debug("credential refreshed", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "expiry", next.Expiry)
			return next, nil
		case errors.Is(err, ErrConflict) && s.supersede(vctx, k, o, next):
			return next, nil
		case errors.Is(err, ErrConflict) && attempt == 0:
			s.p.log.Debug("credential refreshed by another writer first; reading theirs", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant)
			continue
		case errors.Is(err, ErrConflict):
			return nil, &SourceError{Toolset: s.toolset, Reason: "its credential changed during a refresh, twice", Err: err}
		default:
			s.p.log.Error("storing a refreshed credential", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "err", err)
			return nil, &SourceError{Toolset: s.toolset, Reason: "its refreshed credential could not be stored", Err: err}
		}
	}
}

// supersede handles a refresh whose write lost to a replica that, having
// had the same refresh token refused (a server that allows one use of each),
// marked the credential for reconnecting: this refresh holds the newer
// token, so it replaces the mark rather than lose the chain. It reports
// whether it stored next.
func (s *source) supersede(ctx context.Context, k flightKey, used, next OAuth2) bool {
	cur, meta, err := s.p.vault.Get(ctx, k.user, k.slot)
	if err != nil || !meta.NeedsReconnect {
		return false
	}
	if o, ok := cur.(OAuth2); !ok || o.RefreshToken.Reveal() != used.RefreshToken.Reveal() {
		return false
	}
	if _, err := s.p.vault.Put(ctx, k.user, k.slot, next, meta.Version); err != nil {
		return false
	}
	s.p.log.Info("credential refreshed over a replica's reconnect mark for the same refresh token", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant)
	return true
}

// exchange trades refreshToken for a new token at app's token endpoint.
func (s *source) exchange(ctx context.Context, app OAuth2App, refreshToken string) (*oauth2.Token, error) {
	cfg := oauth2.Config{
		ClientID: app.ClientID, ClientSecret: app.ClientSecret.Reveal(),
		Endpoint: oauth2.Endpoint{TokenURL: app.TokenURL, AuthStyle: authStyle(app.AuthStyle)},
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.p.tokenClient)
	// An expired token with only the refresh token: Token refreshes it.
	return cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(1, 0)}).Token()
}

// clientToken fetches a token for k's client credentials and caches it
// for the credential's version.
func (s *source) clientToken(ctx context.Context, k flightKey) (Credential, error) {
	cred, meta, err := s.load(ctx, k)
	if err != nil {
		return nil, err
	}
	cc, ok := cred.(ClientCredentials)
	if !ok { // replaced by another kind meanwhile
		if _, o := cred.(OAuth2); o {
			return s.refresh(ctx, k)
		}
		return cred, nil
	}
	if tok, ok := s.p.cached(k, meta.Version); ok {
		return tok, nil
	}
	style := AuthInHeader
	if app, ok := s.p.apps[k.slot]; ok {
		style = app.AuthStyle
	}
	cfg := clientcredentials.Config{
		ClientID: cc.ClientID, ClientSecret: cc.ClientSecret.Reveal(), TokenURL: cc.TokenURL,
		Scopes: slices.Clone(cc.Scopes), AuthStyle: authStyle(style),
	}
	tok, err := cfg.Token(context.WithValue(ctx, oauth2.HTTPClient, s.p.storedClient))
	if err != nil {
		s.p.log.Warn("fetching a client credentials token failed", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "reason", tokenReason(err))
		return nil, &SourceError{Toolset: s.toolset, Reason: "fetching its token failed: " + tokenReason(err), Err: err}
	}
	o := OAuth2{AccessToken: NewSecret(tok.AccessToken), Expiry: s.expiry(tok), Scopes: cc.Scopes, TokenType: tok.TokenType}
	s.p.mu.Lock()
	s.p.tokens[k] = cachedToken{version: meta.Version, token: o}
	s.p.mu.Unlock()
	s.p.log.Debug("client credentials token fetched", "toolset", s.toolset, "slot", k.slot, "tenant", k.tenant, "expiry", o.Expiry)
	return o, nil
}

// expiry is when tok expires by this source's clock.
func (s *source) expiry(tok *oauth2.Token) time.Time {
	if tok.ExpiresIn > 0 {
		return s.p.clock.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC()
	}
	return tok.Expiry.UTC()
}

func authStyle(a AuthStyle) oauth2.AuthStyle {
	if a == AuthInParams {
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

// standardCodes are RFC 6749 §5.2's error codes: only these are repeated
// from a token endpoint's answer, whose other fields (and body) may echo
// what was sent.
var standardCodes = []string{
	"invalid_request", "invalid_client", "invalid_grant", "unauthorized_client",
	"unsupported_grant_type", "invalid_scope",
}

// errorCode is the standard error code a token endpoint answered with, or "".
func errorCode(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && slices.Contains(standardCodes, re.ErrorCode) {
		return re.ErrorCode
	}
	return ""
}

// tokenReason describes a failed token request without its body: the
// status and the standard error code, or that the endpoint was not reached.
func tokenReason(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		reason := "the token endpoint answered " + statusOf(re)
		if code := errorCode(err); code != "" {
			reason += " " + code
		}
		return reason
	}
	var ue *url.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the token endpoint did not answer in time"
	case errors.As(err, &ue):
		return "the token endpoint could not be reached"
	}
	return "the token endpoint's answer was not a token"
}

func statusOf(re *oauth2.RetrieveError) string {
	if re.Response == nil {
		return "an error"
	}
	return fmt.Sprint(re.Response.StatusCode)
}
