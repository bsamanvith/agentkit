package oauth

import (
	"fmt"
	"time"
)

// Profile is how one provider's OAuth differs from the MCP spec. The zero
// value is the spec: everything is discovered, the client is registered by
// CIMD or DCR, and resource is sent. Each field changes one thing, and a
// toolset's config can set any of them on top of a preset from [Profiles].
type Profile struct {
	// ClientID and ClientSecret are a pre-registered client. When ClientID
	// is set it is used first, before CIMD and DCR. Salesforce, Google and
	// Microsoft need one: they support neither.
	ClientID     string
	ClientSecret Secret
	// Issuer, if set, is the only authorization server the pre-registered
	// client may be sent to.
	Issuer string

	// AuthURL and TokenURL skip discovery: providers whose MCP servers
	// publish no metadata, or whose endpoints depend on the account (a
	// Salesforce My Domain). Both or neither.
	AuthURL  string
	TokenURL string

	// Scopes to ask for. Empty takes the scopes the server's 401 or its
	// metadata names.
	Scopes []string
	// RefreshScope is the scope that asks for a refresh token:
	// "offline_access" (the spec, Microsoft) or "refresh_token"
	// (Salesforce). It is added when the authorization server lists it, or
	// always when discovery is skipped. "-" never adds one (Google asks with
	// AuthParams instead).
	RefreshScope string
	// AuthParams are added to the authorization URL, such as Google's
	// access_type=offline and prompt=consent.
	AuthParams map[string]string
	// OmitResource leaves the resource parameter (RFC 8707) out of the
	// authorization and token requests. Microsoft's v2 endpoints refuse
	// resource together with scopes.
	OmitResource bool

	// DefaultTTL is the access token's lifetime when the token response has
	// no expires_in (Salesforce). Zero treats such a token as never
	// expiring until the server rejects it.
	DefaultTTL time.Duration
	// KeepExtras names token response fields to keep in [Token].Extra, such
	// as Salesforce's instance_url. They must not be secret.
	KeepExtras []string

	// OnToken, if set, is called with each new token, after the code
	// exchange and after each refresh, before it is stored; it may change
	// it or refuse it. For the rare provider whose differences are not data.
	OnToken func(*Token) error
}

func (p Profile) validate() error {
	if (p.AuthURL == "") != (p.TokenURL == "") {
		return fmt.Errorf("oauth: profile sets one of AuthURL and TokenURL; set both or neither")
	}
	if p.AuthURL != "" && p.ClientID == "" {
		return fmt.Errorf("oauth: a profile without discovery needs a pre-registered ClientID")
	}
	return nil
}

func (p Profile) refreshScope() string {
	switch p.RefreshScope {
	case "":
		return "offline_access"
	case "-":
		return ""
	}
	return p.RefreshScope
}

// Profiles returns the presets: "mcp" (the spec), "salesforce", "google"
// and "microsoft". Each is a fresh copy. The provider presets carry no client
// (the operator registers one with the provider and sets ClientID and
// ClientSecret), and Salesforce and Microsoft need their endpoints set for
// the account (see [SalesforceEndpoints], [MicrosoftEndpoints]).
func Profiles() map[string]Profile {
	return map[string]Profile{
		"mcp": {},
		// Salesforce: a connected app; no expires_in in the token response;
		// instance_url names the org's API host.
		"salesforce": {
			RefreshScope: "refresh_token",
			DefaultTTL:   time.Hour,
			KeepExtras:   []string{"instance_url"},
		},
		// Google: a refresh token only with access_type=offline, and again
		// on reconnect only with prompt=consent.
		"google": {
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			RefreshScope: "-",
			AuthParams:   map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		// Microsoft identity platform v2: offline_access for a refresh
		// token; resource is refused alongside v2 scopes.
		"microsoft": {
			RefreshScope: "offline_access",
			OmitResource: true,
		},
	}
}

// SalesforceEndpoints sets p's endpoints for a Salesforce login host, such
// as "https://login.salesforce.com" or a My Domain.
func SalesforceEndpoints(p Profile, loginHost string) Profile {
	p.AuthURL = loginHost + "/services/oauth2/authorize"
	p.TokenURL = loginHost + "/services/oauth2/token"
	return p
}

// MicrosoftEndpoints sets p's endpoints for an Entra ID tenant ("common",
// "organizations" or a tenant id).
func MicrosoftEndpoints(p Profile, tenant string) Profile {
	base := "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0"
	p.AuthURL = base + "/authorize"
	p.TokenURL = base + "/token"
	return p
}
