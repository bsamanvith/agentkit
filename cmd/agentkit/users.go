package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/auth/login"
)

// envUsersFile names a JSON file of local users, in the form of the config's
// "auth" section ({"users": [...]}): for password hashes kept in a mounted
// secret.
const envUsersFile = "AGENTKIT_USERS_FILE"

// users are the config's local users and those of $AGENTKIT_USERS_FILE.
func (c cli) users(cfg serveConfig) ([]login.User, error) {
	users := slices.Clone(cfg.Auth.Users)
	if p := c.env(envUsersFile); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("$%s: %w", envUsersFile, err)
		}
		var f authConfig
		if err := decodeStrict(raw, &f); err != nil {
			return nil, fmt.Errorf("$%s: %w", envUsersFile, err)
		}
		users = append(users, f.Users...)
	}
	return users, nil
}

// usersCmd is `agentkit users`: hash-password, for the config's
// auth.users.
func (c cli) usersCmd(_ context.Context, args []string) error {
	if len(args) == 0 || args[0] != "hash-password" {
		_, _ = io.WriteString(c.stderr, `usage: agentkit users hash-password < password

Reads a password from the first line of stdin and prints its argon2id hash,
for the password_hash of a user in the config's auth.users. To type it
without echo: read -rs pw && printf '%s\n' "$pw" | agentkit users hash-password
`)
		return errUsage
	}
	fs := c.newFlagSet("agentkit users hash-password", "< password")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errUsage
	}
	line, err := bufio.NewReader(io.LimitReader(c.stdin(), login.MaxPasswordLength+3)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading the password: %w", err)
	}
	if !strings.HasSuffix(line, "\n") && len(line) > login.MaxPasswordLength {
		return login.ErrPasswordLength
	}
	pw := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if pw == "" {
		return errors.New("no password on stdin")
	}
	h, err := login.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.stdout, h)
	return err
}
