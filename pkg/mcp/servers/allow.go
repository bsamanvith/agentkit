package servers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Allowlist is the tenant setting mcp_servers.allow (T097): the URLs admins
// may add servers at, per tenant. An empty list allows any URL (that the
// SSRF guard allows). It limits additions only: a toolset added before the
// list changed stays until an admin removes it.
//
// A pattern is [scheme://]host[:port][/path]:
//   - scheme is https when left out;
//   - host is a name ("mcp.linear.app"), "*.example.com" for its
//     subdomains (not example.com itself), or "*" for any host;
//   - port, when left out, is the scheme's default;
//   - path, when left out, is any path; in a path, "*" matches any run of
//     characters, "/" included ("/mcp/*").
//
// Hosts compare case-insensitively and must be ASCII (an IDN in its xn--
// form); a URL's query is not compared. While a list applies, a URL whose
// path has a "." or ".." segment or a backslash, encoded or not, is
// refused: a server may resolve it to a path no pattern covers. So is a
// redirect to a URL the list does not include ([Manager.Add]'s client
// checks each one).
type Allowlist struct {
	all     []allowPattern
	tenants map[string][]allowPattern
}

type allowPattern struct {
	scheme, host, port string
	// path is "" for any path, else a glob.
	path string
}

// NewAllowlist returns the allowlist with the patterns of every tenant,
// and those of the tenants that have their own, which replace them: a
// tenant whose list is empty may add any URL.
func NewAllowlist(all []string, tenants map[string][]string) (*Allowlist, error) {
	a := &Allowlist{tenants: map[string][]allowPattern{}}
	var err error
	if a.all, err = parsePatterns(all); err != nil {
		return nil, err
	}
	for id, list := range tenants {
		if _, err := tenancy.NewTenant(id); err != nil {
			return nil, fmt.Errorf("mcp_servers.allow: tenant %q: %w", id, err)
		}
		if a.tenants[id], err = parsePatterns(list); err != nil {
			return nil, fmt.Errorf("tenant %q: %w", id, err)
		}
	}
	return a, nil
}

func parsePatterns(list []string) ([]allowPattern, error) {
	out := make([]allowPattern, 0, len(list))
	for _, raw := range list {
		p, err := parsePattern(raw)
		if err != nil {
			return nil, fmt.Errorf("mcp_servers.allow: %q: %w", raw, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func parsePattern(raw string) (allowPattern, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return allowPattern{}, err
	case u.Scheme != "https" && u.Scheme != "http":
		return allowPattern{}, errors.New("the scheme must be https or http")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return allowPattern{}, errors.New("want [scheme://]host[:port][/path], with no user, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	switch rest, sub := strings.CutPrefix(host, "*."); {
	case host == "":
		return allowPattern{}, errors.New("no host")
	case !ascii(host):
		return allowPattern{}, errors.New("write an internationalized host in its ASCII (xn--) form")
	case host != "*" && strings.Contains(rest, "*"), sub && rest == "":
		return allowPattern{}, errors.New(`a host is a name, "*.domain" or "*"`)
	}
	p := allowPattern{scheme: u.Scheme, host: host, port: normalPort(u.Scheme, u.Port()), path: u.EscapedPath()}
	if p.path == "/*" {
		p.path = ""
	}
	return p, nil
}

// normalPort is "" for a scheme's default port.
func normalPort(scheme, port string) string {
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return ""
	}
	return port
}

// Restricted reports whether ctx's tenant limits the URLs it may add.
func (a *Allowlist) Restricted(ctx context.Context) bool { return len(a.patterns(ctx)) > 0 }

// Allows reports whether ctx's tenant may add a server at raw.
func (a *Allowlist) Allows(ctx context.Context, raw string) bool {
	list := a.patterns(ctx)
	if len(list) == 0 {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || !ascii(u.Hostname()) || dotSegments(u.Path) {
		return false
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	port, path := normalPort(scheme, u.Port()), u.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, p := range list {
		if p.scheme == scheme && p.port == port && hostMatch(p.host, host) && (p.path == "" || glob(p.path, path)) {
			return true
		}
	}
	return false
}

func (a *Allowlist) patterns(ctx context.Context) []allowPattern {
	if a == nil {
		return nil
	}
	if t, ok := tenancy.From(ctx); ok {
		if list, own := a.tenants[t.ID()]; own {
			return list
		}
	}
	return a.all
}

// ascii reports whether s is all ASCII. A host that is not is refused
// rather than compared after a case mapping that may differ from the HTTP
// client's IDNA conversion (U+212A, the Kelvin sign, lowers to "k").
func ascii(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// dotSegments reports whether a decoded path has a "." or ".." segment or
// a backslash, which a server may resolve ("/mcp/../admin" is "/admin").
func dotSegments(path string) bool {
	if strings.Contains(path, `\`) {
		return true
	}
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

func hostMatch(pattern, host string) bool {
	if pattern == "*" {
		return true
	}
	if domain, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+domain) && len(host) > len(domain)+1
	}
	return pattern == host
}

// glob matches s against pattern, in which "*" matches any run of
// characters and every other character itself.
func glob(pattern, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, sx
			px++
		case px < len(pattern) && pattern[px] == s[sx]:
			px++
			sx++
		case star >= 0:
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}
