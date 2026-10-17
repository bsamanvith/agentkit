package agentclient_test

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestRuntimeToolsets: the client adds an MCP server as a toolset, reviews
// and approves its tool, disables and deletes it, and each refusal matches
// its sentinel.
func TestRuntimeToolsets(t *testing.T) {
	t.Parallel()
	search, err := tool.Func("search", "Search.", func(_ context.Context, in struct {
		Q string `json:"q"`
	},
	) (string, error) {
		return in.Q, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry([]tool.Tool{search})
	if err != nil {
		t.Fatal(err)
	}
	up, err := server.New(map[string]tool.Registry{"linear": reg}, server.WithBasePath("/mcp"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(up)
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	guard, err := netguard.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := servers.New(scoped.Memory(nil), servers.WithGuard(guard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	r := newRig(t, &model{}, agentapi.WithToolsetAdmin(mgr))
	ctx, c := t.Context(), r.client

	added, err := c.AddToolset(ctx, wire.AddToolsetRequest{Type: "mcp", Name: "linear", URL: ts.URL + "/mcp/linear"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Status != wire.ToolsetReviewNeeded || len(added.Tools) != 1 || added.Tools[0].Status != wire.ToolNew {
		t.Fatalf("added = %+v, want one new tool to review", added)
	}
	if _, err := c.AddToolset(ctx, wire.AddToolsetRequest{Type: "mcp", Name: "linear", URL: ts.URL + "/mcp/linear"}); !errors.Is(err, agentclient.ErrToolsetExists) {
		t.Errorf("adding it again: %v, want ErrToolsetExists", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String() + "/mcp"
	_ = l.Close()
	if _, err := c.AddToolset(ctx, wire.AddToolsetRequest{Type: "mcp", Name: "gone", URL: dead}); !errors.Is(err, agentclient.ErrServerUnreachable) {
		t.Errorf("adding a dead server: %v, want ErrServerUnreachable", err)
	}

	review, err := c.ToolsetReview(ctx, "linear")
	if err != nil || len(review.Tools) != 1 {
		t.Fatalf("ToolsetReview = %+v, %v", review, err)
	}
	stale := wire.ReviewRequest{Tools: []wire.ToolDecision{{Name: "search", Fingerprint: "sha256:stale", Decision: wire.DecisionApprove}}}
	if _, err := c.ReviewToolset(ctx, "linear", stale); !errors.Is(err, agentclient.ErrConflict) {
		t.Errorf("approving another definition: %v, want ErrConflict", err)
	}
	ok := wire.ReviewRequest{Tools: []wire.ToolDecision{{Name: "search", Fingerprint: review.Tools[0].Fingerprint, Decision: wire.DecisionApprove}}}
	if got, err := c.ReviewToolset(ctx, "linear", ok); err != nil || got.Status != wire.ToolsetReady || got.Tools[0].Status != wire.ToolApproved {
		t.Fatalf("approving = %+v, %v; want ready", got, err)
	}
	if tools, err := c.Tools(ctx, "linear"); err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "search" {
		t.Errorf("Tools(linear) = %+v, %v; want search", tools, err)
	}

	off := false
	if got, err := c.UpdateToolset(ctx, "linear", wire.UpdateToolsetRequest{Enabled: &off}); err != nil || got.Enabled || got.Status != wire.ToolsetDisabled {
		t.Errorf("disabling = %+v, %v", got, err)
	}
	if err := c.DeleteToolset(ctx, "linear"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteToolset(ctx, "linear"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("deleting it again: %v, want ErrNotFound", err)
	}
}
