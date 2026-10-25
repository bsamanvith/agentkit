package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/file"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// testCredentialsKey is a key ring for $AGENTKIT_CREDENTIALS_KEY.
var testCredentialsKey = "k1:" + base64.StdEncoding.EncodeToString(make([]byte, credentials.KeySize))

func testRing(t *testing.T) *credentials.KeyRing {
	t.Helper()
	r, err := credentials.ParseKeyRing(testCredentialsKey)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func cliWithEnv(env map[string]string) cli {
	return cli{getenv: func(k string) string { return env[k] }}
}

// openTestVault opens the vault of cfg, closed when the test ends.
func openTestVault(t *testing.T, c cli, cfg credentialsConfig) credentials.Vault {
	t.Helper()
	v, closeFn, err := c.openVault(t.Context(), cfg, testRing(t))
	if err != nil {
		t.Fatalf("openVault(%+v): %v", cfg, err)
	}
	t.Cleanup(func() { _ = closeFn() })
	return v
}

func tenantContext(t *testing.T) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(t.Context(), tn)
}

// TestCredentialsFile: the file backend keeps credentials across a restart,
// and a second process (another openVault) on the same file is refused.
func TestCredentialsFile(t *testing.T) {
	t.Parallel()
	cfg := credentialsConfig{Backend: "file", Path: filepath.Join(t.TempDir(), "credentials.json")}
	ctx := tenantContext(t)
	v, closeFn, err := cli{}.openVault(t.Context(), cfg, testRing(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Put(ctx, "", "crm", credentials.APIKey{Key: credentials.NewSecret("sk-canary-0451")}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (cli{}).openVault(t.Context(), cfg, testRing(t)); !errors.Is(err, file.ErrLocked) {
		t.Errorf("a second vault on the file: %v, want ErrLocked", err)
	}
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(cfg.Path); strings.Contains(string(raw), "sk-canary-0451") || !strings.Contains(string(raw), "crm") {
		t.Errorf("the file holds the plaintext, or not the credential:\n%s", raw)
	}
	cred, _, err := openTestVault(t, cli{}, cfg).Get(ctx, "", "crm")
	if k, ok := cred.(credentials.APIKey); err != nil || !ok || k.Key.Reveal() != "sk-canary-0451" {
		t.Errorf("Get after a restart = %v, %v", cred, err)
	}
}

// TestCredentialsNoVault: without a key ring and with the default backend
// there is no vault, and nothing to close but a no-op.
func TestCredentialsNoVault(t *testing.T) {
	t.Parallel()
	v, closeFn, err := cli{}.openVault(t.Context(), credentialsConfig{}, nil)
	if v != nil || err != nil || closeFn() != nil {
		t.Errorf("openVault with no config and no key = %v, %v", v, err)
	}
	if v := openTestVault(t, cli{}, credentialsConfig{}); v == nil {
		t.Error("openVault with a key and no config gave no vault (want memory)")
	}
}

// TestStoreBackend: credentials.backend "store" keeps the vault where
// $AGENTKIT_STORE is, keeps none without a key ring, and refuses a path or
// a store it cannot share.
func TestStoreBackend(t *testing.T) {
	t.Parallel()
	for store, want := range map[string]string{
		"": "memory", "memory": "memory", "sqlite:/data/agentkit.db": "sqlite",
		"postgres://db/agentkit": "postgres", "postgresql://db/agentkit": "postgres", "redis://x": "",
	} {
		got, err := storeBackend(store)
		if got != want || (err != nil) != (want == "") {
			t.Errorf("storeBackend(%q) = %q, %v; want %q", store, got, err, want)
		}
	}
	on := func(store string) cli {
		return cli{getenv: func(k string) string {
			if k == envStore {
				return store
			}
			return ""
		}}
	}
	cfg := credentialsConfig{Backend: "store"}
	parts, closeFn, err := on("memory").openVaultParts(t.Context(), cfg, nil)
	if err != nil || parts != nil || closeFn == nil {
		t.Errorf("no key ring: %v, %v; want no vault and no error", parts, err)
	}
	parts, closeFn, err = on("memory").openVaultParts(t.Context(), cfg, testRing(t))
	if err != nil || parts == nil || parts.vault == nil {
		t.Fatalf("key ring on the memory store: %v, %v; want a vault", parts, err)
	}
	if err := closeFn(); err != nil {
		t.Error(err)
	}
	if _, _, err := on("redis://x").openVaultParts(t.Context(), cfg, testRing(t)); err == nil {
		t.Error("a store the vault can't share: no error")
	}
	for _, ring := range []*credentials.KeyRing{nil, testRing(t)} {
		if _, _, err := on("memory").openVaultParts(t.Context(), credentialsConfig{Backend: "store", Path: "/x"}, ring); err == nil {
			t.Errorf("a path with backend store (ring %v): no error", ring != nil)
		}
	}
}

// TestCredentialsOnPublicURL: the redirect and return URLs that are paths
// are on $AGENTKIT_PUBLIC_URL, full URLs stay; with no public URL a return
// path is dropped and a redirect path is refused by name.
func TestCredentialsOnPublicURL(t *testing.T) {
	t.Parallel()
	cfg := credentialsConfig{
		ReturnURLs: []string{"/connections/done", "https://app.example.com/done"},
		OAuth2: []oauth2AppConfig{
			{RedirectURL: "/oauth/callback"},
			{RedirectURL: "https://api.example.com/oauth/callback"},
		},
	}
	got := cfg.withPublicURL("http://localhost:30080")
	if want := []string{"http://localhost:30080/connections/done", "https://app.example.com/done"}; !slices.Equal(got.ReturnURLs, want) {
		t.Errorf("return_urls = %v, want %v", got.ReturnURLs, want)
	}
	if got.OAuth2[0].RedirectURL != "http://localhost:30080/oauth/callback" || got.OAuth2[1].RedirectURL != "https://api.example.com/oauth/callback" {
		t.Errorf("redirect_urls = %q, %q", got.OAuth2[0].RedirectURL, got.OAuth2[1].RedirectURL)
	}
	if cfg.ReturnURLs[0] != "/connections/done" || cfg.OAuth2[0].RedirectURL != "/oauth/callback" {
		t.Error("withPublicURL changed the config it was given")
	}
	none := cfg.withPublicURL("")
	if want := []string{"https://app.example.com/done"}; !slices.Equal(none.ReturnURLs, want) {
		t.Errorf("no public URL: return_urls = %v, want %v", none.ReturnURLs, want)
	}
	app := credentialsConfig{OAuth2: []oauth2AppConfig{{
		Slot: "x", ClientID: "id", ClientSecretEnv: "X_SECRET", AuthURL: "https://x.example.com/auth", TokenURL: "https://x.example.com/token", RedirectURL: "/oauth/callback",
	}}}
	if _, err := (cli{getenv: func(string) string { return "secret" }}).oauth2Apps(app); err == nil || !strings.Contains(err.Error(), envPublicURL) {
		t.Errorf("a redirect path with no public URL: %v, want an error naming $%s", err, envPublicURL)
	}
}
