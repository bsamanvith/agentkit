package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth/login"
)

// agentkit users hash-password prints a hash that signs the password in, and
// never the password.
func TestHashPasswordCommand(t *testing.T) {
	t.Parallel()
	const pw = "a CANARY password, with spaces 7e1f"
	for name, in := range map[string]string{"LF": pw + "\n", "CRLF": pw + "\r\n", "no newline": pw, "two lines": pw + "\nsecond line\n"} {
		var stdout, stderr bytes.Buffer
		c := cli{stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }, term: clientTerm{stdin: strings.NewReader(in)}}
		if code := c.main(t.Context(), []string{"users", "hash-password"}); code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, &stderr)
		}
		hash := strings.TrimSuffix(stdout.String(), "\n")
		if strings.Contains(stdout.String()+stderr.String(), pw[:12]) || strings.Count(stdout.String(), "\n") != 1 {
			t.Errorf("%s: the output holds the password, or more than the hash: %q %q", name, &stdout, &stderr)
		}
		l, err := login.New([]login.User{{Username: "ann", PasswordHash: hash, Tenant: "t", Roles: []string{"user"}}})
		if err != nil {
			t.Fatalf("%s: the hash %q: %v", name, hash, err)
		}
		if _, err := l.SignIn(httptest.NewRequest("POST", "/v1/auth/login", nil), "ann", pw); err != nil {
			t.Errorf("%s: signing in with the password: %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		args []string
		in   string
		code int
	}{
		"no subcommand":     {args: []string{"users"}, code: 2},
		"unknown":           {args: []string{"users", "add"}, code: 2},
		"an argument":       {args: []string{"users", "hash-password", "pw"}, in: "x\n", code: 2},
		"empty stdin":       {args: []string{"users", "hash-password"}, code: 1},
		"empty line":        {args: []string{"users", "hash-password"}, in: "\nsecond\n", code: 1},
		"short":             {args: []string{"users", "hash-password"}, in: "short\n", code: 1},
		"too long":          {args: []string{"users", "hash-password"}, in: strings.Repeat("x", login.MaxPasswordLength+1) + "\n", code: 1},
		"too long, no line": {args: []string{"users", "hash-password"}, in: strings.Repeat("x", 5000), code: 1},
	} {
		var stdout, stderr bytes.Buffer
		c := cli{stdout: &stdout, stderr: &stderr, getenv: func(string) string { return "" }, term: clientTerm{stdin: strings.NewReader(tc.in)}}
		if code := c.main(t.Context(), tc.args); code != tc.code || stdout.Len() != 0 {
			t.Errorf("%s: exit %d, stdout %q; want exit %d and no output", name, code, &stdout, tc.code)
		}
	}
}
