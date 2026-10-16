package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// OAuth scopes the tools need. ScopeReadonly covers search, get_file,
// read_file and list_permissions; the rest need Scope.
const (
	Scope         = "https://www.googleapis.com/auth/drive"
	ScopeReadonly = "https://www.googleapis.com/auth/drive.readonly"
)

// TokenSource supplies the access token for the user a call is made for.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

const (
	maxBodyBytes    = 8 << 20
	maxMessageChars = 300
)

// api sends requests to the Drive API as the calling user.
type api struct {
	http    *http.Client
	baseURL string
	tokens  TokenSource
}

// call sends one request and returns the body of a 2xx. contentType is for
// body, which may be nil.
func (a *api) call(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string) ([]byte, error) {
	tok, err := a.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	u := a.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not reach Google Drive")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	switch {
	case err != nil:
		return nil, errors.New("could not read the response from Google Drive")
	case len(data) > maxBodyBytes:
		return nil, errors.New("the response from Google Drive is over the 8 MB limit; the file is too large to read")
	}
	return data, statusError(resp.StatusCode, data, tok)
}

// getJSON, sendJSON: JSON in, JSON out.
func (a *api) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	data, err := a.call(ctx, http.MethodGet, path, q, nil, "")
	if err != nil {
		return err
	}
	return decode(data, out)
}

func (a *api) sendJSON(ctx context.Context, method, path string, q url.Values, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	data, err := a.call(ctx, method, path, q, bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	return decode(data, out)
}

func decode(data []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("Google Drive returned a response that is not valid JSON")
	}
	return nil
}

// statusError maps a non-2xx status to the error the model sees.
func statusError(status int, body []byte, token string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	msg := truncate(strings.ReplaceAll(errorMessage(body), token, "[redacted]"), maxMessageChars)
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return invalid("Google Drive rejected the request (status %d): %s", status, orNoDetails(msg))
	case http.StatusNotFound:
		return fmt.Errorf("%w: not found, or the connected account cannot see it (status 404)", tool.ErrToolFailed)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: Google rejected the access token; the account must be reconnected (status 401)", tool.ErrToolFailed)
	case http.StatusForbidden:
		return fmt.Errorf("%w: not allowed (status 403): %s", tool.ErrToolFailed, orNoDetails(msg))
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: Google Drive rate limited the request", tool.ErrToolFailed)
	default:
		return fmt.Errorf("%w: Google Drive returned status %d", tool.ErrToolFailed, status)
	}
}

func orNoDetails(s string) string {
	if s == "" {
		return "no details"
	}
	return s
}

// errorMessage extracts {"error": {"message": "..."}}.
func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Message
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", tool.ErrInvalidInput, fmt.Sprintf(format, args...))
}

func jsonEncode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
