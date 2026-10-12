package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the factory's type, the "type" of a toolset spec.
const Type = "tavily"

// DefaultBaseURL is Tavily's API.
const DefaultBaseURL = "https://api.tavily.com"

// Auth modes, the "auth" setting.
const (
	AuthAuto    = "auto"
	AuthKey     = "key"
	AuthKeyless = "keyless"
)

const (
	defaultKeyEnv          = "TAVILY_API_KEY"
	defaultTimeout         = 30 * time.Second
	advancedExtractTimeout = 60 * time.Second
	defaultSearchChars     = 4000
	defaultExtractChars    = 20000
	maxContentChars        = 200_000
	maxTimeout             = 5 * time.Minute
)

// Option configures [Factory].
type Option func(*factory)

// WithHTTPClient sets the client requests go through (default: a client on
// [http.DefaultTransport], which honors the proxy environment variables).
func WithHTTPClient(c *http.Client) Option { return func(f *factory) { f.client = c } }

// WithBaseURL sets Tavily's address (default [DefaultBaseURL]; "" keeps the
// default); tests point it at a fake. A base_url setting overrides it for its
// toolset.
func WithBaseURL(u string) Option {
	return func(f *factory) {
		if u != "" {
			f.baseURL = strings.TrimSuffix(u, "/")
		}
	}
}

// WithLookupEnv sets how the key's environment variable is read (default:
// none is ever set, so auto means keyless). cmd/agentkit passes
// [os.LookupEnv].
func WithLookupEnv(lookup func(string) (string, bool)) Option {
	return func(f *factory) { f.lookupEnv = lookup }
}

// WithClock sets the clock the wait before a retry uses (default
// [clock.Real]).
func WithClock(c clock.Clock) Option { return func(f *factory) { f.clock = c } }

// WithLogger sets the logger (default: discard). Build logs each toolset's
// auth mode at info, and requests at debug; never the key.
func WithLogger(l *slog.Logger) Option { return func(f *factory) { f.log = l } }

// Factory returns the "tavily" toolset factory: tavily_search and
// tavily_extract.
func Factory(opts ...Option) toolset.Factory {
	f := &factory{
		client:    &http.Client{},
		baseURL:   DefaultBaseURL,
		lookupEnv: func(string) (string, bool) { return "", false },
		clock:     clock.Real(),
		log:       slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

type factory struct {
	client    *http.Client
	baseURL   string
	lookupEnv func(string) (string, bool)
	clock     clock.Clock
	log       *slog.Logger
}

func (*factory) Type() string { return Type }

type settings struct {
	Auth            string          `json:"auth"`
	BaseURL         string          `json:"base_url"`
	APIKeyEnv       string          `json:"api_key_env"`
	Timeout         string          `json:"timeout"`
	MaxContentChars contentLimits   `json:"max_content_chars"`
	SearchDefaults  searchDefaults  `json:"search_defaults"`
	ExtractDefaults extractDefaults `json:"extract_defaults"`
}

type contentLimits struct {
	Search  int `json:"search"`
	Extract int `json:"extract"`
}

type searchDefaults struct {
	SearchDepth       string `json:"search_depth"`
	Topic             string `json:"topic"`
	ChunksPerSource   int    `json:"chunks_per_source"`
	IncludeAnswer     bool   `json:"include_answer"`
	ModelChoosesDepth bool   `json:"model_chooses_depth"`
}

type extractDefaults struct {
	ExtractDepth string `json:"extract_depth"`
	Format       string `json:"format"`
}

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

	authModes     = []string{AuthAuto, AuthKey, AuthKeyless}
	searchDepths  = []string{"basic", "advanced", "fast", "ultra-fast"}
	topics        = []string{"general", "news", "finance"}
	timeRanges    = []string{"day", "week", "month", "year"}
	extractDepths = []string{"basic", "advanced"}
	formats       = []string{"markdown", "text"}
)

func (f *factory) Build(_ context.Context, raw json.RawMessage) ([]tool.Tool, error) {
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	key, err := f.resolveKey(s)
	if err != nil {
		return nil, err
	}
	mode := AuthKeyless
	if key != "" {
		mode = AuthKey
	}
	f.log.Info("tavily toolset auth", "mode", mode, "api_key_env", s.APIKeyEnv)

	baseURL := f.baseURL
	if s.BaseURL != "" {
		baseURL = s.BaseURL
	}
	c := &client{
		http:    f.client,
		baseURL: baseURL,
		key:     key,
		keyEnv:  s.APIKeyEnv,
		clock:   f.clock,
		log:     f.log,
	}
	return newTools(c, s)
}

// decodeSettings decodes and validates settings, filling in the defaults.
func decodeSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if err := refuseLiteralKey(raw); err != nil {
		return s, err
	}
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return s, err
	}
	bad := func(format string, args ...any) (settings, error) {
		return s, fmt.Errorf("%w: tavily: %s", toolset.ErrInvalidSettings, fmt.Sprintf(format, args...))
	}

	if s.Auth == "" {
		s.Auth = AuthAuto
	}
	if s.APIKeyEnv == "" {
		s.APIKeyEnv = defaultKeyEnv
	}
	if s.MaxContentChars.Search == 0 {
		s.MaxContentChars.Search = defaultSearchChars
	}
	if s.MaxContentChars.Extract == 0 {
		s.MaxContentChars.Extract = defaultExtractChars
	}
	if s.SearchDefaults.SearchDepth == "" {
		s.SearchDefaults.SearchDepth = "basic"
	}
	if s.ExtractDefaults.ExtractDepth == "" {
		s.ExtractDefaults.ExtractDepth = "basic"
	}
	if s.ExtractDefaults.Format == "" {
		s.ExtractDefaults.Format = "markdown"
	}

	switch {
	case !oneOf(s.Auth, authModes):
		return bad("auth %q: want one of %s", s.Auth, strings.Join(authModes, ", "))
	case !envNamePattern.MatchString(s.APIKeyEnv):
		// The value is not echoed: it may be a key pasted in the wrong place.
		return bad("api_key_env must name an environment variable (letters, digits and _), not hold the key")
	case s.MaxContentChars.Search < 100 || s.MaxContentChars.Search > maxContentChars,
		s.MaxContentChars.Extract < 100 || s.MaxContentChars.Extract > maxContentChars:
		return bad("max_content_chars: search and extract must be in 100..%d", maxContentChars)
	case !oneOf(s.SearchDefaults.SearchDepth, searchDepths):
		return bad("search_defaults.search_depth %q: want one of %s", s.SearchDefaults.SearchDepth, strings.Join(searchDepths, ", "))
	case s.SearchDefaults.Topic != "" && !oneOf(s.SearchDefaults.Topic, topics):
		return bad("search_defaults.topic %q: want one of %s", s.SearchDefaults.Topic, strings.Join(topics, ", "))
	case s.SearchDefaults.ChunksPerSource < 0 || s.SearchDefaults.ChunksPerSource > 3:
		return bad("search_defaults.chunks_per_source %d: want 1..3", s.SearchDefaults.ChunksPerSource)
	case !oneOf(s.ExtractDefaults.ExtractDepth, extractDepths):
		return bad("extract_defaults.extract_depth %q: want one of %s", s.ExtractDefaults.ExtractDepth, strings.Join(extractDepths, ", "))
	case !oneOf(s.ExtractDefaults.Format, formats):
		return bad("extract_defaults.format %q: want one of %s", s.ExtractDefaults.Format, strings.Join(formats, ", "))
	}
	if s.BaseURL != "" {
		u, ok := checkBaseURL(s.BaseURL)
		if !ok {
			// Not echoed: a URL can carry credentials.
			return bad("base_url must be an https URL with a host and no user, query or fragment (http only for localhost or a loopback IP)")
		}
		s.BaseURL = u
	}
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil || d <= 0 || d > maxTimeout {
			return bad("timeout %q: want a duration such as \"30s\", at most %v", s.Timeout, maxTimeout)
		}
	}
	return s, nil
}

// checkBaseURL accepts an https URL, or an http one whose host is localhost
// or a loopback IP (a local replay server), and returns it without a trailing
// slash. The key goes wherever base_url points, hence https.
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

// refuseLiteralKey fails settings that hold a key instead of naming its
// environment variable. DecodeSettings would refuse the unknown field too,
// but this says what to do instead, and never echoes the value.
func refuseLiteralKey(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil // DecodeSettings reports what is wrong with it
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for name := range fields {
		if strings.EqualFold(name, "api_key") || strings.EqualFold(name, "apikey") || strings.EqualFold(name, "key") {
			return fmt.Errorf("%w: tavily: a literal %q is not accepted, because config files get committed; "+
				"put the key in an environment variable and name it in \"api_key_env\" (default %s)",
				toolset.ErrInvalidSettings, name, defaultKeyEnv)
		}
	}
	return nil
}

// resolveKey returns the key to send, or "" for keyless.
func (f *factory) resolveKey(s settings) (string, error) {
	if s.Auth == AuthKeyless {
		return "", nil
	}
	key, _ := f.lookupEnv(s.APIKeyEnv)
	key = strings.TrimSpace(key)
	if key == "" && s.Auth == AuthKey {
		return "", fmt.Errorf("%w: tavily: auth is %q but $%s is not set", toolset.ErrInvalidSettings, AuthKey, s.APIKeyEnv)
	}
	return key, nil
}

// timeout is the limit on one request to endpoint.
func (s settings) timeout(advancedExtract bool) time.Duration {
	if s.Timeout != "" {
		d, _ := time.ParseDuration(s.Timeout) // validated
		return d
	}
	if advancedExtract {
		return advancedExtractTimeout
	}
	return defaultTimeout
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}
