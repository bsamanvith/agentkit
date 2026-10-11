package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/flow"
)

const approvePath = "demo/approve"

func (c cli) flowDemo(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit flow demo", `["<topic>"]`)
	var mf modelFlags
	mf.register(fs)
	state := fs.String("state", "agentkit-demo.json", "file holding the flow's checkpoints and transcripts")
	id := fs.String("id", "demo", "flow run id")
	approve := fs.Bool("approve", false, "approve the research and write the draft")
	reject := fs.String("reject", "", "reject the research with this reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 || (*approve && *reject != "") {
		fs.Usage()
		return errUsage
	}

	store, err := openFileStore(*state)
	if err != nil {
		return err
	}
	p := &printer{w: c.stdout}
	step, err := c.demoFlow(ctx, mf, store, p)
	if err != nil {
		return err
	}
	opts := []flow.Option{flow.WithCheckpointer(store)}

	var st *flow.State
	switch _, err := store.Load(ctx, *id); {
	case errors.Is(err, flow.ErrNotFound):
		if fs.NArg() != 1 || *approve || *reject != "" {
			fmt.Fprintf(c.stderr, "no flow %q in %s yet: start one with a topic\n", *id, *state)
			fs.Usage()
			return errUsage
		}
		initial := flow.NewState()
		if err := flow.Set(initial, "topic", fs.Arg(0)); err != nil {
			return err
		}
		st, err = flow.Run(ctx, *id, step, append(opts, flow.WithState(initial))...)
		p.end()
		if err != nil {
			return c.flowStopped(err, *state, *id)
		}
	case err != nil:
		return err
	default:
		if fs.NArg() == 1 {
			fmt.Fprintf(c.stderr, "flow %q already exists in %s: the topic is ignored\n", *id, *state)
		}
		switch {
		case *approve:
			opts = append(opts, flow.WithDecision(approvePath, flow.Decision{Approved: true}))
		case *reject != "":
			opts = append(opts, flow.WithDecision(approvePath, flow.Decision{Reason: *reject}))
		}
		st, err = flow.Resume(ctx, *id, step, opts...)
		p.end()
		if err != nil {
			return c.flowStopped(err, *state, *id)
		}
	}
	draft, err := flow.Get[agentstep.Output](st, "draft")
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "\n=== draft ===\n%s\n", draft.Answer)
	return nil
}

// flowStopped reports a flow that did not complete. Waiting for approval is
// not a failure.
func (c cli) flowStopped(err error, state, id string) error {
	if !errors.Is(err, flow.ErrSuspended) {
		return err
	}
	fmt.Fprintf(c.stdout, "\nWaiting for approval at %s. Continue with:\n"+
		"  agentkit flow demo -state %s -id %s -approve\n"+
		"  agentkit flow demo -state %s -id %s -reject \"<reason>\"\n", approvePath, state, id, state, id)
	return nil
}

// demoFlow is research → approve → write. Both agents save their transcripts
// in the store, and stream their events to p.
func (c cli) demoFlow(ctx context.Context, mf modelFlags, store *fileStore, p *printer) (flow.Step, error) {
	agent := func(name, system string, keys agentstep.Keys) (*agentstep.Step, error) {
		h, err := c.harness(ctx, mf, system)
		if err != nil {
			return nil, err
		}
		return agentstep.New(name, h, store.transcripts(), keys, agentstep.WithObserver(p.event))
	}
	research, err := agent("research",
		"You are a researcher. Collect the key facts about the topic in a short bulleted list. "+
			"Use the tools for anything they can answer, such as today's date or arithmetic.",
		agentstep.Keys{Input: "topic", Output: "research"})
	if err != nil {
		return nil, err
	}
	write, err := agent("write",
		"You are a writer. Write one short, clear paragraph from the research you are given.",
		agentstep.Keys{Input: "brief", Output: "draft"})
	if err != nil {
		return nil, err
	}
	brief := flow.Func("brief", func(_ context.Context, st *flow.State) error {
		topic, err := flow.Get[string](st, "topic")
		if err != nil {
			return err
		}
		res, err := flow.Get[agentstep.Output](st, "research")
		if err != nil {
			return err
		}
		return flow.Set(st, "brief", fmt.Sprintf("Topic: %s\n\nResearch:\n%s", topic, strings.TrimSpace(res.Answer)))
	})
	return flow.Sequence("demo", research, flow.Approve("approve"), brief, write), nil
}
