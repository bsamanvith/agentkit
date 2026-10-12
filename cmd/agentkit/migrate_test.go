package main

import (
	"bytes"
	"strings"
	"testing"
)

// migrateWith runs agentkit migrate with AGENTKIT_STORE=store and returns its
// exit code and stderr.
func migrateWith(t *testing.T, store string, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	c := cli{stdout: &stdout, stderr: &stderr, getenv: func(k string) string {
		if k == envStore {
			return store
		}
		return ""
	}}
	code := c.main(t.Context(), append([]string{"migrate"}, args...))
	if stdout.Len() != 0 {
		t.Errorf("agentkit migrate printed %q to stdout, want nothing", &stdout)
	}
	return code, stderr.String()
}

// agentkit migrate needs a postgres:// URL, and never prints the URL it was
// given: a connection string can carry a password. (Migrating a real
// database is TestMigrateCommand, in the integration tier.)
func TestMigrateRefusesWhatItCannotMigrate(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		store string
		args  []string
		code  int
		want  string
	}{
		"unset":               {code: 1, want: "AGENTKIT_STORE is not set"},
		"not postgres":        {store: "sqlite:/var/lib/agentkit/secret.db", code: 1, want: "not a postgres:// URL"},
		"key=value string":    {store: "host=db password=secret", code: 1, want: "not a postgres:// URL"},
		"unreachable":         {store: "postgres://agentkit:secret@127.0.0.1:1/agentkit?connect_timeout=5", code: 1, want: "agentkit:"},
		"positional argument": {store: "postgres://agentkit:secret@127.0.0.1:1/agentkit", args: []string{"now"}, code: 2, want: "usage: agentkit migrate"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, stderr := migrateWith(t, tc.store, tc.args...)
			if code != tc.code || !strings.Contains(stderr, tc.want) {
				t.Errorf("agentkit migrate with %s=%q = exit %d, stderr %q; want exit %d mentioning %q", envStore, tc.store, code, stderr, tc.code, tc.want)
			}
			if strings.Contains(stderr, "secret") {
				t.Errorf("stderr %q shows the store's password or path", stderr)
			}
		})
	}
}
