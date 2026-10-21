package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
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
