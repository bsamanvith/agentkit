package credentials

import (
	"encoding/json"
	"fmt"
	"time"
)

// Kind names a kind of credential.
type Kind string

// The kinds of credential.
const (
	KindAPIKey            Kind = "api_key"
	KindBasic             Kind = "basic"
	KindOAuth2            Kind = "oauth2"
	KindClientCredentials Kind = "client_credentials"
	KindOAuth2Client      Kind = "oauth2_client"
)

// Credential is one of [APIKey], [Basic], [OAuth2], [ClientCredentials]
// and [OAuth2Client].
type Credential interface {
	// Kind says which.
	Kind() Kind
	credential() // only this package's types
}

// APIKey is a key sent as it is (a header, a query parameter).
type APIKey struct {
	Key Secret
}

// Basic is a user name and password.
type Basic struct {
	User     string
	Password Secret
}

// OAuth2 is an OAuth2 token, as a user's connection holds it.
type OAuth2 struct {
	AccessToken  Secret
	RefreshToken Secret
	Expiry       time.Time // zero: unknown or never
	Scopes       []string
	TokenType    string // "Bearer" when empty
	// Client is the OAuth2 client the token was issued to when it was
	// found as the account was connected, rather than configured for the
	// slot (MCP OAuth, T095): what refreshing the token needs. Nil for a
	// slot whose client is configured (credentials.oauth2).
	Client *OAuth2Client
	// Extra holds token response fields kept with the token, such as
	// Salesforce's instance_url. They are not secret.
	Extra map[string]string
}

// OAuth2Client is an OAuth2 client registered with an authorization server
// at run time (MCP OAuth, T095). Stored on its own, it is a client
// registered by dynamic registration (RFC 7591) that a tenant's users
// share; in an [OAuth2], the client its token was issued to.
type OAuth2Client struct {
	ClientID     string
	ClientSecret Secret // empty for a public client
	// Issuer is the authorization server, TokenURL its token endpoint.
	Issuer   string
	TokenURL string
	// TokenAuthStyle is how the client authenticates to the token
	// endpoint, as golang.org/x/oauth2's AuthStyle: 0 auto-detects, 1 in
	// the form, 2 in a basic auth header.
	TokenAuthStyle int
	// Resource is the resource (RFC 8707) tokens are asked for.
	Resource string
}

// ClientCredentials is what an OAuth2 client credentials grant needs.
type ClientCredentials struct {
	ClientID     string
	ClientSecret Secret
	TokenURL     string
	Scopes       []string
}

// Kind implements [Credential].
func (APIKey) Kind() Kind { return KindAPIKey }

// Kind implements [Credential].
func (Basic) Kind() Kind { return KindBasic }

// Kind implements [Credential].
func (OAuth2) Kind() Kind { return KindOAuth2 }

// Kind implements [Credential].
func (ClientCredentials) Kind() Kind { return KindClientCredentials }

// Kind implements [Credential].
func (OAuth2Client) Kind() Kind { return KindOAuth2Client }

func (APIKey) credential()            {}
func (Basic) credential()             {}
func (OAuth2) credential()            {}
func (ClientCredentials) credential() {}
func (OAuth2Client) credential()      {}

// expiry is the time a credential stops working, if it says.
func expiry(c Credential) time.Time {
	if o, ok := c.(OAuth2); ok {
		return o.Expiry
	}
	return time.Time{}
}

// The plaintext form, only ever inside a sealed blob: secrets as plain
// strings, which is why these types are not the public ones.
type (
	envelope struct {
		Kind Kind            `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	apiKeyJSON struct {
		Key string `json:"key"`
	}
	basicJSON struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	oauth2JSON struct {
		AccessToken  string            `json:"access_token"`
		RefreshToken string            `json:"refresh_token,omitempty"`
		Expiry       time.Time         `json:"expiry,omitzero"`
		Scopes       []string          `json:"scopes,omitempty"`
		TokenType    string            `json:"token_type,omitempty"`
		Client       *oauth2ClientJSON `json:"client,omitempty"`
		Extra        map[string]string `json:"extra,omitempty"`
	}
	oauth2ClientJSON struct {
		ClientID       string `json:"client_id"`
		ClientSecret   string `json:"client_secret,omitempty"`
		Issuer         string `json:"issuer,omitempty"`
		TokenURL       string `json:"token_url,omitempty"`
		TokenAuthStyle int    `json:"token_auth_style,omitempty"`
		Resource       string `json:"resource,omitempty"`
	}
	clientCredentialsJSON struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		TokenURL     string   `json:"token_url"`
		Scopes       []string `json:"scopes,omitempty"`
	}
)

// encode is the plaintext a credential is sealed as.
func encode(c Credential) ([]byte, error) {
	var data any
	switch c := c.(type) {
	case APIKey:
		data = apiKeyJSON{Key: c.Key.Reveal()}
	case Basic:
		data = basicJSON{User: c.User, Password: c.Password.Reveal()}
	case OAuth2:
		d := oauth2JSON{
			AccessToken: c.AccessToken.Reveal(), RefreshToken: c.RefreshToken.Reveal(),
			Expiry: c.Expiry.UTC(), Scopes: c.Scopes, TokenType: c.TokenType, Extra: c.Extra,
		}
		if c.Client != nil {
			cj := clientJSON(*c.Client)
			d.Client = &cj
		}
		data = d
	case OAuth2Client:
		data = clientJSON(c)
	case ClientCredentials:
		data = clientCredentialsJSON{ClientID: c.ClientID, ClientSecret: c.ClientSecret.Reveal(), TokenURL: c.TokenURL, Scopes: c.Scopes}
	default:
		return nil, fmt.Errorf("%w: %T", ErrInvalidCredential, c)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Kind: c.Kind(), Data: b})
}

// decode reverses encode.
func decode(b []byte) (Credential, error) {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, ErrCorrupt
	}
	switch env.Kind {
	case KindAPIKey:
		var d apiKeyJSON
		if json.Unmarshal(env.Data, &d) != nil {
			return nil, ErrCorrupt
		}
		return APIKey{Key: NewSecret(d.Key)}, nil
	case KindBasic:
		var d basicJSON
		if json.Unmarshal(env.Data, &d) != nil {
			return nil, ErrCorrupt
		}
		return Basic{User: d.User, Password: NewSecret(d.Password)}, nil
	case KindOAuth2:
		var d oauth2JSON
		if json.Unmarshal(env.Data, &d) != nil {
			return nil, ErrCorrupt
		}
		o := OAuth2{
			AccessToken: NewSecret(d.AccessToken), RefreshToken: NewSecret(d.RefreshToken),
			Expiry: d.Expiry, Scopes: d.Scopes, TokenType: d.TokenType, Extra: d.Extra,
		}
		if d.Client != nil {
			cl := d.Client.client()
			o.Client = &cl
		}
		return o, nil
	case KindOAuth2Client:
		var d oauth2ClientJSON
		if json.Unmarshal(env.Data, &d) != nil {
			return nil, ErrCorrupt
		}
		return d.client(), nil
	case KindClientCredentials:
		var d clientCredentialsJSON
		if json.Unmarshal(env.Data, &d) != nil {
			return nil, ErrCorrupt
		}
		return ClientCredentials{ClientID: d.ClientID, ClientSecret: NewSecret(d.ClientSecret), TokenURL: d.TokenURL, Scopes: d.Scopes}, nil
	}
	return nil, ErrCorrupt
}

func clientJSON(c OAuth2Client) oauth2ClientJSON {
	return oauth2ClientJSON{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret.Reveal(), Issuer: c.Issuer,
		TokenURL: c.TokenURL, TokenAuthStyle: c.TokenAuthStyle, Resource: c.Resource,
	}
}

func (d oauth2ClientJSON) client() OAuth2Client {
	return OAuth2Client{
		ClientID: d.ClientID, ClientSecret: NewSecret(d.ClientSecret), Issuer: d.Issuer,
		TokenURL: d.TokenURL, TokenAuthStyle: d.TokenAuthStyle, Resource: d.Resource,
	}
}

// check refuses a credential with nothing to use in it.
func check(c Credential) error {
	var empty bool
	switch c := c.(type) {
	case APIKey:
		empty = c.Key.IsZero()
	case Basic:
		empty = c.User == "" && c.Password.IsZero()
	case OAuth2:
		empty = c.AccessToken.IsZero() && c.RefreshToken.IsZero()
	case ClientCredentials:
		empty = c.ClientID == "" || c.ClientSecret.IsZero() || c.TokenURL == ""
	case OAuth2Client:
		empty = c.ClientID == ""
	case nil:
		return fmt.Errorf("%w: nil", ErrInvalidCredential)
	default:
		return fmt.Errorf("%w: %T", ErrInvalidCredential, c)
	}
	if empty {
		return fmt.Errorf("%w: %s with nothing in it", ErrInvalidCredential, c.Kind())
	}
	return nil
}
