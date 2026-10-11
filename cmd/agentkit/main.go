// Command agentkit runs agents and flows.
//
//	agentkit run [flags] "<prompt>"            one agent against Claude, with the demo tools
//	agentkit flow demo [flags] ["<topic>"]     research → approval → write, resumable
//
// Credentials come from the Anthropic SDK's chain: ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN or an `ant auth login` profile.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli{stdout: os.Stdout, stderr: os.Stderr, newModel: newClaude}.main(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

// cli is the command line, with what tests replace.
type cli struct {
	stdout, stderr io.Writer
	newModel       func(modelFlags) (llm.Model, error)
}

const usage = `usage:
  agentkit run [flags] "<prompt>"          run one agent against Claude with the demo tools
  agentkit flow demo [flags] ["<topic>"]   research → approval → write; run again with -approve to continue

Run "agentkit run -h" or "agentkit flow demo -h" for flags.
`

// errUsage means the command line was wrong; the message is already printed.
var errUsage = errors.New("usage")

func (c cli) main(ctx context.Context, args []string) int {
	var err error
	switch {
	case len(args) >= 1 && args[0] == "run":
		err = c.run(ctx, args[1:])
	case len(args) >= 2 && args[0] == "flow" && args[1] == "demo":
		err = c.flowDemo(ctx, args[2:])
	default:
		fmt.Fprint(c.stderr, usage)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage), errors.Is(err, flag.ErrHelp):
		return 2
	default:
		fmt.Fprintln(c.stderr, "agentkit:", err)
		return 1
	}
}

// modelFlags choose the model and how hard it works.
type modelFlags struct {
	model    string
	effort   string
	maxTurns int
}

func (m *modelFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&m.model, "model", anthropic.DefaultModel, "Claude model ID")
	fs.StringVar(&m.effort, "effort", "", "effort: low, medium, high, xhigh or max (default: the adapter's, medium)")
	fs.IntVar(&m.maxTurns, "max-turns", 25, "maximum model calls per agent run")
}

func newClaude(f modelFlags) (llm.Model, error) {
	return anthropic.New(
		anthropic.WithModel(f.model),
		anthropic.WithOptions(anthropic.Options{Effort: anthropic.Effort(f.effort)}),
	)
}

// demoTools builds the built-in demo toolset (echo, current_time, calculate).
func demoTools(ctx context.Context) (tool.Registry, error) {
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		return nil, err
	}
	sets, _, err := toolset.Build(ctx, catalog, []toolset.Spec{{Name: "demo", Type: toolset.DemoType}})
	if err != nil {
		return nil, err
	}
	return sets["demo"], nil
}

// newFlagSet returns a flag set that reports errors to c.stderr instead of
// exiting.
func (c cli) newFlagSet(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() {
		fmt.Fprintf(c.stderr, "usage: %s [flags] %s\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}
