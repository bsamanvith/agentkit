// Package cfaccess verifies Cloudflare Access in front of agentkit: the
// Cf-Access-Jwt-Assertion header that Access adds to every request it lets
// through to the origin (a Cloudflare Tunnel, say).
//
// Access is a reverse proxy that signs people in (with the team's identity
// providers) before any request reaches the server. Checking its assertion
// here is defense in depth: a request that reaches the origin some other way
// (an open port, a tunnel hostname without an Access application) carries
// no valid assertion and is refused.
//
// An assertion is a JWT that pkg/auth/jwt verifies, with these settings:
//   - the issuer is the team domain, https://<team>.cloudflareaccess.com;
//   - the keys are the team's, at <team domain>/cdn-cgi/access/certs,
//     cached and fetched again as pkg/auth/jwt does;
//   - RS256 only;
//   - "aud" names the Access application's audience (AUD) tag;
//   - "exp" and "nbf" (and "iat") hold, with a 60 s leeway.
//
// The principal's user is the "email" claim; an assertion without one (a
// service token's) is refused. Every principal gets the configured tenant,
// scopes and toolsets: Access decides who gets in, and this is for one
// owner or a small team that all may do everything. [Config.Emails] narrows
// it further, should the Access policy let in more than it should.
//
// [Authenticator.Authenticate] is an auth.Authenticator, to chain with the
// API keys. [Authenticator.Require] is middleware for the whole server: a
// request without a valid assertion is refused before any handler, except
// the paths its caller names public (the health probes, an OAuth callback,
// inbound webhooks: those Access is told to bypass). Since the browser sends
// the Access cookie by itself, Require also refuses cross-origin requests
// that are not GET, HEAD or OPTIONS (net/http's CrossOriginProtection).
package cfaccess
