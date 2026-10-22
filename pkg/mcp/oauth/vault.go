package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// VaultStore is a [TokenStore] and [ClientStore] over the credentials vault
// (E07, T042): connections and registered clients are sealed at rest, and
// every replica sharing the vault sees the same ones. A connection is an
// OAuth2 credential at the server's name as its slot, under its owner's
// tenant and user ("" for a tenant's shared connection), with the client
// it was issued to inside it, so a refresh never repeats discovery. A
// client registered by dynamic registration is an OAuth2Client credential
// of the tenant, at a reserved slot named from its issuer ("_mcp-client-"
// and a hash), which no toolset can name (slots starting with "_" are
// refused in config). ReconnectNeeded is the credential's NeedsReconnect,
// and Version its version, so [TokenStore.Swap] is the vault's
// compare-and-swap.
//
// The tenant is the key's, never the context's: a key of tenant A reads
// and writes only A's credentials.
type VaultStore struct{ v credentials.Vault }

var (
	_ TokenStore  = (*VaultStore)(nil)
	_ ClientStore = (*VaultStore)(nil)
)

// NewVaultStore returns the store over v.
func NewVaultStore(v credentials.Vault) *VaultStore { return &VaultStore{v: v} }

// ClientSlotPrefix starts the slot of a registered client.
const ClientSlotPrefix = "_mcp-client-"

// clientSlot is the slot of the client registered at issuer.
func clientSlot(issuer string) string {
	sum := sha256.Sum256([]byte(issuer))
	return ClientSlotPrefix + hex.EncodeToString(sum[:20])
}

// in is ctx in tenant, whatever tenant ctx carried.
func in(ctx context.Context, tenant string) (context.Context, error) {
	t, err := tenancy.NewTenant(tenant)
	if err != nil {
		return nil, fmt.Errorf("oauth: %w", err)
	}
	return tenancy.With(ctx, t), nil
}

// Get implements [TokenStore]. A slot that holds another kind of credential
// is no connection ([ErrNotFound]).
func (s *VaultStore) Get(ctx context.Context, k Key) (Token, error) {
	ctx, err := in(ctx, k.Tenant)
	if err != nil {
		return Token{}, err
	}
	cred, meta, err := s.v.Get(ctx, k.User, k.Server)
	if errors.Is(err, credentials.ErrNotFound) {
		return Token{}, ErrNotFound
	} else if err != nil {
		return Token{}, err
	}
	o, ok := cred.(credentials.OAuth2)
	if !ok || o.Client == nil {
		return Token{}, ErrNotFound
	}
	t := Token{
		Access: NewSecret(o.AccessToken.Reveal()), Refresh: NewSecret(o.RefreshToken.Reveal()),
		Expiry: o.Expiry, Scopes: slices.Clone(o.Scopes), Extra: maps.Clone(o.Extra),
		TokenURL: o.Client.TokenURL, ClientID: o.Client.ClientID, ClientSecret: NewSecret(o.Client.ClientSecret.Reveal()),
		AuthStyle: o.Client.TokenAuthStyle, Resource: o.Client.Resource, Issuer: o.Client.Issuer,
		ReconnectNeeded: meta.NeedsReconnect, Version: meta.Version,
	}
	return t, nil
}

// credential is t as the vault keeps it.
func credential(t Token) credentials.OAuth2 {
	return credentials.OAuth2{
		AccessToken: credentials.NewSecret(t.Access.Reveal()), RefreshToken: credentials.NewSecret(t.Refresh.Reveal()),
		Expiry: t.Expiry, Scopes: slices.Clone(t.Scopes), TokenType: "Bearer", Extra: maps.Clone(t.Extra),
		Client: &credentials.OAuth2Client{
			ClientID: t.ClientID, ClientSecret: credentials.NewSecret(t.ClientSecret.Reveal()), Issuer: t.Issuer,
			TokenURL: t.TokenURL, TokenAuthStyle: t.AuthStyle, Resource: t.Resource,
		},
	}
}

// replace writes cred at (user, slot) over whatever is there, again if
// another writer comes between the read and the write.
func (s *VaultStore) replace(ctx context.Context, user, slot string, cred credentials.Credential) error {
	var last error
	for range 5 {
		// The version from the listing, not from Get: a blob that no
		// longer opens (sealed under a key the ring dropped) is replaced
		// too.
		metas, err := s.v.List(ctx, user)
		if err != nil {
			return err
		}
		var version int64
		for _, m := range metas {
			if m.Slot == slot {
				version = m.Version
			}
		}
		_, err = s.v.Put(ctx, user, slot, cred, version)
		if err == nil {
			return nil
		}
		if !errors.Is(err, credentials.ErrConflict) && !errors.Is(err, credentials.ErrExists) {
			return err
		}
		last = err
	}
	return fmt.Errorf("oauth: written by others meanwhile, five times: %w", last)
}

// Put implements [TokenStore].
func (s *VaultStore) Put(ctx context.Context, k Key, t Token) error {
	ctx, err := in(ctx, k.Tenant)
	if err != nil {
		return err
	}
	return s.replace(ctx, k.User, k.Server, credential(t))
}

// Swap implements [TokenStore]: the vault's compare-and-swap on old's
// Version. A t with ReconnectNeeded set marks the stored one, keeping its
// value.
func (s *VaultStore) Swap(ctx context.Context, k Key, old, t Token) error {
	ctx, err := in(ctx, k.Tenant)
	if err != nil {
		return err
	}
	if old.Version < 1 {
		return ErrConflict
	}
	if t.ReconnectNeeded {
		_, err = s.v.MarkNeedsReconnect(ctx, k.User, k.Server, old.Version)
	} else {
		_, err = s.v.Put(ctx, k.User, k.Server, credential(t), old.Version)
	}
	if errors.Is(err, credentials.ErrConflict) || errors.Is(err, credentials.ErrNotFound) || errors.Is(err, credentials.ErrExists) {
		return ErrConflict
	}
	return err
}

// Delete implements [TokenStore].
func (s *VaultStore) Delete(ctx context.Context, k Key) error {
	ctx, err := in(ctx, k.Tenant)
	if err != nil {
		return err
	}
	if err := s.v.Delete(ctx, k.User, k.Server, 0); err != nil && !errors.Is(err, credentials.ErrNotFound) {
		return err
	}
	return nil
}

// GetClient implements [ClientStore].
func (s *VaultStore) GetClient(ctx context.Context, tenant, issuer string) (Client, error) {
	ctx, err := in(ctx, tenant)
	if err != nil {
		return Client{}, err
	}
	cred, _, err := s.v.Get(ctx, "", clientSlot(issuer))
	if errors.Is(err, credentials.ErrNotFound) {
		return Client{}, ErrNotFound
	} else if err != nil {
		return Client{}, err
	}
	c, ok := cred.(credentials.OAuth2Client)
	if !ok || c.Issuer != issuer {
		return Client{}, ErrNotFound
	}
	return Client{ID: c.ClientID, Secret: NewSecret(c.ClientSecret.Reveal()), AuthStyle: c.TokenAuthStyle}, nil
}

// PutClient implements [ClientStore].
func (s *VaultStore) PutClient(ctx context.Context, tenant, issuer string, c Client) error {
	ctx, err := in(ctx, tenant)
	if err != nil {
		return err
	}
	return s.replace(ctx, "", clientSlot(issuer), credentials.OAuth2Client{
		ClientID: c.ID, ClientSecret: credentials.NewSecret(c.Secret.Reveal()), Issuer: issuer, TokenAuthStyle: c.AuthStyle,
	})
}
