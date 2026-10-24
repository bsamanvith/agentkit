package toolrpc_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

type whoOut struct {
	Tenant string `json:"tenant"`
	User   string `json:"user"`
}

// A remote tool acts for the caller's tenant and user, over HTTP as in
// process, so a toolset with a user's credential slot works on a toolsets
// component; a call with no tenant reaches the tool with none.
func TestToolActsForTheCaller(t *testing.T) {
	t.Parallel()
	who, err := tool.Func("who", "Say who calls.", func(ctx context.Context, _ struct{}) (whoOut, error) {
		tn, _ := tenancy.From(ctx)
		u, _ := tenancy.User(ctx)
		return whoOut{Tenant: tn.ID(), User: u}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, overHTTP := range []bool{true, false} {
		reg := serve(t, []tool.Tool{who}, overHTTP).Registry("fx")
		for _, c := range []struct {
			ctx  context.Context
			want whoOut
		}{
			{tenancy.WithUser(tenancy.With(t.Context(), acme), "ann"), whoOut{"acme", "ann"}},
			{tenancy.With(t.Context(), acme), whoOut{"acme", ""}},
			{t.Context(), whoOut{}},
		} {
			out, err := reg.Invoke(c.ctx, "who", nil)
			if err != nil {
				t.Fatalf("http %v: %v", overHTTP, err)
			}
			var got whoOut
			if err := json.Unmarshal(out, &got); err != nil || got != c.want {
				t.Errorf("http %v: tool saw %s, want %+v", overHTTP, out, c.want)
			}
		}
	}
}
