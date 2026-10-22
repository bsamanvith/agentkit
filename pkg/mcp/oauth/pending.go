package oauth

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

// pendingJSON is a [Pending] with its secrets as plain strings: only ever
// inside a sealed blob.
type pendingJSON struct {
	V          int               `json:"v"`
	Tenant     string            `json:"tenant"`
	User       string            `json:"user,omitempty"`
	Server     string            `json:"server"`
	ReturnTo   string            `json:"return_to,omitempty"`
	Created    time.Time         `json:"created"`
	Expires    time.Time         `json:"expires"`
	Verifier   string            `json:"verifier"`
	Resource   string            `json:"resource,omitempty"`
	Scopes     []string          `json:"scopes,omitempty"`
	Issuer     string            `json:"issuer,omitempty"`
	IssParam   bool              `json:"iss_param,omitempty"`
	AuthURL    string            `json:"auth_url"`
	TokenURL   string            `json:"token_url"`
	ClientID   string            `json:"client_id"`
	Secret     string            `json:"client_secret,omitempty"`
	AuthStyle  int               `json:"auth_style,omitempty"`
	AuthParams map[string]string `json:"auth_params,omitempty"`
}

const pendingVersion = 1

// EncodePending is p as bytes, for a caller that keeps flows itself
// ([Connector.Start]). They hold the PKCE verifier and the client's
// secret: keep them sealed (the vault's Sealer), never in the clear.
func EncodePending(p Pending) ([]byte, error) {
	return json.Marshal(pendingJSON{
		V: pendingVersion, Tenant: p.Owner.Tenant, User: p.Owner.User, Server: p.Server, ReturnTo: p.ReturnTo,
		Created: p.Created, Expires: p.Expires, Verifier: p.Verifier.Reveal(), Resource: p.Resource,
		Scopes: p.Scopes, Issuer: p.Issuer, IssParam: p.IssParam, AuthURL: p.AuthURL, TokenURL: p.TokenURL,
		ClientID: p.ClientID, Secret: p.Secret.Reveal(), AuthStyle: p.AuthStyle, AuthParams: p.AuthParams,
	})
}

// DecodePending reverses [EncodePending].
func DecodePending(b []byte) (Pending, error) {
	var d pendingJSON
	if err := json.Unmarshal(b, &d); err != nil || d.V != pendingVersion || d.Server == "" || d.Verifier == "" {
		return Pending{}, errors.New("oauth: not a pending flow")
	}
	return Pending{
		Owner: Owner{Tenant: d.Tenant, User: d.User}, Server: d.Server, ReturnTo: d.ReturnTo,
		Created: d.Created, Expires: d.Expires, Verifier: NewSecret(d.Verifier), Resource: d.Resource,
		Scopes: slices.Clone(d.Scopes), Issuer: d.Issuer, IssParam: d.IssParam, AuthURL: d.AuthURL, TokenURL: d.TokenURL,
		ClientID: d.ClientID, Secret: NewSecret(d.Secret), AuthStyle: d.AuthStyle, AuthParams: maps.Clone(d.AuthParams),
	}, nil
}
