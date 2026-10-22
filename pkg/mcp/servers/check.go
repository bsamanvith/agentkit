package servers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
)

// The scopes of a server.
const (
	scopeTenant = "tenant"
	scopeUser   = "user"
)

// maxURLLength and maxHeaders bound what an admin may send.
const (
	maxURLLength = 2048
	maxHeaders   = 20
	maxValueLen  = 4096
)

var (
	nameRE       = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	headerNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")
	secretNameRE = regexp.MustCompile(`(?i)authorization|cookie|key|token|secret|auth|password`)
	envRefRE     = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	fullRefRE    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

// reservedHeaders are the headers the transport sets itself.
var reservedHeaders = []string{
	"Host", "Content-Length", "Content-Type", "Accept", "Connection", "Transfer-Encoding",
	"Upgrade", "Te", "Trailer", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id",
}

// checkAdd checks a request to add a server and returns its record.
func (m *Manager) checkAdd(req wire.AddToolsetRequest) (record, error) {
	bad := func(format string, args ...any) (record, error) {
		return record{}, fmt.Errorf("%w: "+format, append([]any{agentapi.ErrToolsetInvalid}, args...)...)
	}
	if req.Type != Type {
		return bad("type %q: the toolsets added at run time are of type %q", req.Type, Type)
	}
	if !nameRE.MatchString(req.Name) {
		return bad("name %q must match %s", req.Name, nameRE)
	}
	if len(req.URL) > maxURLLength {
		return bad("the url is longer than %d bytes", maxURLLength)
	}
	if _, err := m.guard.CheckURL(req.URL); err != nil {
		return bad("url: %v", err)
	}
	r := record{Type: Type, URL: req.URL, Transport: req.Transport, Auth: req.Auth, Scope: req.Scope, Enabled: true, Tools: map[string]review{}}
	switch r.Transport {
	case "":
		r.Transport = string(client.TransportAuto)
	case string(client.TransportAuto), string(client.TransportHTTP), string(client.TransportSSE):
	default:
		return bad("transport %q must be auto, http or sse", req.Transport)
	}
	switch r.Scope {
	case "":
		r.Scope = scopeTenant
	case scopeTenant:
	case scopeUser:
		if req.Auth != wire.AuthOAuth {
			return bad(`scope "user" (each user connects their own account) needs auth "oauth"`)
		}
	default:
		return bad("scope %q must be tenant or user", req.Scope)
	}
	if req.OAuthClient != nil && req.Auth != wire.AuthOAuth {
		return bad(`oauth_client needs auth "oauth"`)
	}
	switch r.Auth {
	case "", wire.AuthNone:
		r.Auth = wire.AuthNone
		if len(req.Headers) > 0 {
			return bad(`headers need auth "headers"`)
		}
	case wire.AuthHeaders:
		if len(req.Headers) == 0 {
			return bad(`auth "headers" needs headers`)
		}
		if err := m.checkHeaders(req.Headers); err != nil {
			return record{}, err
		}
		r.Headers = req.Headers
	case wire.AuthOAuth:
		if err := m.checkOAuth(req, &r); err != nil {
			return record{}, err
		}
	default:
		return bad("auth %q must be none, headers or oauth", req.Auth)
	}
	return r, nil
}

// checkHeaders checks headers as given: their names, and that a header
// that carries a secret takes it from a variable the operator allows, that
// is set.
func (m *Manager) checkHeaders(headers map[string]string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{agentapi.ErrToolsetInvalid}, args...)...)
	}
	if len(headers) > maxHeaders {
		return bad("more than %d headers", maxHeaders)
	}
	for name, value := range headers {
		switch {
		case !headerNameRE.MatchString(name):
			return bad("headers: %q is not a header name", name)
		case slices.ContainsFunc(reservedHeaders, func(h string) bool { return strings.EqualFold(h, name) }):
			return bad("headers: %s is set by the transport", name)
		case len(value) > maxValueLen || strings.ContainsAny(value, "\r\n\x00"):
			return bad("headers: %s: the value is too long or has a line break", name)
		case secretNameRE.MatchString(name) && !envRefRE.MatchString(value):
			return bad(`headers: %s carries a secret: take its value from a variable the operator allows, as in "Bearer ${NAME}"`, name)
		}
		if _, err := m.expand(value); err != nil {
			return bad("headers: %s: %v", name, err)
		}
	}
	return nil
}

// expand replaces each ${NAME} in a header value with the variable, which
// the operator must allow and must be set.
func (m *Manager) expand(value string) (string, error) {
	var problem error
	out := envRefRE.ReplaceAllStringFunc(value, func(ref string) string {
		name := envRefRE.FindStringSubmatch(ref)[1]
		v, ok := m.lookupEnv(name)
		switch {
		case !m.env[name]:
			problem = fmt.Errorf("$%s is not a variable the operator allows in headers", name)
		case !ok || v == "":
			problem = fmt.Errorf("$%s is not set on the server", name)
		case strings.ContainsAny(v, "\r\n\x00"):
			problem = fmt.Errorf("$%s has a line break", name)
		}
		return v
	})
	return out, problem
}

// settings resolves a record's headers, and hashes what its connection is
// made with: the same hash means the same connection will do. A header
// whose variable is gone makes the toolset unusable.
func (m *Manager) settings(r record) (http.Header, string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s|%d:%s|", len(r.URL), r.URL, len(r.Transport), r.Transport)
	if r.Auth == wire.AuthOAuth {
		fmt.Fprintf(h, "oauth|%d:%s|%d:%s|%d:%s|%d:%s|", len(r.Scope), r.Scope, len(r.Issuer), r.Issuer,
			len(r.OAuthClientID), r.OAuthClientID, len(r.OAuthClientSecret), r.OAuthClientSecret)
	}
	var header http.Header
	names := make([]string, 0, len(r.Headers))
	for name := range r.Headers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		v, err := m.expand(r.Headers[name])
		if err != nil {
			return nil, "", fmt.Errorf("%w: header %s: %w", agentapi.ErrServerUnreachable, name, err)
		}
		if header == nil {
			header = http.Header{}
		}
		header.Set(name, v)
		fmt.Fprintf(h, "%d:%s=%d:%s|", len(name), name, len(v), v)
	}
	return header, hex.EncodeToString(h.Sum(nil)), nil
}

// clientConfig is how the client reaches a record's server of ctx's
// tenant: through the guard, with the 401s noted. A redirect must pass the
// guard and the tenant's allowlist, and must stay on the server's origin
// when the toolset sends headers, which the client adds to every request.
func (m *Manager) clientConfig(ctx context.Context, r record, header http.Header) client.Config {
	guarded := m.guard.Client(nil)
	tenant := context.WithoutCancel(ctx) // only its tenant is read
	origin, _ := url.Parse(r.URL)
	return client.Config{
		URL: r.URL, Transport: client.Transport(r.Transport), Header: header,
		HTTPClient: &http.Client{
			Transport: &authSniffer{base: guarded.Transport},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if err := guarded.CheckRedirect(req, via); err != nil {
					return err
				}
				if len(header) > 0 && (origin == nil || req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host) {
					return fmt.Errorf("a redirect to another origin (%s), which would get the toolset's headers, is not followed", req.URL.Host)
				}
				return m.checkAllowed(tenant, req.URL.String())
			},
		},
		ConnectTimeout: m.connectTimeout,
	}
}
