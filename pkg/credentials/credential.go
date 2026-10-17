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
)

// Credential is one of [APIKey], [Basic], [OAuth2] and [ClientCredentials].
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

func (APIKey) credential()            {}
func (Basic) credential()             {}
func (OAuth2) credential()            {}
func (ClientCredentials) credential() {}

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
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token,omitempty"`
		Expiry       time.Time `json:"expiry,omitzero"`
		Scopes       []string  `json:"scopes,omitempty"`
		TokenType    string    `json:"token_type,omitempty"`
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
		data = oauth2JSON{
			AccessToken: c.AccessToken.Reveal(), RefreshToken: c.RefreshToken.Reveal(),
			Expiry: c.Expiry.UTC(), Scopes: c.Scopes, TokenType: c.TokenType,
		}
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
		return OAuth2{
			AccessToken: NewSecret(d.AccessToken), RefreshToken: NewSecret(d.RefreshToken),
			Expiry: d.Expiry, Scopes: d.Scopes, TokenType: d.TokenType,
		}, nil
	case KindClientCredentials:
		var d clientCredentialsJSON
		if json.Unmarshal(env.Data, &d) != nil {
			return nil, ErrCorrupt
		}
		return ClientCredentials{ClientID: d.ClientID, ClientSecret: NewSecret(d.ClientSecret), TokenURL: d.TokenURL, Scopes: d.Scopes}, nil
	}
	return nil, ErrCorrupt
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
