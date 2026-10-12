package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// envStore names the store: a postgres:// URL selects the Postgres store.
// T037 adds the switch in serve that reads it; agentkit migrate reads it now.
const envStore = "AGENTKIT_STORE"

// migrate applies the Postgres store's migrations to the database
// $AGENTKIT_STORE names, then exits: the one-off job for deployments that
// start serve with AGENTKIT_STORE_MIGRATE=off, so that no server changes the
// schema. It is safe to run while servers are up, and more than once: the
// migrations run under the store's advisory lock and skip what is applied. A
// database a newer binary has migrated is refused (postgres.ErrSchemaTooNew).
// It prints nothing on success.
func (c cli) migrate(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit migrate", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errUsage
	}
	url := c.env(envStore)
	switch {
	case url == "":
		return fmt.Errorf("%s is not set: set it to the postgres:// URL of the database to migrate", envStore)
	case !strings.HasPrefix(url, "postgres://") && !strings.HasPrefix(url, "postgresql://"):
		// Not echoed: a connection string can carry a password.
		return fmt.Errorf("%s is not a postgres:// URL: only the Postgres store has migrations to run as a job (SQLite migrates itself when opened)", envStore)
	}
	return postgres.Migrate(ctx, url)
}
