package exa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/connectors/internal/apikey"
	"github.com/bsamanvith/agentkit/connectors/internal/httpx"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the toolset type of this connector.
const Type = "exa"

// DefaultBaseURL is Exa's API.
const DefaultBaseURL = "https://api.exa.ai"

// Errors of the connector, checked with errors.Is. Tool calls wrap them in a
// [tool.Error] of kind [tool.ErrToolFailed].
var (
	// ErrMissingKey reports, from Build, that the environment variable named
	// by api_key_env is unset or empty (for a toolset without a credential
	// slot).
	ErrMissingKey = errors.New("exa API key not set")
	// ErrKeyRejected reports a 401 or 403 from Exa.
	ErrKeyRejected = errors.New("exa rejected the API key")
	// ErrRateLimited reports a 429 that persisted after the one retry, or
	// asked for a wait longer than the connector honors.
	ErrRateLimited = errors.New("exa rate limited the request; " + httpx.RateLimitAdvice)
	// ErrUpstream reports any other failure of the call: a 5xx or other
	// unexpected status, a transport error or timeout, an unreadable body.
	ErrUpstream = errors.New("exa request failed")
	// ErrResponseTooLarge reports a response body over the 8 MB limit.
	ErrResponseTooLarge = errors.New("exa response too large")
)

// Limits and defaults (E10).
const (
	maxResponseBytes      = httpx.MaxBody
	maxErrorMessageChars  = 300
	defaultTimeout        = 30 * time.Second
	maxTimeout            = 5 * time.Minute
	defaultSearchChars    = 4000
	defaultExtractChars   = 20000
	maxCharsSetting       = 1_000_000
	maxMetadataChars      = 1000
	maxURLChars           = 4096
	defaultMaxResults     = 5
	maxMaxResults         = 20
	maxExtractURLs        = 10
	maxRequestURLChars    = 2048
	searchTruncationHint  = "call exa_extract on this URL for the full page"
	extractTruncationHint = "call exa_extract again with a query to get the passages that matter"
	highlightSeparator    = "\n…\n"
	defaultAPIKeyEnv      = "EXA_API_KEY"
	defaultSearchType     = "auto"
	contentsHighlights    = "highlights"
	contentsText          = "text"
	minMaxAgeHours        = -1
	maxMaxAgeHours        = 720
	userAgent             = "agentkit-exa-connector"
	searchPath            = "/search"
	contentsPath          = "/contents"
	categoryCompany       = "company"
	categoryPeople        = "people"
	dateOnlyLayout        = "2006-01-02"
	exaDateTimeLayout     = "2006-01-02T15:04:05.000Z"
	endOfDay              = 24*time.Hour - time.Millisecond
	apiKeySettingRejected = "api_key is not accepted: give the toolset a credential slot that each user connects their key to, or put the key in an environment variable and name it in api_key_env (default " + defaultAPIKeyEnv + ")"
)

// searchTypes and categories are the enums of Exa's OpenAPI spec
// (SearchRequest.type and .category), fetched 2026-10-12.
var (
	searchTypes = []string{"instant", "fast", "auto", "deep-lite", "deep", "deep-reasoning"}
	categories  = []string{"company", "publication", "news", "personal site", "financial report", "people"}
)

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Option configures [Factory].
type Option func(*factory)

// WithHTTPClient sets the HTTP client for calls to Exa (default: a client
// with no timeout of its own; each call is limited by the timeout setting).
func WithHTTPClient(c *http.Client) Option { return func(f *factory) { f.client = c } }

// WithBaseURL sets Exa's base URL (default [DefaultBaseURL]; "" keeps the
// default); tests point it at a fake.
func WithBaseURL(u string) Option {
	return func(f *factory) {
		if u != "" {
			f.baseURL = u
		}
	}
}

// WithLookupEnv sets how the API key's environment variable is read;
// cmd/agentkit passes [os.LookupEnv]. Without it every variable reads as
// unset, so Build fails with [ErrMissingKey]: the connector never reads the
// process environment on its own.
func WithLookupEnv(fn func(string) (string, bool)) Option {
	return func(f *factory) { f.lookupEnv = fn }
}

// WithClock sets the clock that times retry waits (default [clock.Real]).
func WithClock(c clock.Clock) Option { return func(f *factory) { f.clock = c } }

// WithLogger sets the logger (default: discard). It never sees the key or
// response bodies.
func WithLogger(l *slog.Logger) Option { return func(f *factory) { f.log = l } }

// Factory returns the "exa" toolset factory. See the package documentation
// for its settings and tools.
func Factory(opts ...Option) toolset.Factory {
	f := &factory{
		baseURL:   DefaultBaseURL,
		lookupEnv: func(string) (string, bool) { return "", false },
	}
	for _, o := range opts {
		o(f)
	}
	if f.client == nil {
		f.client = &http.Client{}
	}
	if f.clock == nil {
		f.clock = clock.Real()
	}
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
	}
	if f.lookupEnv == nil {
		f.lookupEnv = func(string) (string, bool) { return "", false }
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

// settings is the JSON form of the toolset's settings.
type settings struct {
	APIKeyEnv       string          `json:"api_key_env"`
	BaseURL         string          `json:"base_url"`
	Timeout         duration        `json:"timeout"`
	MaxContentChars contentCaps     `json:"max_content_chars"`
	SearchDefaults  searchDefaults  `json:"search_defaults"`
	ExtractDefaults extractDefaults `json:"extract_defaults"`
	// keyEnvSet: api_key_env was given, not defaulted.
	keyEnvSet bool
}

type contentCaps struct {
	Search  int `json:"search"`
	Extract int `json:"extract"`
}

type searchDefaults struct {
	Type             string `json:"type"`
	ModelChoosesType bool   `json:"model_chooses_type"`
	Contents         string `json:"contents"`
	MaxCharacters    int    `json:"max_characters"`
	Category         string `json:"category"`
}

type extractDefaults struct {
	MaxCharacters int  `json:"max_characters"`
	MaxAgeHours   *int `json:"max_age_hours"`
}

// duration is a time.Duration written as a string ("30s").
type duration time.Duration

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}

func (f *factory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	raw := in.Settings
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	base := s.BaseURL // checked by decodeSettings
	if base == "" {
		if err := checkBaseURL(f.baseURL); err != nil {
			return nil, fmt.Errorf("exa: WithBaseURL: %w", err)
		}
		base = f.baseURL
	}
	keys, err := f.keys(s, in.Credentials)
	if err != nil {
		return nil, err
	}
	c := &client{
		http:    f.client,
		base:    strings.TrimSuffix(base, "/"),
		keys:    keys,
		clock:   f.clock,
		log:     f.log,
		timeout: time.Duration(s.Timeout),
	}
	search, err := newSearchTool(c, s)
	if err != nil {
		return nil, err
	}
	extract, err := tool.Func("exa_extract", extractDescription, func(ctx context.Context, in extractIn) (extractOut, error) {
		return extractCall(ctx, c, s, in)
	}, tool.WithMetadata(extractMeta))
	if err != nil {
		return nil, err
	}
	keyFrom := "env " + s.APIKeyEnv
	if keys.PerCaller() {
		keyFrom = "credential slot"
	}
	f.log.Info("exa toolset built", "api_key", keyFrom, "search_type", s.SearchDefaults.Type,
		"model_chooses_type", s.SearchDefaults.ModelChoosesType, "contents", s.SearchDefaults.Contents)
	return []tool.Tool{classified{search}, classified{extract}}, nil
}

// keys is where the toolset's calls get their key: the caller's credential
// when the spec names a slot (src), else the environment variable that
// api_key_env names, read once now.
func (f *factory) keys(s settings, src credentials.Source) (apikey.Key, error) {
	if src != nil {
		if s.keyEnvSet {
			return apikey.Key{}, fmt.Errorf("%w: api_key_env and the toolset's credential slot are two places for the key: give one", toolset.ErrInvalidSettings)
		}
		return apikey.FromSource(src), nil
	}
	key, ok := f.lookupEnv(s.APIKeyEnv)
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return apikey.Key{}, fmt.Errorf("%w: environment variable %s is unset or empty (it must hold the Exa API key; api_key_env names it, or give the toolset a credential slot)", ErrMissingKey, s.APIKeyEnv)
	}
	return apikey.Static(key), nil
}

// decodeSettings decodes and validates settings, filling in defaults.
func decodeSettings(raw json.RawMessage) (settings, error) {
	var s settings
	// A literal key gets its own message (and its value is never echoed):
	// config files get committed, so the key must come from the environment.
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) == nil {
		for k := range probe {
			if strings.EqualFold(k, "api_key") {
				return s, fmt.Errorf("%w: %s", toolset.ErrInvalidSettings, apiKeySettingRejected)
			}
		}
	}
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return s, err
	}
	bad := func(format string, args ...any) (settings, error) {
		return s, fmt.Errorf("%w: %s", toolset.ErrInvalidSettings, fmt.Sprintf(format, args...))
	}

	s.keyEnvSet = s.APIKeyEnv != ""
	if !s.keyEnvSet {
		s.APIKeyEnv = defaultAPIKeyEnv
	}
	if !envNameRE.MatchString(s.APIKeyEnv) {
		return bad("api_key_env %q is not an environment variable name", s.APIKeyEnv)
	}
	if s.BaseURL != "" {
		if err := checkBaseURL(s.BaseURL); err != nil {
			return bad("base_url: %v", err)
		}
	}
	switch t := time.Duration(s.Timeout); {
	case t == 0:
		s.Timeout = duration(defaultTimeout)
	case t < 0 || t > maxTimeout:
		return bad("timeout %s out of range (0s, %s]", t, maxTimeout)
	}
	if s.MaxContentChars.Search == 0 {
		s.MaxContentChars.Search = defaultSearchChars
	}
	if s.MaxContentChars.Extract == 0 {
		s.MaxContentChars.Extract = defaultExtractChars
	}
	if !inRange(s.MaxContentChars.Search, 1, maxCharsSetting) || !inRange(s.MaxContentChars.Extract, 1, maxCharsSetting) {
		return bad("max_content_chars.search and .extract must be between 1 and %d", maxCharsSetting)
	}

	sd := &s.SearchDefaults
	if sd.Type == "" {
		sd.Type = defaultSearchType
	}
	if !slices.Contains(searchTypes, sd.Type) {
		return bad("search_defaults.type %q is not one of %s", sd.Type, strings.Join(searchTypes, ", "))
	}
	if sd.Contents == "" {
		sd.Contents = contentsHighlights
	}
	if sd.Contents != contentsHighlights && sd.Contents != contentsText {
		return bad("search_defaults.contents %q is not %q or %q", sd.Contents, contentsHighlights, contentsText)
	}
	if !inRange(sd.MaxCharacters, 0, maxCharsSetting) {
		return bad("search_defaults.max_characters must be between 0 (Exa's default) and %d", maxCharsSetting)
	}
	if sd.MaxCharacters == 0 && sd.Contents == contentsText {
		sd.MaxCharacters = s.MaxContentChars.Search // no point fetching text we cut off
	}
	if sd.Category != "" && !slices.Contains(categories, sd.Category) {
		return bad("search_defaults.category %q is not one of %s", sd.Category, quoteAll(categories))
	}

	ed := &s.ExtractDefaults
	if ed.MaxCharacters == 0 {
		ed.MaxCharacters = s.MaxContentChars.Extract
	}
	if !inRange(ed.MaxCharacters, 1, maxCharsSetting) {
		return bad("extract_defaults.max_characters must be between 1 and %d", maxCharsSetting)
	}
	if ed.MaxAgeHours != nil && !inRange(*ed.MaxAgeHours, minMaxAgeHours, maxMaxAgeHours) {
		return bad("extract_defaults.max_age_hours must be between %d and %d", minMaxAgeHours, maxMaxAgeHours)
	}
	return s, nil
}

// checkBaseURL accepts an https URL, or http to a loopback host (a local
// replay server or fake): the key must not cross a network in the clear.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not an absolute URL without credentials, query or fragment", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("%q: plain http is allowed only to a loopback host; use https", raw)
	default:
		return fmt.Errorf("%q is not an http or https URL", raw)
	}
}

func inRange(v, lo, hi int) bool { return v >= lo && v <= hi }

func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// invalidInput is the error for model input that the input schema cannot
// express (a range, an enum, a URL). No request is sent.
func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", tool.ErrInvalidInput, fmt.Sprintf(format, args...))
}

// classified reports errors from a tool's function that wrap
// [tool.ErrInvalidInput] as that kind instead of [tool.ErrToolFailed], which
// [tool.Func] gives every error its function returns.
type classified struct{ tool.Tool }

func (c classified) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	out, err := c.Tool.Invoke(ctx, input)
	var te *tool.Error
	if errors.As(err, &te) && errors.Is(te.Kind, tool.ErrToolFailed) && errors.Is(te.Err, tool.ErrInvalidInput) {
		return out, &tool.Error{Tool: te.Tool, Kind: tool.ErrInvalidInput, Err: te.Err}
	}
	return out, err
}
