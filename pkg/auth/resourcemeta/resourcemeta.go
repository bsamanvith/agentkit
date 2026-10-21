// Package resourcemeta serves OAuth 2.0 Protected Resource Metadata (RFC
// 9728): the document at /.well-known/oauth-protected-resource that tells an
// OAuth client (an MCP client such as Claude Code) which authorization
// servers issue tokens for this server, and which scopes it knows. A 401
// points at it with WWW-Authenticate: Bearer resource_metadata="..." (see
// auth.WithResourceMetadata and [URL]).
//
// It is built on the MCP SDK's auth.ProtectedResourceMetadataHandler and
// oauthex.ProtectedResourceMetadata. It is its own package, apart from
// auth/jwt, so that only what serves MCP links the SDK.
package resourcemeta

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// WellKnown is the path of the metadata of the resource at the root. The
// metadata of the resource at a path p is at WellKnown + p (RFC 9728 §3.1).
const WellKnown = "/.well-known/oauth-protected-resource"

// Config is what the metadata says.
type Config struct {
	// Resource is this server's resource URL, an origin such as
	// "https://agents.example.com": the audience its tokens are issued for.
	// The resource at path p is Resource + p.
	Resource string
	// AuthorizationServers are the issuers of the tokens it accepts.
	AuthorizationServers []string
	// ScopesSupported are the scopes it knows, if it says.
	ScopesSupported []string
	// ResourceName is a name for people, if any.
	ResourceName string
	// Paths are the path prefixes, such as "/mcp/", whose resources get
	// metadata at WellKnown + path. Others answer 404.
	Paths []string
}

// Handler serves the metadata of the resource at the root (WellKnown) and of
// those under Paths (WellKnown + path), each with its own resource URL, as
// RFC 9728 §3.3 has a client check. Mount it at WellKnown and WellKnown+"/".
func Handler(c Config) (http.Handler, error) {
	u, err := url.Parse(c.Resource)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		strings.TrimSuffix(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("resourcemeta: the resource must be an origin: an http(s) URL with no path, query or fragment")
	}
	if len(c.AuthorizationServers) == 0 {
		return nil, errors.New("resourcemeta: at least one authorization server is needed")
	}
	base := strings.TrimSuffix(c.Resource, "/")
	meta := func(resource string) http.Handler {
		return sdkauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource:               resource,
			AuthorizationServers:   slices.Clone(c.AuthorizationServers),
			ScopesSupported:        slices.Clone(c.ScopesSupported),
			BearerMethodsSupported: []string{"header"},
			ResourceName:           c.ResourceName,
		})
	}
	root := meta(base)
	paths := slices.Clone(c.Paths)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == WellKnown {
			root.ServeHTTP(w, r)
			return
		}
		p, ok := strings.CutPrefix(r.URL.Path, WellKnown)
		if !ok || !slices.ContainsFunc(paths, func(pre string) bool { return strings.HasPrefix(p, pre) }) {
			http.NotFound(w, r)
			return
		}
		meta(base+p).ServeHTTP(w, r)
	}), nil
}

// URL returns where the metadata of the resource at path is: the resource
// (an origin), WellKnown, then path without a trailing slash ("" or "/" for
// the root).
func URL(resource, path string) string {
	return strings.TrimSuffix(resource, "/") + WellKnown + strings.TrimSuffix(path, "/")
}

// Middleware is auth.Middleware(a, opts...) whose 401s point at the metadata
// of the resource asked for: for a path under one of paths, such as
// /mcp/crm/... under "/mcp/", that of the endpoint (/mcp/crm), which is the
// MCP server URL a client checks the metadata's resource against; for any
// other path, that of the root.
func Middleware(a auth.Authenticator, resource string, paths []string, opts ...auth.Option) func(http.Handler) http.Handler {
	paths = slices.Clone(paths)
	opts = slices.Clone(opts)
	root := URL(resource, "")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			meta := root
			for _, pre := range paths {
				rest, ok := strings.CutPrefix(r.URL.Path, pre)
				if seg, _, _ := strings.Cut(rest, "/"); ok && seg != "" {
					meta = URL(resource, pre+url.PathEscape(seg))
					break
				}
			}
			auth.Middleware(a, append(slices.Clip(opts), auth.WithResourceMetadata(meta))...)(next).ServeHTTP(w, r)
		})
	}
}
