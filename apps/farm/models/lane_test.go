package models_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/models"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

var (
	claude = models.Name{Provider: "anthropic", ID: "claude-x"}
	gpt    = models.Name{Provider: "openai", ID: "gpt-x"}
	flash  = models.Name{Provider: "gemini", ID: "flash-x"}
)

// allKeys gives every provider a key.
func allKeys(string) string { return "key" }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func ask(t *testing.T, m llm.Model) (string, error) {
	t.Helper()
	r, err := llm.Collect(m.Generate(t.Context(), llm.Request{Messages: []llm.Message{llm.User(llm.Text{Text: "hi"})}}))
	if err != nil {
		return "", err
	}
	return r.Message.Content[0].(llm.Text).Text, nil
}

func states(l *models.Lane) map[string]models.State {
	out := map[string]models.State{}
	for _, s := range l.Statuses() {
		out[s.Model] = s.State
	}
	return out
}

func TestLaneUsesFirstModel(t *testing.T) {
	t.Parallel()
	a, b := llmtest.NewFake(t, llmtest.Text("claude"), llmtest.Text("claude again")), llmtest.NewFake(t)
	l := models.New([]models.Name{claude, gpt}, models.AllowUnevaluated(), models.WithGetenv(allKeys),
		models.WithModel(claude, a), models.WithModel(gpt, b))
	for _, want := range []string{"claude", "claude again"} {
		if got, err := ask(t, l); err != nil || got != want {
			t.Fatalf("reply = %q, %v; want %q", got, err, want)
		}
	}
	if l.Active() != claude.String() || l.Len() != 2 {
		t.Errorf("Active = %s, Len = %d", l.Active(), l.Len())
	}
}

// TestLaneHandsOn: a model that fails before its reply starts, with an error
// another could avoid, hands the same request to the next, then rests; when
// its rest ends it is first again.
func TestLaneHandsOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		rest time.Duration
	}{
		{llm.ErrOverloaded, models.RestTransient},
		{llm.ErrRateLimited, models.RestTransient},
		{llm.ErrAuth, models.RestAuth},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			t.Parallel()
			clk := &clock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
			a := llmtest.NewFake(t, llmtest.Fail(tc.err), llmtest.Text("claude is back"))
			b := llmtest.NewFake(t, llmtest.Text("gpt"), llmtest.Text("gpt while claude rests"))
			l := models.New([]models.Name{claude, gpt}, models.AllowUnevaluated(), models.WithGetenv(allKeys),
				models.WithClock(clk.now), models.WithModel(claude, a), models.WithModel(gpt, b))
			if got, err := ask(t, l); err != nil || got != "gpt" {
				t.Fatalf("reply = %q, %v; want gpt's", got, err)
			}
			if b.Requests()[0].Messages[0].Content[0].(llm.Text).Text != "hi" {
				t.Error("the next model did not get the same request")
			}
			if st := states(l); st[claude.String()] != models.StateResting || l.Active() != gpt.String() {
				t.Errorf("after the failure: %v, Active %s", st, l.Active())
			}
			clk.add(tc.rest - time.Second)
			if got, _ := ask(t, l); got != "gpt while claude rests" {
				t.Errorf("during the rest: reply %q", got)
			}
			clk.add(time.Second)
			if got, _ := ask(t, l); got != "claude is back" {
				t.Errorf("after the rest: reply %q", got)
			}
			if st := states(l); st[claude.String()] != models.StateReady {
				t.Errorf("after recovering: %v", st)
			}
		})
	}
}

// TestLaneKeepsRequestErrors: errors the request causes, errors after the
// reply started and errors of the last model are the caller's, unchanged.
func TestLaneKeepsRequestErrors(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]llmtest.Reply{
		"invalid":   llmtest.Fail(llm.ErrInvalidRequest),
		"too long":  llmtest.Fail(llm.ErrContextTooLong),
		"mid-reply": {Response: llmtest.Text("a long reply that breaks").Response, Err: llm.ErrOverloaded, After: 2},
		"cancelled": llmtest.Fail(context.Canceled),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, b := llmtest.NewFake(t, reply), llmtest.NewFake(t)
			l := models.New([]models.Name{claude, gpt}, models.AllowUnevaluated(), models.WithGetenv(allKeys),
				models.WithModel(claude, a), models.WithModel(gpt, b))
			_, err := ask(t, l)
			if !errors.Is(err, reply.Err) {
				t.Errorf("err = %v, want %v", err, reply.Err)
			}
			if len(b.Requests()) != 0 {
				t.Error("the request was handed on")
			}
		})
	}
	t.Run("last model", func(t *testing.T) {
		t.Parallel()
		a, b := llmtest.NewFake(t, llmtest.Fail(llm.ErrOverloaded)), llmtest.NewFake(t, llmtest.Fail(llm.ErrRateLimited))
		l := models.New([]models.Name{claude, gpt}, models.AllowUnevaluated(), models.WithGetenv(allKeys),
			models.WithModel(claude, a), models.WithModel(gpt, b))
		if _, err := ask(t, l); !errors.Is(err, llm.ErrRateLimited) {
			t.Errorf("err = %v, want the last model's", err)
		}
		if st := states(l); st[claude.String()] != models.StateResting || st[gpt.String()] != models.StateResting {
			t.Errorf("states %v, want both resting", st)
		}
	})
}

// TestLaneLeavesOut: models without a key, or not evaluated, are not in the
// lane, and say why; an empty lane fails its requests.
func TestLaneLeavesOut(t *testing.T) {
	t.Parallel()
	onlyGemini := func(k string) string {
		if k == "GEMINI_KEY" {
			return "key"
		}
		return ""
	}
	l := models.New([]models.Name{claude, flash}, models.WithGetenv(onlyGemini), models.WithModel(flash, llmtest.NewFake(t)))
	st := states(l)
	if st[claude.String()] != models.StateNoKey || st[flash.String()] != models.StateNotEvaluated || l.Len() != 0 {
		t.Errorf("states %v, Len %d", st, l.Len())
	}
	if l.Active() != "off" {
		t.Errorf("Active = %s", l.Active())
	}
	if _, err := ask(t, l); !errors.Is(err, models.ErrNoModel) {
		t.Errorf("empty lane: err = %v", err)
	}
	all := l.Statuses()
	if len(all) != 2 || all[0].Model != claude.String() || all[0].Reason == "" || all[1].Reason == "" {
		t.Errorf("Statuses = %+v, want both in list order with a reason", all)
	}
}

func TestLaneStatusesInOrder(t *testing.T) {
	t.Parallel()
	l := models.New([]models.Name{flash, claude, gpt}, models.AllowUnevaluated(), models.WithGetenv(allKeys),
		models.WithModel(flash, llmtest.NewFake(t)), models.WithModel(claude, llmtest.NewFake(t)), models.WithModel(gpt, llmtest.NewFake(t)))
	s := l.Statuses()
	for i, n := range []models.Name{flash, claude, gpt} {
		if s[i].Model != n.String() || s[i].State != models.StateReady || s[i].Cost == "" {
			t.Errorf("status %d = %+v", i, s[i])
		}
	}
}
