package wire

import "time"

// Credential statuses of a slot (GET /v1/credentials), and of a toolset
// whose spec names a slot (GET /v1/toolsets), for the caller.
const (
	// CredentialConnected: a credential is stored for the caller (their
	// own, for a user-scoped slot; the tenant's, for a tenant-scoped one).
	CredentialConnected = "connected"
	// CredentialNotConnected: none is stored.
	CredentialNotConnected = "not_connected"
	// CredentialNeedsReconnect: one is stored, but its refresh token was
	// refused (invalid_grant): connect the account again.
	CredentialNeedsReconnect = "needs_reconnect"
)

// The scopes of a credential slot: whose credential a toolset uses.
const (
	// ScopeTenant: one credential the whole tenant shares (an org's
	// connection). Setting it needs the scope credentials:admin.
	ScopeTenant = "tenant"
	// ScopeUser: each user's own (delegated access, such as a mailbox).
	ScopeUser = "user"
)

// The kinds of credential PUT /v1/credentials/{slot} sets. OAuth2 tokens
// ("oauth2") come from POST /v1/credentials/{slot}/connect.
const (
	KindAPIKey            = "api_key"
	KindBasic             = "basic"
	KindClientCredentials = "client_credentials"
	KindOAuth2            = "oauth2"
)

// CredentialInfo is one slot as the caller sees it: whether a credential is
// stored for them, and what kind. It never holds a value.
type CredentialInfo struct {
	Slot string `json:"slot"`
	// Scope is ScopeTenant or ScopeUser. A slot the server's toolsets use
	// with both scopes is listed once for each.
	Scope string `json:"scope"`
	// Status is CredentialConnected, CredentialNotConnected or
	// CredentialNeedsReconnect.
	Status string `json:"status"`
	// Kind is the stored credential's kind, when there is one.
	Kind string `json:"kind,omitempty"`
	// OAuth says the server can connect an account for the slot (POST
	// /v1/credentials/{slot}/connect).
	OAuth bool `json:"oauth"`
	// Toolsets are the toolsets of the server that use the slot with this
	// scope.
	Toolsets []string `json:"toolsets"`
	// Version counts the writes of the stored credential; CreatedAt and
	// UpdatedAt date it, and Expiry is when its access token expires (an
	// OAuth2 token that says).
	Version   int64     `json:"version,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Expiry    time.Time `json:"expiry,omitzero"`
}

// CredentialList is the body of GET /v1/credentials.
type CredentialList struct {
	V           int              `json:"v"`
	Credentials []CredentialInfo `json:"credentials"`
}

// PutCredentialRequest is the body of PUT /v1/credentials/{slot}: a
// credential to store, replacing the one there.
type PutCredentialRequest struct {
	// Scope is ScopeTenant or ScopeUser; it may be left out when the
	// server's toolsets use the slot with one scope only.
	Scope string `json:"scope,omitempty"`
	// Kind is KindAPIKey (APIKey), KindBasic (Username and Password) or
	// KindClientCredentials (ClientID, ClientSecret, TokenURL and Scopes).
	Kind     string `json:"kind"`
	APIKey   string `json:"api_key,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// TokenURL must be https, on a public address (the server's SSRF
	// guard): the server sends ClientSecret there.
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	TokenURL     string   `json:"token_url,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// ConnectRequest is the body of POST /v1/credentials/{slot}/connect.
type ConnectRequest struct {
	// Scope is as in PutCredentialRequest.
	Scope string `json:"scope,omitempty"`
	// ReturnURL is where the callback sends the browser when the flow
	// ends, with "result" (CallbackConnected, CallbackDenied,
	// CallbackExpired or CallbackFailed) and "slot" added to its query. It
	// must be on the server's allowlist. Without it the callback shows a
	// page of its own.
	ReturnURL string `json:"return_url,omitempty"`
}

// ConnectResponse is the answer to POST /v1/credentials/{slot}/connect:
// the provider's page to send the user's browser to, and when the flow
// expires.
type ConnectResponse struct {
	V            int       `json:"v"`
	AuthorizeURL string    `json:"authorize_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// The results the OAuth callback adds to a flow's return URL.
const (
	CallbackConnected = "connected" // the tokens are stored
	CallbackDenied    = "denied"    // the provider sent an error (most often: the user declined)
	CallbackExpired   = "expired"   // the flow took longer than its 10 minutes
	CallbackFailed    = "failed"    // the code exchange or storing the tokens failed
)
