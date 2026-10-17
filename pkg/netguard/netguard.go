// Package netguard keeps outbound requests to URLs that users supply off the
// internal network (SSRF, E19): only https, and only to public addresses,
// checked on the address each connection is actually made to, so a DNS
// name that resolves to a private address, or rebinds to one after a check,
// is refused. Redirects are checked the same way. An operator can allow
// internal hosts by name.
//
//	g, err := netguard.New([]string{"mcp.internal.example.com"})
//	u, err := g.CheckURL(raw)       // before storing a URL
//	hc := g.Client(nil)             // the only client that calls it
//
// The client never uses a proxy from the environment: a proxy would make
// the connection, and the address checked would be the proxy's.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned for a URL or an address the guard refuses.
var ErrBlocked = errors.New("netguard: not allowed")

// maxRedirects caps the redirects the client follows.
const maxRedirects = 5

// Guard checks URLs and the addresses connections are made to.
type Guard struct {
	exact    map[string]bool // allowed hosts
	suffixes []string        // ".example.com" for "*.example.com"
	resolver *net.Resolver
}

// Option configures [New].
type Option func(*Guard)

// WithResolver sets the resolver host names are looked up with (default
// net.DefaultResolver).
func WithResolver(r *net.Resolver) Option { return func(g *Guard) { g.resolver = r } }

// New returns a guard that allows the hosts in allow whatever they resolve
// to: a host name ("mcp.internal") or a wildcard for its subdomains
// ("*.internal.example.com", which does not match the name itself). Names
// compare case-insensitively. An allowed host may be called over http too.
func New(allow []string, opts ...Option) (*Guard, error) {
	g := &Guard{exact: map[string]bool{}}
	for _, h := range allow {
		h = strings.ToLower(strings.TrimSpace(h))
		rest, wild := strings.CutPrefix(h, "*.")
		if rest == "" || strings.ContainsAny(rest, "*/:@ ") || strings.HasPrefix(rest, ".") || strings.HasSuffix(rest, ".") {
			return nil, fmt.Errorf("netguard: allowed host %q: want a host name or *.<domain>", h)
		}
		if wild {
			g.suffixes = append(g.suffixes, "."+rest)
		} else {
			g.exact[rest] = true
		}
	}
	for _, o := range opts {
		o(g)
	}
	return g, nil
}

// allowed reports whether host is on the operator's list.
func (g *Guard) allowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if g.exact[host] {
		return true
	}
	for _, s := range g.suffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// CheckURL parses raw and checks what can be checked without connecting:
// https (or http to an allowed host), a host, no user info, and, when the
// host is an IP address, that the address is public. It returns the parsed
// URL.
func (g *Guard) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: not a URL", ErrBlocked)
	}
	return u, g.checkURL(u)
}

func (g *Guard) checkURL(u *url.URL) error {
	host := u.Hostname()
	switch {
	case host == "":
		return fmt.Errorf("%w: the URL has no host", ErrBlocked)
	case u.User != nil:
		return fmt.Errorf("%w: the URL has user info", ErrBlocked)
	case u.Scheme == "https":
	case u.Scheme == "http" && g.allowed(host):
	default:
		return fmt.Errorf("%w: the URL must be https, not %q", ErrBlocked, u.Scheme)
	}
	if g.allowed(host) {
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if why := Reason(a); why != "" {
			return fmt.Errorf("%w: %s is %s", ErrBlocked, a, why)
		}
	}
	return nil
}

// Reason says why a connection to a is refused ("a private address"), or
// is "" for a public address. IPv4-mapped, 6to4 and NAT64 addresses are
// judged by the IPv4 address they carry.
func Reason(a netip.Addr) string {
	if a.Zone() != "" {
		return "a scoped address"
	}
	a = a.Unmap()
	if a.Is6() {
		for _, p := range embedding {
			if p.Contains(a) {
				b := a.As16()
				var v4 [4]byte
				if p.Bits() == 16 { // 6to4: 2002:AABB:CCDD::
					copy(v4[:], b[2:6])
				} else { // NAT64: the last 32 bits
					copy(v4[:], b[12:])
				}
				if why := Reason(netip.AddrFrom4(v4)); why != "" {
					return why + " (inside " + p.String() + ")"
				}
				return ""
			}
		}
	}
	switch {
	case !a.IsValid():
		return "not an address"
	case a.IsUnspecified():
		return "an unspecified address"
	case a.IsLoopback():
		return "a loopback address"
	case a.IsLinkLocalUnicast():
		return "a link-local address (cloud metadata lives here)"
	case a.IsMulticast(), a.IsLinkLocalMulticast(), a.IsInterfaceLocalMulticast():
		return "a multicast address"
	case a.IsPrivate():
		return "a private address"
	}
	for _, b := range blocked {
		if b.prefix.Contains(a) {
			return b.why
		}
	}
	if !a.IsGlobalUnicast() {
		return "not a public unicast address"
	}
	return ""
}

// embedding are the IPv6 ranges that carry an IPv4 address.
var embedding = []netip.Prefix{
	netip.MustParsePrefix("2002::/16"),      // 6to4
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64, well-known
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64, local use
}

// blocked are special-purpose ranges (IANA's registries) the netip
// predicates above do not cover.
var blocked = []struct {
	prefix netip.Prefix
	why    string
}{
	{netip.MustParsePrefix("0.0.0.0/8"), `a "this network" address`},
	{netip.MustParsePrefix("100.64.0.0/10"), "a carrier-grade NAT address"},
	{netip.MustParsePrefix("192.0.0.0/24"), "an IETF protocol address"},
	{netip.MustParsePrefix("192.0.2.0/24"), "a documentation address"},
	{netip.MustParsePrefix("192.88.99.0/24"), "a 6to4 relay address"},
	{netip.MustParsePrefix("198.18.0.0/15"), "a benchmarking address"},
	{netip.MustParsePrefix("198.51.100.0/24"), "a documentation address"},
	{netip.MustParsePrefix("203.0.113.0/24"), "a documentation address"},
	{netip.MustParsePrefix("240.0.0.0/4"), "a reserved address"},
	{netip.MustParsePrefix("100::/64"), "a discard address"},
	{netip.MustParsePrefix("2001::/23"), "an IETF protocol address (Teredo among them)"},
	{netip.MustParsePrefix("2001:db8::/32"), "a documentation address"},
	{netip.MustParsePrefix("fec0::/10"), "a site-local address"},
}

// DialContext connects like net.Dialer, refusing a connection to an address
// [Reason] rejects unless the host is allowed. It checks each address the
// name resolves to as the connection is made, so a name that resolves (or
// rebinds) to an internal address gets no connection.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Resolver: g.resolver}
	if !g.allowed(host) {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrBlocked, address)
			}
			if why := Reason(ap.Addr()); why != "" {
				return fmt.Errorf("%w: %s resolves to %s, %s", ErrBlocked, host, ap.Addr(), why)
			}
			return nil
		}
	}
	return d.DialContext(ctx, network, addr)
}

// Client returns an HTTP client that connects through [Guard.DialContext],
// uses no proxy, and checks every redirect with [Guard.CheckURL]. base is
// cloned for its TLS and timeout settings; nil means http.DefaultTransport's.
func (g *Guard) Client(base *http.Transport) *http.Client {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	t := base.Clone()
	t.Proxy = nil
	t.DialContext = g.DialContext
	t.DialTLSContext = nil
	return &http.Client{
		Transport: t,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: more than %d redirects", ErrBlocked, maxRedirects)
			}
			return g.checkURL(req.URL)
		},
	}
}
