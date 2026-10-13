package scoped_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// TestOpen: each spec opens its backend, which keeps a conversation record;
// the store is never the backend's type nor an Admin; a bad spec fails
// without showing a password.
func TestOpen(t *testing.T) {
	t.Parallel()
	specs := []string{"memory", "sqlite:" + filepath.Join(t.TempDir(), "agentkit.db")}
	if u := os.Getenv("AGENTKIT_PG_URL"); u != "" {
		specs = append(specs, u)
	}
	tenant, err := tenancy.NewTenant("scoped-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.With(t.Context(), tenant)
	for _, spec := range specs {
		s, closeFn, err := scoped.Open(t.Context(), spec, nil)
		if err != nil {
			t.Fatalf("Open(%q): %v", strings.SplitN(spec, ":", 2)[0], err)
		}
		if _, ok := s.(store.Admin); ok {
			t.Errorf("%s: the store is an Admin", spec[:6])
		}
		if _, ok := s.(*memory.Store); ok {
			t.Errorf("%s: the store is the backend's type", spec[:6])
		}
		id := "conv_scoped_" + strings.ReplaceAll(t.Name(), "/", "_")
		_ = s.DeleteConversation(ctx, id)
		if _, err := s.CreateConversation(ctx, store.Conversation{ID: id, Agent: "a"}); err != nil {
			t.Errorf("%s: CreateConversation: %v", spec[:6], err)
		} else if c, err := s.GetConversation(ctx, id); err != nil || c.Agent != "a" {
			t.Errorf("%s: GetConversation = %+v, %v", spec[:6], c, err)
		}
		_ = s.DeleteConversation(ctx, id)
		if err := closeFn(); err != nil {
			t.Errorf("%s: close: %v", spec[:6], err)
		}
	}
	if _, ok := scoped.Memory(nil).(*memory.Store); ok {
		t.Error("Memory is the backend's type")
	}
	for _, spec := range []string{"", "mem", "sqlite:", "mysql://u:secret@host/db", "postgress://u:secret@host/db"} {
		_, _, err := scoped.Open(t.Context(), spec, nil)
		if !errors.Is(err, scoped.ErrUnknownSpec) || strings.Contains(err.Error(), "secret") {
			t.Errorf("Open(%q) = %v, want ErrUnknownSpec without the password", spec, err)
		}
	}
}
