// Package providers is how each OAuth provider's authorization differs from
// the spec, as data: the presets that the MCP OAuth core (pkg/mcp/oauth's
// Profiles) and the credential slots' connect flows (credentials.oauth2's
// "profile") both start from. It imports nothing of agentkit's, so a binary
// that connects slots needs no MCP client for it.
package providers

import "time"

// Preset is one provider's differences. The zero value is the spec:
// endpoints are discovered, offline_access asks for a refresh token, and
// resource is sent.
type Preset struct {
	// AuthURL and TokenURL are fixed endpoints; empty where they depend on
	// the account (a Salesforce My Domain, a Microsoft tenant) or are
	// discovered.
	AuthURL  string
	TokenURL string
	// RefreshScope is the scope that asks for a refresh token:
	// "offline_access" (the spec, Microsoft; also "") or "refresh_token"
	// (Salesforce). "-" never adds one (Google asks with AuthParams).
	RefreshScope string
	// AuthParams are added to the authorization URL.
	AuthParams map[string]string
	// AuthInParams sends the client's id and secret in the token request's
	// form rather than HTTP basic (HubSpot takes them only in the form).
	AuthInParams bool
	// OmitResource leaves resource (RFC 8707) out: Microsoft's v2 endpoints
	// refuse it together with scopes.
	OmitResource bool
	// DefaultTTL is an access token's lifetime when the token response has
	// no expires_in (Salesforce).
	DefaultTTL time.Duration
	// KeepExtras names token response fields kept with the token, such as
	// Salesforce's instance_url. They must not be secret.
	KeepExtras []string
}

// Presets returns the providers by name: "salesforce", "google", "hubspot"
// and "microsoft". Each call returns fresh copies.
func Presets() map[string]Preset {
	return map[string]Preset{
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
		// HubSpot: a public app; a refresh token always comes, without
		// asking; the client goes in the token request's form.
		"hubspot": {
			AuthURL:      "https://app.hubspot.com/oauth/authorize",
			TokenURL:     "https://api.hubapi.com/oauth/v1/token",
			RefreshScope: "-",
			AuthInParams: true,
		},
		// Microsoft identity platform v2: offline_access for a refresh
		// token; resource is refused alongside v2 scopes.
		"microsoft": {
			RefreshScope: "offline_access",
			OmitResource: true,
		},
	}
}
