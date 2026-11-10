package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts/accountstest"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts/sqlite"
)

func TestStore(t *testing.T) {
	accountstest.RunStore(t, func(t *testing.T) accounts.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "accounts.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestStoreKeepsItsDataAcrossOpens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "farm.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAccount(ctx, accounts.Account{ID: "a1", Name: "Ravi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.Account(ctx, "a1")
	if err != nil || a.Name != "Ravi" {
		t.Fatalf("after reopening: %+v, %v", a, err)
	}
}
