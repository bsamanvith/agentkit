package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The tenants of the suites. unusedTenant never writes anything; the
// invariant checks read every conversation and run through it too. tenantC
// sorts after the others, for the cross-tenant order of RunAdmin.
const (
	tenantA      = "acme"
	tenantB      = "globex"
	tenantC      = "umbrella"
	unusedTenant = "initech"
)

// Times no store can keep (outside 1678 to 2262, what int64 Unix nanoseconds
// hold), for the rule that a time to keep must be in range and a time a read
// only compares with may be anything. farPast is not the zero time, which
// means unset.
var (
	farFuture = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	farPast   = time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
)

func tenantCtx(ctx context.Context, t *testing.T, id string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatalf("NewTenant(%q): %v", id, err)
	}
	return tenancy.With(ctx, tn)
}

// Messages and records are chosen so that any re-encoding shows in their
// bytes: ToolUse input with unsorted keys and number literals a generic
// decoder rewrites (1.50, 1e2), HTML characters, escapes and non-ASCII text,
// and Opaque data with whitespace. A record has whitespace and unsorted keys.

func opaque(i int) llm.Opaque {
	return llm.Opaque{
		Provider: "anthropic",
		Data:     json.RawMessage(fmt.Sprintf("{ \"type\" : \"thinking\", \"signature\":\"sig-%d<&>\",\n \"n\": 1.50 }", i)),
	}
}

func toolUse(i int) llm.ToolUse {
	return llm.ToolUse{
		ID:    fmt.Sprintf("toolu_%d", i),
		Name:  "lookup",
		Input: json.RawMessage(fmt.Sprintf(`{"z": %d, "a": [1e2, 1.50, "<&>é"], "m": {"y": null, "b": true}}`, i)),
	}
}

func promptMessages(i int) []llm.Message {
	return []llm.Message{llm.User(llm.Text{Text: fmt.Sprintf("question %d: <b>&</b> é ✓", i)})}
}

func turnMessages(i int) []llm.Message {
	return []llm.Message{
		llm.Assistant(opaque(i), llm.Text{Text: fmt.Sprintf("answer %d", i)}, toolUse(i)),
		llm.User(llm.ToolResult{ToolUseID: fmt.Sprintf("toolu_%d", i), Content: "result <&>", IsError: i%2 == 0}),
	}
}

func record(i int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("{\"z\": %d,  \"a\" : 1.50 }", i))
}

// promptAt, turnAt and compactionAt build entries at seq; i makes the content
// unique, so two entries with different i are never identical.
func promptAt(seq int64, i int) store.Entry {
	return store.Entry{Seq: seq, Kind: store.KindPrompt, Messages: promptMessages(i), Record: record(i), RunID: fmt.Sprintf("run-%d", i)}
}

func turnAt(seq int64, i int) store.Entry {
	return store.Entry{Seq: seq, Kind: store.KindTurn, Messages: turnMessages(i), Record: record(i), RunID: fmt.Sprintf("run-%d", i)}
}

func compactionAt(seq int64, i int) store.Entry {
	return store.Entry{
		Seq:      seq,
		Kind:     store.KindCompaction,
		Messages: []llm.Message{llm.User(llm.Text{Text: fmt.Sprintf("summary %d", i)}), llm.Assistant(opaque(i))},
		Record:   record(i),
		Covers:   seq - 1,
	}
}

// snapshot is what must survive a round trip through a store: everything in an
// entry but CreatedAt, with the messages as the JSON they marshal to.
type snapshot struct {
	Kind     store.Kind
	Messages string
	Record   string
	RunID    string
	Covers   int64
}

func snap(e store.Entry) (snapshot, error) {
	b, err := json.Marshal(e.Messages)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{Kind: e.Kind, Messages: string(b), Record: string(e.Record), RunID: e.RunID, Covers: e.Covers}, nil
}

func mustSnap(t *testing.T, e store.Entry) snapshot {
	t.Helper()
	s, err := snap(e)
	if err != nil {
		t.Fatalf("marshal messages of entry %d: %v", e.Seq, err)
	}
	return s
}

func seqs(es []store.Entry) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.Seq
	}
	return out
}
