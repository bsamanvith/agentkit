package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// ErrProtocol reports a response that is not what A2A says it is: not JSON,
// the wrong shape, or a stream that breaks off.
var ErrProtocol = errors.New("A2A protocol error")

// StatusError is an HTTP failure that carries no A2A error: the agent
// refused the credential (401, 403), is not there (404), or failed (5xx).
// Message is the agent's own text, redacted and cut short.
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("the agent answered HTTP %d", e.StatusCode)
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// maxMessageRunes caps text taken from an error response.
const maxMessageRunes = 300

// transport sends requests with the credential and reads bounded, redacted
// responses.
type transport struct {
	hc      *http.Client
	header  http.Header
	secrets [][]byte // what redact replaces, longest first
	maxBody int64
}

func newTransport(cfg Config) *transport {
	base := cfg.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	hc := *base
	// A redirect could take the credential to another host: never follow.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t := &transport{hc: &hc, header: cfg.Header.Clone(), maxBody: cfg.MaxBodyBytes}
	if t.maxBody <= 0 {
		t.maxBody = DefaultMaxBodyBytes
	}
	for _, vs := range cfg.Header {
		for _, v := range vs {
			t.addSecret(v)
			if _, rest, ok := strings.Cut(v, " "); ok { // "Bearer <token>"
				t.addSecret(strings.TrimSpace(rest))
			}
		}
	}
	// Longest first, so "Bearer <token>" goes as a whole before the token.
	slices.SortStableFunc(t.secrets, func(a, b []byte) int { return len(b) - len(a) })
	return t
}

// minSecret is the shortest value redacted: shorter ones (a scheme name)
// would only garble text.
const minSecret = 8

func (t *transport) addSecret(v string) {
	if len(v) < minSecret {
		return
	}
	t.secrets = append(t.secrets, []byte(v))
	if b, err := json.Marshal(v); err == nil && string(b[1:len(b)-1]) != v {
		t.secrets = append(t.secrets, b[1:len(b)-1]) // as it appears inside JSON
	}
}

var redacted = []byte("[redacted]")

// redact replaces every credential value in b.
func (t *transport) redact(b []byte) []byte {
	for _, s := range t.secrets {
		if bytes.Contains(b, s) {
			b = bytes.ReplaceAll(b, s, redacted)
		}
	}
	return b
}

func (t *transport) redactString(s string) string { return string(t.redact([]byte(s))) }

// send sends req with the credential and returns the response, which is a
// 2xx; any other status is read (bounded) and returned as an error.
func (t *transport) send(req *http.Request, decodeError func(status int, body []byte) error) (*http.Response, error) {
	for k, vs := range t.header {
		req.Header[k] = vs
	}
	req.Header.Set("A2A-Version", "1.0")
	resp, err := t.hc.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// A transport error can quote the URL; nothing else of ours.
		return nil, fmt.Errorf("%w: the request failed: %s", ErrProtocol, t.redactString(cleanTransportError(err)))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	body = t.redact(body)
	if decodeError != nil {
		if err := decodeError(resp.StatusCode, body); err != nil {
			return nil, err
		}
	}
	return nil, &StatusError{StatusCode: resp.StatusCode, Message: errorText(body)}
}

// do sends req and reads its whole body, at most limit bytes, redacted.
func (t *transport) do(req *http.Request, limit int64) ([]byte, error) {
	resp, err := t.send(req, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return t.read(resp, limit)
}

func (t *transport) read(resp *http.Response, limit int64) ([]byte, error) {
	if !isJSON(resp.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("%w: the response is %q, not JSON", ErrProtocol, resp.Header.Get("Content-Type"))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctxErr := resp.Request.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: reading the response failed", ErrProtocol)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: the response is larger than %d bytes", ErrProtocol, limit)
	}
	return t.redact(b), nil
}

// errorText is a short message from an error body: the "message" of a
// JSON-RPC or AIP-193 error, or the start of the text.
func errorText(body []byte) string {
	var v struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	s := ""
	if json.Unmarshal(body, &v) == nil {
		switch {
		case v.Error != nil:
			s = v.Error.Message
		default:
			s = v.Message
		}
	} else if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("<")) {
		s = string(body)
	}
	return cut(strings.TrimSpace(s), maxMessageRunes)
}

// cut keeps at most n runes of s, marking the cut.
func cut(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos] + "…"
		}
		i++
	}
	return s
}

// cleanTransportError is the net/http error without the URL it quotes.
func cleanTransportError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}
