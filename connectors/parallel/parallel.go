package parallel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the factory type config uses to select this connector.
const Type = "parallel"

// Defaults for omitted settings.
const (
	defaultAPIKeyEnv    = "PARALLEL_API_KEY"
	defaultBaseURL      = "https://api.parallel.ai"
	defaultTimeout      = 30 * time.Second
	defaultSearchChars  = 4000
	defaultExtractChars = 20000
	defaultMode         = "fast"
)

// modes are the search modes Parallel accepts.
var modes = []string{"turbo", "fast", "basic", "advanced"}

// Option configures [Factory].
type Option func(*factory)

// WithHTTPClient sets the HTTP client (default: a new client with no timeout
// of its own; the timeout setting bounds each attempt).
func WithHTTPClient(c *http.Client) Option { return func(f *factory) { f.client = c } }

// WithBaseURL sets the API's base URL (default https://api.parallel.ai). A
// base_url setting takes precedence.
func WithBaseURL(u string) Option { return func(f *factory) { f.baseURL = u } }

// WithLookupEnv sets how the key's environment variable is read. Without it
// no variable is ever found, so the binary passes os.LookupEnv.
func WithLookupEnv(fn func(string) (string, bool)) Option {
	return func(f *factory) { f.lookupEnv = fn }
}

// WithClock sets the clock that times the wait before a retry (default the
// real clock).
func WithClock(c clock.Clock) Option { return func(f *factory) { f.clock = c } }

// WithLogger sets the logger for build information and Parallel's warnings
// (default: discard). The key is never logged.
func WithLogger(l *slog.Logger) Option { return func(f *factory) { f.log = l } }

// Factory returns the "parallel" toolset factory.
func Factory(opts ...Option) toolset.Factory {
	f := &factory{baseURL: defaultBaseURL}
	for _, o := range opts {
		o(f)
	}
	// Defaults go in after the options, so that a nil option means the
	// default rather than a nil the toolset would dereference.
	if f.client == nil {
		f.client = &http.Client{}
	}
	if f.lookupEnv == nil {
		f.lookupEnv = func(string) (string, bool) { return "", false }
	}
	if f.clock == nil {
		f.clock = clock.Real()
	}
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
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

func (f *factory) Type() string { return Type }

// settings is the JSON form of the toolset's settings.
type settings struct {
	APIKeyEnv       string           `json:"api_key_env"`
	BaseURL         string           `json:"base_url"`
	Timeout         duration         `json:"timeout"`
	MaxContentChars *contentLimits   `json:"max_content_chars"`
	SearchDefaults  *searchDefaults  `json:"search_defaults"`
	ExtractDefaults *extractDefaults `json:"extract_defaults"`
}

type contentLimits struct {
	Search  int `json:"search"`
	Extract int `json:"extract"`
}

type searchDefaults struct {
	Mode              string       `json:"mode"`
	AllowModelMode    bool         `json:"allow_model_mode"`
	MaxCharsPerResult int          `json:"max_chars_per_result"`
	Location          string       `json:"location"`
	FetchPolicy       *fetchPolicy `json:"fetch_policy"`
}

type extractDefaults struct {
	FullContent bool         `json:"full_content"`
	FetchPolicy *fetchPolicy `json:"fetch_policy"`
}

// fetchPolicy is Parallel's FetchPolicy, sent as configured.
type fetchPolicy struct {
	MaxAgeSeconds        int     `json:"max_age_seconds,omitempty"`
	TimeoutSeconds       float64 `json:"timeout_seconds,omitempty"`
	DisableCacheFallback bool    `json:"disable_cache_fallback,omitempty"`
}

// duration is a time.Duration written as a Go duration string ("30s").
type duration time.Duration

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration is a string such as \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}

var (
	envNameRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	locationRE = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// config is the validated settings with defaults applied.
type config struct {
	keyEnv       string
	baseURL      string
	timeout      time.Duration
	searchChars  int
	extractChars int
	mode         string
	allowMode    bool
	maxCharsPer  int
	location     string
	searchFetch  *fetchPolicy
	fullContent  bool
	extractFetch *fetchPolicy
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", toolset.ErrInvalidSettings, Type, fmt.Sprintf(format, args...))
}

func (f *factory) decode(raw json.RawMessage) (config, error) {
	// A literal key gets its own message: it is the mistake that matters.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err == nil {
		for k := range top {
			if strings.EqualFold(k, "api_key") {
				return config{}, invalid(`"api_key" is not accepted, because config files get committed: put the key in an environment variable and name it in "api_key_env" (default %s)`, defaultAPIKeyEnv)
			}
		}
	}
	var s settings
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return config{}, fmt.Errorf("%s: %w", Type, err)
	}
	c := config{
		keyEnv:       defaultAPIKeyEnv,
		baseURL:      f.baseURL,
		timeout:      defaultTimeout,
		searchChars:  defaultSearchChars,
		extractChars: defaultExtractChars,
		mode:         defaultMode,
	}
	if s.APIKeyEnv != "" {
		if !envNameRE.MatchString(s.APIKeyEnv) {
			return config{}, invalid("api_key_env %q is not an environment variable name", s.APIKeyEnv)
		}
		c.keyEnv = s.APIKeyEnv
	}
	if s.BaseURL != "" {
		c.baseURL = s.BaseURL
	}
	u, err := checkBaseURL(c.baseURL)
	if err != nil {
		return config{}, invalid("base_url: %v", err)
	}
	c.baseURL = u
	if s.Timeout != 0 {
		if s.Timeout < 0 || time.Duration(s.Timeout) > 10*time.Minute {
			return config{}, invalid("timeout %v is out of range (above 0, at most 10m)", time.Duration(s.Timeout))
		}
		c.timeout = time.Duration(s.Timeout)
	}
	if l := s.MaxContentChars; l != nil {
		if l.Search < 0 || l.Extract < 0 || l.Search > 1_000_000 || l.Extract > 1_000_000 {
			return config{}, invalid("max_content_chars values must be between 1 and 1000000 (0 means the default)")
		}
		if l.Search > 0 {
			c.searchChars = l.Search
		}
		if l.Extract > 0 {
			c.extractChars = l.Extract
		}
	}
	if d := s.SearchDefaults; d != nil {
		if d.Mode != "" {
			if !slices.Contains(modes, d.Mode) {
				return config{}, invalid("search_defaults.mode %q is not one of %s", d.Mode, strings.Join(modes, ", "))
			}
			c.mode = d.Mode
		}
		c.allowMode = d.AllowModelMode
		if d.MaxCharsPerResult < 0 {
			return config{}, invalid("search_defaults.max_chars_per_result must not be negative")
		}
		c.maxCharsPer = d.MaxCharsPerResult
		if d.Location != "" {
			if !locationRE.MatchString(d.Location) {
				return config{}, invalid("search_defaults.location %q is not an ISO 3166-1 alpha-2 country code", d.Location)
			}
			c.location = strings.ToLower(d.Location)
		}
		if err := checkFetchPolicy(d.FetchPolicy); err != nil {
			return config{}, invalid("search_defaults.fetch_policy: %v", err)
		}
		c.searchFetch = d.FetchPolicy
	}
	if d := s.ExtractDefaults; d != nil {
		c.fullContent = d.FullContent
		if err := checkFetchPolicy(d.FetchPolicy); err != nil {
			return config{}, invalid("extract_defaults.fetch_policy: %v", err)
		}
		c.extractFetch = d.FetchPolicy
	}
	return c, nil
}

func checkFetchPolicy(p *fetchPolicy) error {
	if p == nil {
		return nil
	}
	if p.MaxAgeSeconds != 0 && p.MaxAgeSeconds < 600 {
		return fmt.Errorf("max_age_seconds is at least 600 (Parallel's minimum)")
	}
	if p.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must not be negative")
	}
	return nil
}

// checkBaseURL accepts an absolute https URL, or http to a loopback host, with
// no credentials, query or fragment. It returns it without a trailing slash.
func checkBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a URL", raw)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q must be scheme://host[/path], with no credentials, query or fragment", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("%q must use https unless the host is loopback", raw)
		}
	default:
		return "", fmt.Errorf("%q must use https", raw)
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Build implements [toolset.Factory]. It makes no network request.
func (f *factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	raw := in.Settings
	c, err := f.decode(raw)
	if err != nil {
		return nil, err
	}
	key, ok := f.lookupEnv(c.keyEnv)
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return nil, fmt.Errorf("%s: the API key is missing: set the environment variable %s", Type, c.keyEnv)
	}
	cl := &client{
		http:    f.client,
		baseURL: c.baseURL,
		key:     key,
		timeout: c.timeout,
		clock:   f.clock,
		log:     f.log,
	}
	search, err := newSearchTool(cl, c)
	if err != nil {
		return nil, err
	}
	extract, err := newExtractTool(cl, c)
	if err != nil {
		return nil, err
	}
	f.log.Info("parallel toolset built", "api_key_env", c.keyEnv, "base_url", c.baseURL,
		"mode", c.mode, "model_picks_mode", c.allowMode, "full_content", c.fullContent)
	return []tool.Tool{search, extract}, nil
}
