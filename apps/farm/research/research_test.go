package research_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/research"
	"github.com/bsamanvith/agentkit/apps/farm/research/researchtest"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	credsqlite "github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func open(t *testing.T, fake *researchtest.Server) (*research.Research, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := credsqlite.Open(context.Background(), filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	keyFile := filepath.Join(dir, "vault.key")
	v, err := research.OpenVault(context.Background(), store, "", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	r, err := research.New(context.Background(), v, research.WithBaseURLs(fake.URL, fake.URL))
	if err != nil {
		t.Fatal(err)
	}
	return r, keyFile
}

func registry(t *testing.T, r *research.Research) tool.Registry {
	t.Helper()
	tools, err := r.Tools()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestNotConnected(t *testing.T) {
	t.Parallel()
	fake := researchtest.New()
	defer fake.Close()
	r, _ := open(t, fake)
	_, err := registry(t, r).Invoke(context.Background(), research.SearchTool, json.RawMessage(`{"query":"paddy seed"}`))
	if !errors.Is(err, tool.ErrToolFailed) || !strings.Contains(err.Error(), "settings") {
		t.Errorf("no key: %v", err)
	}
	if fake.Calls("/search") != 0 {
		t.Error("a search was sent without a key")
	}
	st, err := r.Status(context.Background())
	if err != nil || len(st) != 2 || st[0].Connected || st[1].Connected {
		t.Errorf("status %+v %v", st, err)
	}
}

func TestExaThenParallel(t *testing.T) {
	t.Parallel()
	fake := researchtest.New()
	defer fake.Close()
	r, keyFile := open(t, fake)
	ctx := context.Background()
	reg := registry(t, r)

	if err := r.Connect(ctx, "parallel", "par-key-123456"); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Invoke(ctx, research.SearchTool, json.RawMessage(`{"query":"paddy varieties Godavari delta kharif","domains":["example.org"],"after":"2026-01-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"provider":"parallel"`) || !strings.Contains(string(out), "Delta-1") || fake.Calls("/v1/search") != 1 {
		t.Errorf("parallel search: %s (%d calls)", out, fake.Calls("/v1/search"))
	}
	if q, _ := json.Marshal(fake.Last()); !strings.Contains(string(q), "example.org") || !strings.Contains(string(q), "2026-01-01") {
		t.Errorf("parallel request lost the domains or the date: %s", q)
	}

	// With Exa connected too, Exa answers.
	if err := r.Connect(ctx, "exa", "exa-key-123456"); err != nil {
		t.Fatal(err)
	}
	out, err = reg.Invoke(ctx, research.SearchTool, json.RawMessage(`{"query":"paddy seed price Amalapuram","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"provider":"exa"`) || fake.Calls("/search") != 1 {
		t.Errorf("exa search: %s", out)
	}
	if n, _ := fake.Last()["numResults"].(float64); n != 3 {
		t.Errorf("exa request %v", fake.Last())
	}
	out, err = reg.Invoke(ctx, research.ExtractTool, json.RawMessage(`{"urls":["https://example.org/leaflets/godavari-delta-paddy-varieties"],"focus":"seed rate"}`))
	if err != nil || !strings.Contains(string(out), "25 kg per acre") || fake.Calls("/contents") != 1 {
		t.Errorf("exa extract: %s %v", out, err)
	}
	if _, err := reg.Invoke(ctx, research.ExtractTool, json.RawMessage(`{"urls":[]}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("no URLs: %v", err)
	}

	// A new key replaces the old; disconnecting Exa goes back to Parallel.
	if err := r.Connect(ctx, "exa", "exa-key-654321"); err != nil {
		t.Fatal(err)
	}
	if err := r.Disconnect(ctx, "exa"); err != nil {
		t.Fatal(err)
	}
	if err := r.Disconnect(ctx, "exa"); err != nil {
		t.Errorf("disconnecting twice: %v", err)
	}
	out, _ = reg.Invoke(ctx, research.SearchTool, json.RawMessage(`{"query":"paddy"}`))
	if !strings.Contains(string(out), `"provider":"parallel"`) {
		t.Errorf("after disconnecting exa: %s", out)
	}
	// Refusals.
	if err := r.Connect(ctx, "google", "abcdefghij"); !errors.Is(err, research.ErrUnknownProvider) {
		t.Errorf("unknown provider: %v", err)
	}
	if err := r.Connect(ctx, "exa", "short"); err == nil {
		t.Error("a short key accepted")
	}
	// The key file is private, and the vault holds no plain key.
	fi, err := os.Stat(keyFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file %v %v", fi, err)
	}
	db, _ := os.ReadFile(filepath.Join(filepath.Dir(keyFile), "vault.db"))
	if strings.Contains(string(db), "par-key-123456") {
		t.Error("the key is stored in the clear")
	}
}

func TestOpenVaultKeyRing(t *testing.T) {
	t.Parallel()
	store, err := credsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := research.OpenVault(context.Background(), store, "bad", ""); !errors.Is(err, credentials.ErrKeyConfig) {
		t.Errorf("bad ring: %v", err)
	}
	if _, err := research.OpenVault(context.Background(), store, "k1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ""); err != nil {
		t.Errorf("good ring: %v", err)
	}
}
