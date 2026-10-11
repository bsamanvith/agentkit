package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Errors returned by [New].
var (
	// ErrInvalidName reports a toolset name that is not safe as a URL path
	// segment: it must match ^[a-zA-Z0-9_-]{1,64}$.
	ErrInvalidName = errors.New("invalid toolset name")
	// ErrInvalidConfig reports a nil registry or an unusable option.
	ErrInvalidConfig = errors.New("invalid server configuration")
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Server serves toolsets, one MCP endpoint per toolset name. Build it with
// [New]; it is immutable afterwards and safe for concurrent use.
type Server struct {
	base     string // no trailing slash; "" for the root
	toolsets map[string]http.Handler
	listing  []ToolsetInfo // sorted by name
	handler  http.Handler  // routes, wrapped in the middleware
	listBody []byte
}

var _ http.Handler = (*Server)(nil)

// ToolsetInfo describes one served toolset, as listed by GET {base}/.
type ToolsetInfo struct {
	Name  string `json:"name"`
	Tools int    `json:"tools"`
}

// New builds a Server from named registries. Each name becomes the URL path
// segment of its endpoint, {base}/{name}, and must match
// ^[a-zA-Z0-9_-]{1,64}$ (else [ErrInvalidName]). The tools of each registry
// are listed once, here: a registry whose tools change later needs a new
// Server.
func New(toolsets map[string]tool.Registry, opts ...Option) (*Server, error) {
	c := newConfig(opts)
	base, err := cleanBase(c.basePath)
	if err != nil {
		return nil, err
	}

	s := &Server{base: base, toolsets: make(map[string]http.Handler, len(toolsets))}
	names := make([]string, 0, len(toolsets))
	for name := range toolsets {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		reg := toolsets[name]
		if !namePattern.MatchString(name) {
			return nil, fmt.Errorf("%w: %q must match %s", ErrInvalidName, name, namePattern)
		}
		if reg == nil {
			return nil, fmt.Errorf("%w: toolset %q has a nil registry", ErrInvalidConfig, name)
		}
		srv, n, err := newMCPServer(context.Background(), name, reg, c.logger)
		if err != nil {
			return nil, err
		}
		s.toolsets[name] = mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return srv },
			&mcp.StreamableHTTPOptions{
				Stateless:                    !c.stateful,
				MaxRequestBodyBytes:          c.maxBodyBytes,
				PropagateRequestCancellation: true,
				Logger:                       c.logger,
			})
		s.listing = append(s.listing, ToolsetInfo{Name: name, Tools: n})
	}

	body, err := json.Marshal(struct {
		Toolsets []ToolsetInfo `json:"toolsets"`
	}{append([]ToolsetInfo{}, s.listing...)})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	s.listBody = body

	var h http.Handler = http.HandlerFunc(s.route)
	for i := len(c.middleware) - 1; i >= 0; i-- {
		h = c.middleware[i](h)
	}
	s.handler = h
	return s, nil
}

func cleanBase(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: base path %q must start with /", ErrInvalidConfig, p)
	}
	p = strings.TrimRight(p, "/")
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("%w: base path %q has an empty segment", ErrInvalidConfig, p)
	}
	return p, nil
}

// Toolsets lists the served toolsets, sorted by name.
func (s *Server) Toolsets() []ToolsetInfo { return append([]ToolsetInfo{}, s.listing...) }

// ServeHTTP implements [http.Handler]. Mount it at the base path without
// stripping the prefix.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, s.base+"/")
	if !ok && r.URL.Path != s.base {
		http.NotFound(w, r)
		return
	}
	if rest == "" {
		s.serveListing(w, r)
		return
	}
	name := strings.TrimSuffix(rest, "/")
	h, found := s.toolsets[name]
	if !found {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

func (s *Server) serveListing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		_, _ = w.Write(s.listBody)
	}
}
