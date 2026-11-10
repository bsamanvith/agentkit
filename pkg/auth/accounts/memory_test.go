package accounts_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts/accountstest"
)

func TestMemoryStore(t *testing.T) {
	accountstest.RunStore(t, func(*testing.T) accounts.Store { return accounts.NewMemoryStore() })
}
