package toolsettest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// CredentialEchoType is the type of [CredentialEcho].
const CredentialEchoType = "credential_echo"

// CredentialEcho returns a test-only connector that calls its upstream (a
// [CredentialUpstream]) with the credential of its spec's slot, chosen per
// call from the caller's tenant and user: the reference for how a
// connector uses a credentials.Source, and what the end-to-end credential
// and canary tests run. Its spec must name a credential slot.
//
// Settings:
//
//	{"base_url": "http://127.0.0.1:1234", "api_key_header": "X-Api-Key"}
//
// Tools:
//
//   - whoami: GET {base_url}/whoami with the credential; returns
//     {"who": ...}, the name the upstream knows the credential by. A missing
//     credential is the source's not-connected error, as it is; an upstream
//     error is its status only, never its body (which may echo the
//     credential).
//   - whoami_panic: gets the credential, then panics.
func CredentialEcho() toolset.Factory { return credentialEcho{} }

type credentialEcho struct{}

func (credentialEcho) Type() string { return CredentialEchoType }

type (
	credentialEchoSettings struct {
		BaseURL      string `json:"base_url"`
		APIKeyHeader string `json:"api_key_header,omitempty"`
	}
	whoamiOut struct {
		Who string `json:"who" jsonschema:"whose credential the upstream got"`
	}
)

func (credentialEcho) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	var s credentialEchoSettings
	if err := toolset.DecodeSettings(in.Settings, &s); err != nil {
		return nil, err
	}
	if s.BaseURL == "" {
		return nil, fmt.Errorf("%w: base_url is required", toolset.ErrInvalidSettings)
	}
	src := in.Credentials
	if src == nil {
		return nil, fmt.Errorf("%w: %s needs a credential slot (\"credential\" in the spec)", toolset.ErrInvalidSettings, CredentialEchoType)
	}
	var opts []credentials.AuthOption
	if s.APIKeyHeader != "" {
		opts = append(opts, credentials.APIKeyHeader(s.APIKeyHeader))
	}
	meta := tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow})
	whoami, err := tool.Func("whoami", "Say whose credential the upstream got.",
		func(ctx context.Context, _ struct{}) (whoamiOut, error) {
			c, err := src.HTTPClient(ctx, nil, opts...)
			if err != nil {
				return whoamiOut{}, err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(s.BaseURL, "/")+"/whoami", nil)
			if err != nil {
				return whoamiOut{}, err
			}
			resp, err := c.Do(req)
			if err != nil {
				if errors.Is(err, credentials.ErrNotConnected) {
					var nc *credentials.NotConnectedError
					if errors.As(err, &nc) {
						return whoamiOut{}, nc
					}
				}
				var se *credentials.SourceError
				if errors.As(err, &se) {
					return whoamiOut{}, se
				}
				return whoamiOut{}, errors.New("the upstream could not be reached")
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			if err != nil {
				return whoamiOut{}, errors.New("reading the upstream's answer failed")
			}
			if resp.StatusCode != http.StatusOK {
				// The status only: an error body may echo the credential.
				return whoamiOut{}, fmt.Errorf("the upstream answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			}
			var out whoamiOut
			if err := json.Unmarshal(body, &out); err != nil {
				return whoamiOut{}, errors.New("the upstream's answer was not JSON")
			}
			return out, nil
		}, meta)
	if err != nil {
		return nil, err
	}
	boom, err := tool.Func("whoami_panic", "Get the credential, then panic.",
		func(ctx context.Context, _ struct{}) (whoamiOut, error) {
			if _, err := src.Get(ctx); err != nil {
				return whoamiOut{}, err
			}
			panic("whoami_panic: deliberate")
		}, meta)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{whoami, boom}, nil
}

// CredentialUpstream is the upstream of [CredentialEcho]: an httptest
// server that answers GET /whoami with {"who": name} for each header value
// it was told about ([CredentialUpstream.Accept]), and 401 for any other,
// with a body that echoes the header it got, as careless APIs do.
type CredentialUpstream struct {
	// URL is the server's base URL, for the base_url setting.
	URL string

	mu    sync.Mutex
	names map[string]string // "Authorization: Bearer x" -> name
	seen  []string
}

// NewCredentialUpstream starts one, closed when the test ends.
func NewCredentialUpstream(tb testing.TB) *CredentialUpstream {
	tb.Helper()
	u := &CredentialUpstream{names: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(u.serve))
	tb.Cleanup(srv.Close)
	u.URL = srv.URL
	return u
}

// Accept makes the upstream know a request whose header has value as
// name's (for example "Authorization", "Bearer key-a", "tenant A").
func (u *CredentialUpstream) Accept(header, value, name string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.names[http.CanonicalHeaderKey(header)+": "+value] = name
}

// Seen returns the names of the credentials requests came with, in order
// ("" for one it did not know).
func (u *CredentialUpstream) Seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

func (u *CredentialUpstream) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/whoami" {
		http.NotFound(w, r)
		return
	}
	u.mu.Lock()
	var who string
	var got []string
	for h, vs := range r.Header {
		for _, v := range vs {
			if n, ok := u.names[h+": "+v]; ok {
				who = n
			}
			if h == "Authorization" || strings.Contains(strings.ToLower(h), "key") {
				got = append(got, h+": "+v)
			}
		}
	}
	u.seen = append(u.seen, who)
	u.mu.Unlock()
	if who == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, "unauthorized: you sent %s", strings.Join(got, ", "))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(whoamiOut{Who: who})
}
