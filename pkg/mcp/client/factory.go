package client

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
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Type is the toolset type of an external MCP server.
const Type = "mcp"

// Option configures [Factory].
type Option func(*factory)

// WithLookupEnv sets how variables named in settings are read (default: none
// is set). The binary passes os.LookupEnv.
func WithLookupEnv(fn func(string) (string, bool)) Option {
	return func(f *factory) { f.lookupEnv = fn }
}

// WithHTTPClient sets the HTTP client for the HTTP transports.
func WithHTTPClient(hc *http.Client) Option { return func(f *factory) { f.httpClient = hc } }

// WithLogger sets the logger (default: none).
func WithLogger(l *slog.Logger) Option { return func(f *factory) { f.log = l } }

type factory struct {
	lookupEnv  func(string) (string, bool)
	httpClient *http.Client
	log        *slog.Logger
}

// Factory is the toolset type "mcp": one external MCP server's tools, under
// their own names. Settings:
//
//	url              the server's endpoint (http and sse transports)
//	transport        auto (default: Streamable HTTP, then HTTP+SSE), http, sse or stdio
//	command, args    the server to run (stdio only)
//	env              names of variables passed to the command (stdio only; PATH
//	                 and HOME always are, nothing else is)
//	headers          sent with every request; ${NAME} in a value is replaced by
//	                 the variable NAME. A header that carries a secret
//	                 (Authorization, or a name with key, token, secret or auth
//	                 in it) must take its value from a variable.
//	timeout          per call (default 60s)
//	connect_timeout  connecting and listing tools (default 15s)
//	max_result_bytes the largest result, as JSON (default 1 MiB, at most 16 MiB)
//
// Building connects and lists the tools, so a server that does not answer
// fails the toolset; with "optional": true it is skipped with the reason and
// the agents that use other toolsets still run. Tools are fixed when the
// toolset is built: when the server says its list changed, the change is
// logged and takes effect on the next start. A tool whose definition cannot
// be one of ours (an invalid name) is left out, with a warning.
//
// Two servers with a tool of the same name are two toolsets; an agent using
// both sets "prefix_tool_names" to call them "<toolset>__<tool>".
func Factory(opts ...Option) toolset.Factory {
	f := &factory{lookupEnv: func(string) (string, bool) { return "", false }}
	for _, o := range opts {
		o(f)
	}
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
	}
	return f
}

func (*factory) Type() string { return Type }

type settings struct {
	URL            string            `json:"url"`
	Transport      Transport         `json:"transport"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            []string          `json:"env"`
	Headers        map[string]string `json:"headers"`
	Timeout        duration          `json:"timeout"`
	ConnectTimeout duration          `json:"connect_timeout"`
	MaxResultBytes int               `json:"max_result_bytes"`
}

const maxResultLimit = 16 << 20

func (f *factory) Build(ctx context.Context, raw json.RawMessage) ([]tool.Tool, error) {
	cfg, where, err := f.config(raw)
	if err != nil {
		return nil, err
	}
	c, err := Connect(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("MCP server %s: %w", where, err)
	}
	lctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	defs, err := c.Tools(lctx)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("MCP server %s: list tools: %w", where, err)
	}
	tools := make([]tool.Tool, 0, len(defs))
	for _, d := range defs {
		t, err := c.Wrap(d)
		if err != nil {
			f.log.Warn("mcp: tool left out", "server", where, "error", err)
			continue
		}
		tools = append(tools, t)
	}
	if len(tools) == 0 {
		_ = c.Close()
		return nil, fmt.Errorf("MCP server %s offers no tools agentkit can use (%d listed)", where, len(defs))
	}
	f.log.Info("mcp: connected", "server", where, "transport", c.Transport(), "tools", len(tools))
	go func() {
		for {
			select {
			case <-c.ToolsChanged():
				f.log.Warn("mcp: the server's tools changed; restart to use the new list", "server", where)
			case <-c.Done():
				return
			}
		}
	}()
	return tools, nil
}

var (
	envNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envRefRE     = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	headerNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	secretNameRE = regexp.MustCompile(`(?i)authorization|cookie|key|token|secret|auth|password`)
)

// config validates settings and resolves their variables. where names the
// server in messages: its URL without query or user info, or its command.
func (f *factory) config(raw json.RawMessage) (Config, string, error) {
	var s settings
	if err := toolset.DecodeSettings(raw, &s); err != nil {
		return Config{}, "", err
	}
	bad := func(format string, args ...any) (Config, string, error) {
		return Config{}, "", fmt.Errorf("%w: "+format, append([]any{toolset.ErrInvalidSettings}, args...)...)
	}
	cfg := Config{
		Transport: s.Transport, CallTimeout: time.Duration(s.Timeout),
		ConnectTimeout: time.Duration(s.ConnectTimeout), MaxResultBytes: s.MaxResultBytes,
		HTTPClient: f.httpClient,
	}
	if s.Timeout < 0 || s.ConnectTimeout < 0 {
		return bad("timeouts must be positive")
	}
	if s.MaxResultBytes < 0 || s.MaxResultBytes > maxResultLimit {
		return bad("max_result_bytes must be between 1 and %d", maxResultLimit)
	}
	var where string
	switch s.Transport {
	case TransportStdio:
		if s.Command == "" || s.URL != "" || len(s.Headers) > 0 {
			return bad(`transport "stdio" takes a command (and args, env), not a url or headers`)
		}
		cfg.Command, cfg.Args = s.Command, s.Args
		for _, k := range []string{"PATH", "HOME"} {
			if v, ok := f.lookupEnv(k); ok {
				cfg.Env = append(cfg.Env, k+"="+v)
			}
		}
		for _, name := range s.Env {
			if !envNameRE.MatchString(name) {
				return bad("env: %q is not a variable name", name)
			}
			v, ok := f.lookupEnv(name)
			if !ok {
				return bad("env: $%s is not set", name)
			}
			cfg.Env = append(cfg.Env, name+"="+v)
		}
		where = "command " + s.Command
	case "", TransportAuto, TransportHTTP, TransportSSE:
		if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 {
			return bad(`command, args and env are for transport "stdio"`)
		}
		u, err := url.Parse(s.URL)
		switch {
		case s.URL == "":
			return bad("url is required")
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			return bad("url %q must be an http or https URL", redactURL(s.URL))
		case u.User != nil:
			return bad("url must not carry a user or password: put credentials in headers, from a variable")
		case u.Scheme == "http" && len(s.Headers) > 0 && !loopback(u.Hostname()):
			return bad("url %s: headers are only sent over https (or http to this machine)", redactURL(s.URL))
		}
		cfg.URL, where = s.URL, redactURL(s.URL)
		if len(s.Headers) > 0 {
			cfg.Header = http.Header{}
		}
		for name, value := range s.Headers {
			if !headerNameRE.MatchString(name) {
				return bad("headers: %q is not a header name", name)
			}
			if secretNameRE.MatchString(name) && !envRefRE.MatchString(value) {
				return bad("headers: %s carries a secret: take its value from a variable, as in \"Bearer ${NAME}\"", name)
			}
			v, err := f.expand(value)
			if err != nil {
				return bad("headers: %s: %v", name, err)
			}
			cfg.Header.Set(name, v)
		}
	default:
		return bad("transport %q must be auto, http, sse or stdio", s.Transport)
	}
	return cfg, where, nil
}

// expand replaces ${NAME} with the variable NAME, which must be set.
func (f *factory) expand(value string) (string, error) {
	var missing []string
	out := envRefRE.ReplaceAllStringFunc(value, func(ref string) string {
		name := envRefRE.FindStringSubmatch(ref)[1]
		v, ok := f.lookupEnv(name)
		if !ok || v == "" {
			missing = append(missing, "$"+name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("%s not set", strings.Join(missing, ", "))
	}
	if strings.ContainsAny(out, "\r\n") {
		return "", errors.New("the value has a line break")
	}
	return out, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

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
