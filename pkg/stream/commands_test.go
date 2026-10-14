package stream_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/stream"
)

func steer(i int) store.Command {
	return store.Command{Key: fmt.Sprint("k", i), Type: "steer", Data: json.RawMessage(fmt.Sprintf(`{"text":"%d"}`, i))}
}

// leasedRun makes run runID, leased to owner.
func leasedRun(ctx context.Context, t *testing.T, s *memory.Store, runID, owner string) {
	t.Helper()
	if _, err := s.Create(ctx, runID, "conv"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, runID, owner, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func send(ctx context.Context, t *testing.T, c *stream.Commands, runID string, cmd store.Command) store.Command {
	t.Helper()
	got, err := c.Send(ctx, runID, cmd)
	if err != nil {
		t.Fatalf("Send(%q, %s): %v", runID, cmd.Key, err)
	}
	return got
}

func pendingSeqs(t *testing.T, in *stream.Inbox) []int64 {
	t.Helper()
	cmds, err := in.Pending(0)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, c := range cmds {
		seqs = append(seqs, c.Seq)
	}
	return seqs
}

// Commands sent before the run's process opened its inbox, or while it was
// down, are pending when it does; a signal wakes it for the ones sent
// after; consuming takes them off.
func TestCommandsReachTheRun(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		s := memory.New()
		leasedRun(ctx, t, s, "run", "a")
		c, err := stream.NewCommands(s, b.bus, quiet)
		if err != nil {
			t.Fatal(err)
		}
		send(ctx, t, c, "run", steer(1))
		send(ctx, t, c, "run", steer(2))
		in, err := c.Open(ctx, "run", "a")
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		if got := pendingSeqs(t, in); fmt.Sprint(got) != "[1 2]" {
			t.Errorf("pending at open = %v, want [1 2]", got)
		}
		awaitWake(ctx, t, c, in, 3)
		if err := in.Consume(2); err != nil {
			t.Fatal(err)
		}
		if got := pendingSeqs(t, in); len(got) == 0 || got[0] != 3 {
			t.Errorf("pending after consuming through 2 = %v, want from 3", got)
		}
		// A retry with the same key is the same command, and signals
		// nothing new.
		if again := send(ctx, t, c, "run", steer(1)); again.Seq != 1 {
			t.Errorf("a retry was stored at %d, want 1", again.Seq)
		}
		// Only the lease holder consumes.
		other, err := c.Open(ctx, "run", "b")
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		if err := other.Consume(3); !errors.Is(err, store.ErrLeased) {
			t.Errorf("another owner's Consume: err = %v, want ErrLeased", err)
		}
	})
}

// A command whose signal failed is stored all the same.
func TestCommandSignalFails(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	s := memory.New()
	leasedRun(ctx, t, s, "run", "a")
	f := &faulty{Bus: memoryBackend(t, roomy).bus}
	c, _ := stream.NewCommands(s, f, quiet)
	f.failNext(1)
	if got := send(ctx, t, c, "run", steer(1)); got.Seq != 1 {
		t.Errorf("stored at %d, want 1", got.Seq)
	}
	in, _ := c.Open(ctx, "run", "a")
	defer in.Close()
	if got := pendingSeqs(t, in); fmt.Sprint(got) != "[1]" {
		t.Errorf("pending = %v, want [1]", got)
	}
}

// An inbox whose topic loses its history while it waits wakes, in case a
// command went with it.
func TestInboxWakesAfterAGap(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	s := memory.New()
	leasedRun(ctx, t, s, "run", "a")
	b := memoryBackend(t, roomy)
	c, _ := stream.NewCommands(s, b.bus, quiet)
	in, _ := c.Open(ctx, "run", "a")
	defer in.Close()
	f := &faulty{Bus: b.bus}
	cf, _ := stream.NewCommands(s, f, quiet)
	awaitWake(ctx, t, c, in, 1)
	// The signal for 2 fails; the one for 3 is then a gap, not a message.
	f.failNext(1)
	send(ctx, t, cf, "run", steer(2))
	send(ctx, t, c, "run", steer(3))
	select {
	case <-in.Wake():
	case <-time.After(wait):
		t.Fatal("no wake after a gap")
	}
}

// awaitWake sends commands from i on until one wakes the inbox (the first
// may beat its subscription), and returns the next i.
func awaitWake(ctx context.Context, t *testing.T, c *stream.Commands, in *stream.Inbox, i int) int {
	t.Helper()
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); i++ {
		send(ctx, t, c, "run", steer(i))
		select {
		case <-in.Wake():
			return i + 1
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("no wake after sending commands")
	return i
}

func TestCommandsErrors(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	s := memory.New()
	b := memoryBackend(t, roomy).bus
	if _, err := stream.NewCommands(nil, b); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("NewCommands(nil, b): err = %v, want ErrInvalid", err)
	}
	c, _ := stream.NewCommands(s, b)
	if _, err := c.Send(ctx, "nope", steer(1)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Send to no run: err = %v, want ErrNotFound", err)
	}
	if _, err := c.Send(ctx, "", steer(1)); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("Send to an empty run ID: err = %v, want ErrInvalid", err)
	}
	if _, err := c.Send(t.Context(), "run", steer(1)); err == nil {
		t.Error("Send without a tenant succeeded")
	}
	if _, err := c.Open(ctx, "run", ""); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("Open with no owner: err = %v, want ErrInvalid", err)
	}
	in, err := c.Open(ctx, "run", "a")
	if err != nil {
		t.Fatal(err)
	}
	in.Close()
	if _, err := in.Pending(0); err == nil {
		t.Error("Pending after Close succeeded")
	}
}
