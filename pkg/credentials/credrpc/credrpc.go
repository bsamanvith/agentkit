// Package credrpc puts the credential vault's sources on the wire, for a
// toolsets component that keeps no vault (T131): [Register] serves the
// agent API's [credentials.Sources] on an [rpc.Server], and a [Client] is a
// [credentials.Fetcher] for [credentials.NewRemoteSources].
//
// What crosses is what a toolset sends a provider and nothing more: an API
// key, a user and password, or an access token with its type, expiry and
// extras (a Salesforce token's instance_url). Refresh tokens and client
// secrets stay with the vault, which refreshes. The server answers only for
// the slots it is given, each at its scope, for the tenant and user the
// call carries (pkg/tenancy); the RPC token is what lets a caller name them,
// as for toolrpc's calls.
package credrpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Service is the service's name: a component serving it mounts its server at
// rpc.PathPrefix + Service + "/".
const Service = "credentials"

// ErrUnknownSlot means the server serves no slot of that name at that scope.
var ErrUnknownSlot = errors.New("credential slot not served")

var get = rpc.Unary[getReq, getResp]{Name: Service + "/Get", Errors: []rpc.Code{
	{Name: "credentials.not_connected", Err: credentials.ErrNotConnected},
	{Name: "credentials.unknown_slot", Err: ErrUnknownSlot},
}}

type getReq struct {
	Toolset string            `json:"toolset"`
	Slot    string            `json:"slot"`
	Scope   credentials.Scope `json:"scope,omitempty"`
	Caller  tenancy.Carried   `json:"caller,omitzero"`
}

// getResp is a credential with its secrets: an API key (Key), basic auth
// (User, Password) or an OAuth2 access token.
type getResp struct {
	Kind        credentials.Kind  `json:"kind"`
	Key         string            `json:"key,omitempty"`
	User        string            `json:"user,omitempty"`
	Password    string            `json:"password,omitempty"`
	AccessToken string            `json:"access_token,omitempty"`
	TokenType   string            `json:"token_type,omitempty"`
	Expiry      time.Time         `json:"expiry,omitzero"`
	Scopes      []string          `json:"scopes,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
}

// Provider is what the server serves: [credentials.Sources].
type Provider interface {
	Source(toolset, slot string, scope credentials.Scope) (credentials.Source, error)
}

// Register serves p on s for slots, each at its scope ("" is the tenant's).
func Register(s *rpc.Server, p Provider, slots map[string]credentials.Scope) error {
	if p == nil {
		return errors.New("credrpc: nil provider")
	}
	served := map[string]credentials.Scope{}
	for slot, scope := range slots {
		if err := credentials.CheckSlot(slot); err != nil {
			return fmt.Errorf("credrpc: %w", err)
		}
		if scope == "" {
			scope = credentials.ScopeTenant
		}
		if err := credentials.CheckScope(scope); err != nil {
			return fmt.Errorf("credrpc: slot %q: %w", slot, err)
		}
		served[slot] = scope
	}
	get.Handle(s, func(ctx context.Context, r getReq) (getResp, error) {
		scope := r.Scope
		if scope == "" {
			scope = credentials.ScopeTenant
		}
		if want, ok := served[r.Slot]; !ok || want != scope {
			return getResp{}, fmt.Errorf("%w: %q (%s)", ErrUnknownSlot, r.Slot, scope)
		}
		ctx, err := r.Caller.Into(ctx)
		if err != nil {
			return getResp{}, fmt.Errorf("%w: caller: %w", rpc.ErrBadRequest, err)
		}
		src, err := p.Source(r.Toolset, r.Slot, scope)
		if err != nil {
			return getResp{}, err
		}
		cred, err := src.Get(ctx)
		if err != nil {
			return getResp{}, err
		}
		return encode(cred)
	})
	return nil
}

func encode(c credentials.Credential) (getResp, error) {
	switch c := c.(type) {
	case credentials.APIKey:
		return getResp{Kind: c.Kind(), Key: c.Key.Reveal()}, nil
	case credentials.Basic:
		return getResp{Kind: c.Kind(), User: c.User, Password: c.Password.Reveal()}, nil
	case credentials.OAuth2:
		return getResp{
			Kind: c.Kind(), AccessToken: c.AccessToken.Reveal(), TokenType: c.TokenType,
			Expiry: c.Expiry, Scopes: c.Scopes, Extra: c.Extra,
		}, nil
	}
	return getResp{}, fmt.Errorf("credrpc: a %T cannot be sent", c)
}

func (r getResp) decode() (credentials.Credential, error) {
	switch r.Kind {
	case credentials.KindAPIKey:
		return credentials.APIKey{Key: credentials.NewSecret(r.Key)}, nil
	case credentials.KindBasic:
		return credentials.Basic{User: r.User, Password: credentials.NewSecret(r.Password)}, nil
	case credentials.KindOAuth2:
		return credentials.OAuth2{
			AccessToken: credentials.NewSecret(r.AccessToken), TokenType: r.TokenType,
			Expiry: r.Expiry, Scopes: r.Scopes, Extra: r.Extra,
		}, nil
	}
	return nil, fmt.Errorf("credrpc: unknown credential kind %q", r.Kind)
}

// Client fetches credentials from a server. It is safe for concurrent use.
type Client struct{ conn rpc.Conn }

// NewClient returns a client that calls through conn: rpc.Dial for a server
// in another container, rpc.Pipe for one in process.
func NewClient(conn rpc.Conn) *Client { return &Client{conn: conn} }

var _ credentials.Fetcher = (*Client)(nil)

// Fetch implements [credentials.Fetcher] for the caller ctx carries.
func (c *Client) Fetch(ctx context.Context, toolset, slot string, scope credentials.Scope) (credentials.Credential, error) {
	resp, err := get.Call(ctx, c.conn, getReq{Toolset: toolset, Slot: slot, Scope: scope, Caller: tenancy.Carry(ctx)})
	if err != nil {
		return nil, err
	}
	return resp.decode()
}
