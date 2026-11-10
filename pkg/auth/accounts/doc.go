// Package accounts is sign-in for people who are not configured users:
// accounts that sign themselves up, each with several linked identities,
// reached through sign-in lanes that a deployment switches on and off.
//
// An [Account] is a person. An [Identity] is one way they prove who they
// are, in one lane: a Google account (its "sub"), a username and password,
// a phone number or an email address that received a one-time code. An
// account has as many identities as it likes, in any lanes, and signs in
// with any of them; it loses none of its data when one goes, as long as one
// is left. Identities are never joined because they look alike (the same
// email in two lanes): a person links a new identity while signed in, so
// nobody gets into an account by making an identity that resembles its.
//
// A lane is one implementation of a way to sign in ([Lane]). There are
// three kinds, by what the person does:
//
//   - redirect ([RedirectLane]): the browser goes to a provider and comes
//     back with a code; OpenID Connect in package oidc, with a Google
//     preset;
//   - secret ([SecretLane]): the person types something only they know: a
//     password ([PasswordLane]) or a sign-in code someone made for them
//     ([InviteLane], the assisted sign-up lane);
//   - code ([CodeLane]): a one-time code is sent to a phone or an email
//     address through a [Sender], and typed back ([OneTimeCodeLane]).
//
// [Service] holds the lanes in the order the deployment lists them; which
// lanes exist, and which of them may create an account ([WithSignUp]), is
// configuration, not code. [Service.Handler] serves the HTTP side under
// one prefix, for any app.
//
// # Sessions
//
// A sign-in starts a session: an opaque 256-bit id from crypto/rand in a
// cookie that is HttpOnly, SameSite=Strict, path "/", and Secure with the
// __Host- prefix over TLS. The store keeps it under the SHA-256 of the id.
// Sessions are long by default ([DefaultIdleTimeout] unused, at most
// [DefaultAbsoluteTimeout] in all), because the people this is for sign in
// on their own phone and should rarely have to again; [Service.SignOutAll]
// ends every session of an account. An unsafe request of a session must
// carry an Origin naming this server and the session's CSRF token in
// [CSRFHeader] ([Service.CheckCSRF]).
//
// # Codes
//
// One-time codes, sign-in codes and redirect flows are [Challenge]s in the
// store, under the SHA-256 of their secret: single use (a second redeem of
// the same code fails, however close together), expiring, and limited in
// attempts. Wrong secrets are counted per client address and per
// destination; past a limit the lane answers [*ThrottledError] without
// checking ([limiter]).
//
// Secrets (passwords, codes, session ids, provider tokens) are never
// logged, kept in the clear or put in an error.
package accounts
