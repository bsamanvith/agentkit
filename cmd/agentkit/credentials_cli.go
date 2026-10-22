//go:build !noagent

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const credentialsUsage = `usage:
  agentkit credentials put [flags] <slot>   store a credential in the configured vault (the secret on stdin)

Run "agentkit credentials put -h" for flags.
`

// credentialsCmd runs "agentkit credentials ...": it writes to the vault the
// config file names, directly, to bootstrap a deployment without the API.
func (c cli) credentialsCmd(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "put" {
		fmt.Fprint(c.stderr, credentialsUsage)
		return 2
	}
	switch err := c.credentialsPut(ctx, args[1:]); {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		return 2
	default:
		fmt.Fprintln(c.stderr, "agentkit:", err)
		return 1
	}
}

// credentialsPut stores one credential at (tenant, user, slot), replacing
// what is there. The secret (the key, the password, the client secret or
// the refresh token) is the first line of stdin, never a flag: flags end up
// in shell histories and process lists.
func (c cli) credentialsPut(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit credentials put", "<slot>  (the secret on stdin)")
	configPath := fs.String("config", c.env(envConfig), "config file (JSON) whose credentials section names the vault (default $"+envConfig+")")
	tenant := fs.String("tenant", "", "tenant the credential is for (required)")
	user := fs.String("user", "", "user the credential is for (a slot of scope user); empty for the tenant's own")
	kind := fs.String("kind", string(credentials.KindAPIKey), "api_key, basic (-username), client_credentials (-client-id, -token-url) or oauth2 (a refresh token)")
	username := fs.String("username", "", "basic: the user name")
	clientID := fs.String("client-id", "", "client_credentials: the client id")
	tokenURL := fs.String("token-url", "", "client_credentials: the token endpoint (https)")
	scopes := fs.String("scopes", "", "client_credentials, oauth2: scopes, comma-separated")
	// The slot may come first ("put gmail -tenant acme") or last.
	var slot string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		slot, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	switch {
	case slot == "" && fs.NArg() == 1:
		slot = fs.Arg(0)
	case fs.NArg() != 0 || slot == "" || *tenant == "":
		fs.Usage()
		return errUsage
	}
	if err := credentials.CheckSlot(slot); err != nil {
		return err
	}
	tn, err := tenancy.NewTenant(*tenant)
	if err != nil {
		return fmt.Errorf("-tenant: %w", err)
	}
	cfg, err := loadServeConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Credentials.Backend == "" || cfg.Credentials.Backend == "memory" {
		return errors.New(`credentials put needs a vault that outlives it: set credentials.backend in the config ("file", "sqlite" or "postgres")`)
	}
	v := c.env(envCredentialsKey)
	if v == "" {
		return fmt.Errorf("credentials put needs the key ring in $%s", envCredentialsKey)
	}
	ring, err := credentials.ParseKeyRing(v)
	if err != nil {
		return fmt.Errorf("$%s: %w", envCredentialsKey, err)
	}
	secret, err := c.readSecret()
	if err != nil {
		return err
	}
	var scopeList []string
	for s := range strings.SplitSeq(*scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopeList = append(scopeList, s)
		}
	}
	var cred credentials.Credential
	switch credentials.Kind(*kind) {
	case credentials.KindAPIKey:
		cred = credentials.APIKey{Key: secret}
	case credentials.KindBasic:
		cred = credentials.Basic{User: *username, Password: secret}
	case credentials.KindClientCredentials:
		if *clientID == "" || !strings.HasPrefix(*tokenURL, "https://") {
			return errors.New("client_credentials needs -client-id and an https -token-url")
		}
		cred = credentials.ClientCredentials{ClientID: *clientID, ClientSecret: secret, TokenURL: *tokenURL, Scopes: scopeList}
	case credentials.KindOAuth2:
		// A refresh token: the first use refreshes it (credentials.oauth2
		// names the slot's client).
		cred = credentials.OAuth2{RefreshToken: secret, Scopes: scopeList}
	default:
		return fmt.Errorf("-kind %q: want api_key, basic, client_credentials or oauth2", *kind)
	}
	parts, closeFn, err := c.openVaultParts(ctx, cfg.Credentials, ring)
	if err != nil {
		return err
	}
	defer func() { _ = closeFn() }()
	tctx := tenancy.With(ctx, tn)
	for range 5 {
		var version int64
		list, err := parts.vault.List(tctx, *user)
		if err != nil {
			return err
		}
		for _, m := range list {
			if m.Slot == slot {
				version = m.Version
			}
		}
		m, err := parts.vault.Put(tctx, *user, slot, cred, version)
		if errors.Is(err, credentials.ErrConflict) || errors.Is(err, credentials.ErrExists) {
			continue // written meanwhile: read the version again
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "stored %s credential %q for tenant %s%s (version %d)\n", m.Kind, slot, tn.ID(), userLabel(*user), m.Version)
		return nil
	}
	return errors.New("the credential kept changing while it was written; try again")
}

func userLabel(u string) string {
	if u == "" {
		return ""
	}
	return ", user " + u
}

// readSecret reads the secret: the first line of stdin, without its line
// ending. An empty one is refused.
func (c cli) readSecret() (credentials.Secret, error) {
	sc := bufio.NewScanner(c.stdin())
	sc.Buffer(nil, 64<<10)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return credentials.Secret{}, fmt.Errorf("reading the secret from stdin: %w", err)
		}
		return credentials.Secret{}, errors.New("no secret on stdin")
	}
	v := strings.TrimRight(sc.Text(), "\r")
	if v == "" {
		return credentials.Secret{}, errors.New("the secret on stdin is empty")
	}
	return credentials.NewSecret(v), nil
}
