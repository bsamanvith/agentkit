//go:build !noagent

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
)

// Local users are credentials enough for the agent API alone, off loopback;
// with none it refuses to start there.
func TestUsersAreCredentialsForTheAPI(t *testing.T) {
	t.Parallel()
	hash, err := login.HashPassword("hunter2-hunter2")
	if err != nil {
		t.Fatal(err)
	}
	usersFile := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(usersFile, []byte(`{"users":[{"username":"root","password_hash":"`+hash+`","tenant":"t","roles":["admin"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		config, file string
		ok           bool
	}{
		"none":                  {config: `{}`},
		"in the config":         {config: `{"auth":{"users":[{"username":"ann","password_hash":"` + hash + `","tenant":"t","roles":["user"]}]}}`, ok: true},
		"in the file":           {config: `{}`, file: usersFile, ok: true},
		"behind a proxy":        {config: `{"auth":{"trusted_proxies":["10.0.0.0/8","192.0.2.1"],"users":[{"username":"ann","password_hash":"` + hash + `","tenant":"t","roles":["user"]}]}}`, ok: true},
		"a bad trusted proxy":   {config: `{"auth":{"trusted_proxies":["proxy.internal"],"users":[{"username":"ann","password_hash":"` + hash + `","tenant":"t","roles":["user"]}]}}`},
		"a user's toolset typo": {config: `{"auth":{"users":[{"username":"ann","password_hash":"` + hash + `","tenant":"t","roles":["user"],"toolsets":["dmeo"]}]}}`},
	} {
		path := filepath.Join(t.TempDir(), "agentkit.json")
		if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadServeConfig(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c := cli{getenv: func(k string) string {
			if k == envUsersFile {
				return tc.file
			}
			return ""
		}, newModel: func(modelFlags) (llm.Model, error) { return llmtest.NewFake(t), nil }}
		var logs bytes.Buffer
		env := serveEnv{components: []string{agentapi.Name}, addr: "0.0.0.0:8080", shutdownTimeout: time.Second}
		m, _, err := c.newAgents(t.Context(), cfg, nil, nil, env, nil, scoped.Memory(nil), nil, clock.Real(), slog.New(slog.NewTextHandler(&logs, nil)))
		if tc.ok != (err == nil) {
			t.Errorf("%s: %v, want ok=%v", name, err, tc.ok)
		}
		if err == nil {
			_ = m.Component.Shutdown(t.Context())
			if name != "behind a proxy" && !strings.Contains(logs.String(), "trusted_proxies") {
				t.Errorf("%s: no warning that cookies are not Secure:\n%s", name, &logs)
			}
		}
		if err != nil && strings.Contains(err.Error(), hash) {
			t.Errorf("%s: the error holds the hash: %v", name, err)
		}
		if name == "none" && err != nil && !strings.Contains(err.Error(), envUsersFile) {
			t.Errorf("none: %v, want it to name %s", err, envUsersFile)
		}
	}
}
