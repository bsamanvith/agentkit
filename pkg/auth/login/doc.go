// Package login is password sign-in for the console: local users from the
// configuration, and session cookies that authenticate their requests.
//
// A [User] has a username, an argon2id password hash in PHC form (made with
// [HashPassword], or `agentkit users hash-password`), a tenant and roles. The
// role "user" grants the scopes "agent:run" and "agent:read"; "admin" adds
// "agent:admin" and "admin:*" ([ScopesOf]).
//
// [Login.SignIn] checks a username and password and starts a session: an
// opaque 256-bit id from crypto/rand, sent as a cookie that is HttpOnly,
// SameSite=Strict, path "/", and Secure (with the __Host- prefix) when the
// request came over TLS or through a trusted proxy that says it did. The
// store keeps the session under the SHA-256 of its id, never the id. A session
// ends after 12 hours unused or 7 days in all, or at [Login.SignOut].
//
// [Login] is an auth.Authenticator of those cookies, one more in an
// auth.Chain: no cookie is auth.ErrNoCredentials, an unknown or expired
// session auth.ErrInvalidCredentials. Its principals have Via.Kind
// [KindSession] and Via.ID the username, never the session id.
//
// A browser sends cookies with every request, so a request authenticated by
// a session with an unsafe method must also prove it came from the console
// ([Login.CheckCSRF]): an Origin header naming this server, and the session's
// CSRF token ([Info.CSRF]) in [CSRFHeader]. Bearer credentials need neither.
//
// Wrong passwords and unknown users fail alike ([ErrBadLogin]): the same
// error, and the same work, since an unknown user's attempt is checked against
// a dummy hash with the configured users' parameters. Failures are counted
// per username and per client address; past a threshold each further attempt
// waits twice as long as the last ([ThrottledError]).
//
// Passwords are never logged, kept or put in an error.
package login
