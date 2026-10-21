package wire

import "fmt"

// Error codes of the sign-in endpoints (T061).
const (
	// CodeCSRF: HTTP 403, a request authenticated by a session cookie that
	// did not show it came from the console (its Origin, or the CSRF token
	// of /v1/auth/me in X-Agentkit-CSRF). Read /v1/auth/me again for a fresh
	// token and retry once.
	CodeCSRF = "csrf_failed"
	// CodeTooManyAttempts: HTTP 429 with Retry-After, a sign-in refused
	// after too many failed ones.
	CodeTooManyAttempts = "too_many_attempts"
)

// Sign-in methods, in [AuthConfig].
const (
	// AuthPassword: a username and password, POST /v1/auth/login.
	AuthPassword = "password"
	// AuthToken: a bearer token (an API key, or an identity provider's).
	AuthToken = "token"
)

// AuthConfig is the body of GET /v1/auth/config, which needs no
// credentials: how a client signs in.
type AuthConfig struct {
	V int `json:"v"`
	// Methods are the sign-in methods the server accepts, [AuthPassword]
	// first when it is one. Empty: the server needs no credentials.
	Methods []string `json:"methods"`
}

// LoginRequest is the body of POST /v1/auth/login. Printing it never shows
// the password.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Format prints the request with the password redacted, whatever the verb.
func (r LoginRequest) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "{Username:%q Password:[redacted]}", r.Username)
}

// Me is the body of GET /v1/auth/me: who the caller is.
type Me struct {
	V int `json:"v"`
	// Username is the signed-in user, or the user of a token; absent for a
	// service key and on a server without credentials.
	Username string `json:"username,omitempty"`
	Tenant   string `json:"tenant"`
	// Roles are a signed-in user's ("user", "admin"); empty for a token.
	Roles []string `json:"roles"`
	// Scopes are what the caller may do, as patterns ("agent:run",
	// "admin:*", "*").
	Scopes []string `json:"scopes"`
	// Credential is the kind of credential: "session", "api_key", "jwt", or
	// absent on a server without credentials.
	Credential string `json:"credential,omitempty"`
	// CSRF is the token a session's unsafe requests send in the
	// X-Agentkit-CSRF header; only for a session.
	CSRF string `json:"csrf,omitempty"`
}
