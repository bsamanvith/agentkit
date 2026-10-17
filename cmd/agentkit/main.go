// Command agentkit runs agents, flows and servers.
//
//	agentkit run [flags] "<prompt>"            one agent against Claude, with the demo tools
//	agentkit flow demo [flags] ["<topic>"]     research → approval → write, resumable
//	agentkit serve [-config file]              serve the components in $AGENTKIT_COMPONENTS
//	agentkit chat [flags]                      talk to an agent on a server, interactively
//	agentkit ask [flags] "<message>" | -       one run on a server, for scripts (-json: JSONL events)
//	agentkit create [flags]                    create an empty conversation on a server; prints its ID
//	agentkit migrate                           apply the Postgres store's migrations to $AGENTKIT_STORE
//
// Credentials come from the Anthropic SDK's chain: ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN or an `ant auth login` profile.
//
// serve reads AGENTKIT_COMPONENTS (default mcp-server), AGENTKIT_ADDR
// (default 127.0.0.1:8080), AGENTKIT_LOG_LEVEL (default info),
// AGENTKIT_CONFIG (the config file, if -config is not given) and
// AGENTKIT_SHUTDOWN_TIMEOUT (default 15s). It logs JSON to stderr, answers
// /healthz, /readyz and /version, and shuts down gracefully on SIGINT or
// SIGTERM.
//
// The components are agent (the agent API), web (the console), mcp-server,
// runner and toolsets. Split across containers (docs/COMPONENTS.md), the
// agent API runs its agents on the runner at AGENTKIT_RUNNER_URL, the runner
// and the API take their toolsets from AGENTKIT_TOOLSETS_URL, and
// AGENTKIT_RPC_TOKEN is the token between them.
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
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Build information, set with
// -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  string
	date    string
)

func main() {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if len(os.Args) > 1 && os.Args[1] == "chat" {
		sigs = sigs[1:] // chat takes Ctrl-C itself: it cancels a run, not the program
	}
	ctx, stop := signal.NotifyContext(context.Background(), sigs...)
	code := cli{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, newModel: buildModel}.main(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

// cli is the command line, with what tests replace.
type cli struct {
	stdout, stderr io.Writer
	getenv         func(string) string // nil: os.Getenv
	newModel       func(modelFlags) (llm.Model, error)
	catalog        func() (*toolset.Catalog, error) // nil: newCatalog
	term           clientTerm                       // chat's and ask's input, Ctrl-C and waits
}

// toolsets returns the catalog of connectors: the compiled-in one, or the
// one a test put in its place (fakes, so no test reaches a paid API).
func (c cli) toolsets() (*toolset.Catalog, error) {
	if c.catalog == nil {
		return newCatalog()
	}
	return c.catalog()
}

func (c cli) env(key string) string {
	if c.getenv == nil {
		return os.Getenv(key)
	}
	return c.getenv(key)
}

const usage = `usage:
  agentkit run [flags] "<prompt>"          run one agent against Claude with the demo tools
  agentkit flow demo [flags] ["<topic>"]   research → approval → write; run again with -approve to continue
  agentkit serve [-config file]            serve the components in $AGENTKIT_COMPONENTS (default mcp-server)
  agentkit healthcheck [-ready]            exit 0 if the server on $AGENTKIT_ADDR is live (or ready)
  agentkit chat [flags]                    talk to an agent on a server ($AGENTKIT_SERVER), interactively
  agentkit ask [flags] "<message>" | -     one run on a server, for scripts; -json prints the events as JSONL
  agentkit create [flags]                  create an empty conversation on a server; prints its ID for -conversation
  agentkit migrate                         apply the Postgres store's migrations to $AGENTKIT_STORE, then exit

Run "agentkit <command> -h" for flags.
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
	case len(args) >= 1 && args[0] == "serve":
		err = c.serve(ctx, args[1:])
	case len(args) >= 1 && args[0] == "healthcheck":
		err = c.healthcheck(ctx, args[1:])
	case len(args) >= 1 && args[0] == "migrate":
		err = c.migrate(ctx, args[1:])
	case len(args) >= 1 && args[0] == "chat":
		return c.chat(ctx, args[1:])
	case len(args) >= 1 && args[0] == "ask":
		return c.ask(ctx, args[1:])
	case len(args) >= 1 && args[0] == "create":
		return c.create(ctx, args[1:])
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
	provider string // empty for defaultProvider
	model    string
	effort   string
	maxTurns int
}

func (m *modelFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&m.model, "model", defaultModelID, "Claude model ID")
	fs.StringVar(&m.effort, "effort", "", "effort: low, medium, high, xhigh or max (default: the adapter's, medium)")
	fs.IntVar(&m.maxTurns, "max-turns", 25, "maximum model calls per agent run")
}

// demoTools builds the built-in demo toolset (echo, current_time, calculate).
func (c cli) demoTools(ctx context.Context) (tool.Registry, error) {
	catalog, err := c.toolsets()
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
