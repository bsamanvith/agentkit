package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/flow/flowtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunCheckpointer runs flow's own conformance suite (flowtest.RunCheckpointer)
// against a store's [flow.Checkpointer] inside one tenant, then checks that
// checkpoints are scoped by tenant. newCheckpointer must return an empty
// checkpointer for each call.
func RunCheckpointer(t *testing.T, newCheckpointer func(t *testing.T) flow.Checkpointer) {
	t.Helper()

	t.Run("Flow", func(t *testing.T) {
		t.Parallel()
		flowtest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer {
			tn, err := tenancy.NewTenant(tenantA)
			if err != nil {
				t.Fatalf("NewTenant: %v", err)
			}
			return inTenant{newCheckpointer(t), tn}
		})
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		none := t.Context()
		if err := cp.Save(none, "r", checkpoint("r", "none")); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("Save without a tenant = %v, want ErrNoTenant", err)
		}
		if _, err := cp.Load(none, "r"); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("Load without a tenant = %v, want ErrNoTenant", err)
		}

		a, b, c := tenantCtx(none, t, tenantA), tenantCtx(none, t, tenantB), tenantCtx(none, t, unusedTenant)
		if err := cp.Save(a, "r", checkpoint("r", "a")); err != nil {
			t.Fatalf("Save in A: %v", err)
		}
		if _, err := cp.Load(b, "r"); !errors.Is(err, flow.ErrNotFound) {
			t.Errorf("Load in B of A's checkpoint = %v, want ErrNotFound", err)
		}
		if err := cp.Save(b, "r", checkpoint("r", "b")); err != nil {
			t.Fatalf("Save in B: %v", err)
		}
		for ctx, want := range map[context.Context]string{a: "a", b: "b"} {
			got, err := cp.Load(ctx, "r")
			if err != nil || got.Shape != want {
				t.Errorf("Load = shape %q, %v; want the tenant's own checkpoint %q", got.Shape, err, want)
			}
		}
		if _, err := cp.Load(c, "r"); !errors.Is(err, flow.ErrNotFound) {
			t.Errorf("Load in a third tenant = %v, want ErrNotFound", err)
		}
	})
}

func checkpoint(runID, shape string) flow.Checkpoint {
	return flow.Checkpoint{
		Version: flow.CheckpointVersion,
		RunID:   runID,
		Shape:   shape,
		Status:  flow.StatusRunning,
		Initial: json.RawMessage(`{}`),
		Steps:   map[string]json.RawMessage{},
	}
}

// inTenant runs a checkpointer in one tenant, for flow's suite, which does
// not know about tenants.
type inTenant struct {
	cp flow.Checkpointer
	tn tenancy.Tenant
}

func (c inTenant) Save(ctx context.Context, id string, cp flow.Checkpoint) error {
	return c.cp.Save(tenancy.With(ctx, c.tn), id, cp)
}

func (c inTenant) Load(ctx context.Context, id string) (flow.Checkpoint, error) {
	return c.cp.Load(tenancy.With(ctx, c.tn), id)
}
