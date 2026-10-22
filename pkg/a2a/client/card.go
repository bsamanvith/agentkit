package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/a2a"
)

// ErrInvalidCard reports an Agent Card this client cannot use: not a card,
// a required field missing, or no interface it can call.
var ErrInvalidCard = errors.New("invalid A2A agent card")

// Limits of a card.
const (
	// MaxCardBytes is the largest card read.
	MaxCardBytes = 1 << 20
	maxNameRunes = 256
)

// FetchCard reads the Agent Card at cardURL with cfg's header and client,
// and validates it with [ValidateCard]. The URL must be https, or http to
// this machine.
func FetchCard(ctx context.Context, cardURL string, cfg Config) (*a2a.AgentCard, error) {
	u, err := checkURL(cardURL)
	if err != nil {
		return nil, err
	}
	t := newTransport(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCard, err)
	}
	req.Header.Set("Accept", "application/json")
	body, err := t.do(req, MaxCardBytes)
	if err != nil {
		return nil, fmt.Errorf("agent card %s: %w", redactURL(u), err)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("%w: %s is not an agent card: %s", ErrInvalidCard, redactURL(u), jsonProblem(err))
	}
	if err := ValidateCard(&card, cardURL); err != nil {
		return nil, err
	}
	return &card, nil
}

// ValidateCard checks a card read from cardURL: a name, a description and a
// version, every interface with a binding, a protocol version and an
// absolute http(s) URL, and at least one interface this client can use (see
// the package documentation). The error matches [ErrInvalidCard] and says
// what is wrong.
func ValidateCard(card *a2a.AgentCard, cardURL string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidCard}, args...)...)
	}
	if card == nil {
		return bad("no card")
	}
	origin, err := checkURL(cardURL)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(card.Name) == "":
		return bad("name is required")
	case utf8.RuneCountInString(card.Name) > maxNameRunes:
		return bad("name is longer than %d characters", maxNameRunes)
	case strings.TrimSpace(card.Description) == "":
		return bad("description is required")
	case strings.TrimSpace(card.Version) == "":
		return bad("version is required")
	case len(card.SupportedInterfaces) == 0:
		return bad("supportedInterfaces is empty")
	}
	if _, err := pickInterface(card, origin); err != nil {
		return err
	}
	return nil
}

// pickInterface is the interface the client calls: JSON-RPC before
// HTTP+JSON, version 1.x, on the card's origin.
func pickInterface(card *a2a.AgentCard, origin *url.URL) (iface, error) {
	var found []iface
	elsewhere := false
	for i, in := range card.SupportedInterfaces {
		if in.URL == "" || in.ProtocolBinding == "" || in.ProtocolVersion == "" {
			return iface{}, fmt.Errorf("%w: supportedInterfaces[%d] needs a url, a protocolBinding and a protocolVersion", ErrInvalidCard, i)
		}
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return iface{}, fmt.Errorf("%w: supportedInterfaces[%d].url is not an http(s) URL", ErrInvalidCard, i)
		}
		if in.ProtocolBinding != a2a.BindingJSONRPC && in.ProtocolBinding != a2a.BindingHTTPJSON {
			continue // gRPC, or a binding to come
		}
		if major, _, _ := strings.Cut(in.ProtocolVersion, "."); major != "1" {
			continue
		}
		if !sameOrigin(u, origin) {
			elsewhere = true
			continue
		}
		found = append(found, iface{url: strings.TrimRight(u.String(), "/"), binding: in.ProtocolBinding})
	}
	for _, b := range []string{a2a.BindingJSONRPC, a2a.BindingHTTPJSON} {
		for _, f := range found {
			if f.binding == b {
				return f, nil
			}
		}
	}
	if elsewhere {
		return iface{}, fmt.Errorf("%w: its interfaces are on another origin than the card (%s): the credential goes to the card's origin only", ErrInvalidCard, origin.Scheme+"://"+origin.Host)
	}
	return iface{}, fmt.Errorf("%w: no interface speaks JSON-RPC or HTTP+JSON at protocol version 1", ErrInvalidCard)
}

type iface struct{ url, binding string }

// checkURL parses an agent's URL: https, or http to this machine, no user
// information.
func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return nil, fmt.Errorf("%w: %q is not an http(s) URL", ErrInvalidCard, redactRaw(raw))
	case u.User != nil:
		return nil, fmt.Errorf("%w: the URL must not carry a user or password", ErrInvalidCard)
	case u.Scheme == "http" && !loopback(u.Hostname()):
		return nil, fmt.Errorf("%w: %s: plain http only to this machine; use https", ErrInvalidCard, redactURL(u))
	}
	return u, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(hostPort(a), hostPort(b))
}

func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return u.Hostname() + ":443"
	}
	return u.Hostname() + ":80"
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redactURL is u without its query, fragment or user information, for
// messages: a query can carry a key.
func redactURL(u *url.URL) string {
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

func redactRaw(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return redactURL(u)
	}
	if i := strings.IndexAny(raw, "?#@"); i >= 0 {
		return raw[:i]
	}
	return raw
}

func jsonProblem(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return te.Field + " has the wrong type"
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "not JSON"
	}
	return "not the expected JSON"
}

func isJSON(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}
