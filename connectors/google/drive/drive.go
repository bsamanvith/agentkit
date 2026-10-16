package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the factory's type, the "type" of a toolset spec.
const Type = "google_drive"

// DefaultBaseURL is Google's API host (Drive's paths, including uploads,
// hang under it).
const DefaultBaseURL = "https://www.googleapis.com"

const (
	defaultReadChars = 20000
	maxReadChars     = 200_000
)

// Option configures [Factory].
type Option func(*factory)

// WithTokenSource sets where the user's access token comes from. Without
// it every call fails with "not connected".
func WithTokenSource(ts TokenSource) Option { return func(f *factory) { f.tokens = ts } }

// WithHTTPClient sets the client requests go through.
func WithHTTPClient(c *http.Client) Option { return func(f *factory) { f.client = c } }

// WithBaseURL sets Google's address; "" keeps the default. A base_url
// setting overrides it for its toolset.
func WithBaseURL(u string) Option {
	return func(f *factory) {
		if u != "" {
			f.baseURL = strings.TrimSuffix(u, "/")
		}
	}
}

// Factory returns the "google_drive" toolset factory.
//
// Settings:
//
//	{"base_url": "", "max_read_chars": 20000, "allow_public_sharing": false}
//
// max_read_chars caps the text drive_read_file returns per call (the agent
// pages on with offset). allow_public_sharing lets drive_share_file make a
// file readable by anyone with the link; by default it refuses.
func Factory(opts ...Option) toolset.Factory {
	f := &factory{baseURL: DefaultBaseURL}
	for _, o := range opts {
		o(f)
	}
	if f.client == nil {
		f.client = &http.Client{}
	}
	if f.tokens == nil {
		f.tokens = notConnected{}
	}
	return f
}

type factory struct {
	client  *http.Client
	baseURL string
	tokens  TokenSource
}

func (*factory) Type() string { return Type }

type settings struct {
	BaseURL            string `json:"base_url"`
	MaxReadChars       int    `json:"max_read_chars"`
	AllowPublicSharing bool   `json:"allow_public_sharing"`
}

type notConnected struct{}

func (notConnected) Token(context.Context) (string, error) {
	return "", fmt.Errorf("%w: no Google account is connected", tool.ErrToolFailed)
}

func (f *factory) Build(_ context.Context, raw json.RawMessage) ([]tool.Tool, error) {
	var s settings
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return nil, err
	}
	bad := func(format string, args ...any) ([]tool.Tool, error) {
		return nil, fmt.Errorf("%w: google_drive: %s", toolset.ErrInvalidSettings, fmt.Sprintf(format, args...))
	}
	if s.MaxReadChars == 0 {
		s.MaxReadChars = defaultReadChars
	}
	if s.MaxReadChars < 100 || s.MaxReadChars > maxReadChars {
		return bad("max_read_chars must be in 100..%d", maxReadChars)
	}
	base := f.baseURL
	if s.BaseURL != "" {
		u, ok := checkBaseURL(s.BaseURL)
		if !ok {
			return bad("base_url must be an https URL with a host and no user, query or fragment (http only for localhost or a loopback IP)")
		}
		base = u
	}
	d := &drive{
		api:      &api{http: f.client, baseURL: base, tokens: f.tokens},
		maxChars: s.MaxReadChars,
		public:   s.AllowPublicSharing,
	}
	return d.tools()
}

// checkBaseURL accepts an https URL, or an http one on a loopback host (a
// local replay server). The token goes wherever base_url points.
func checkBaseURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", false
		}
	default:
		return "", false
	}
	return strings.TrimSuffix(u.String(), "/"), true
}
