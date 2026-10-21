// Package agenttools is the agentkit toolset: the agent API's agents as tools,
// so that Claude Code and other MCP clients can list the agents, run them,
// follow their runs and read their conversations. agentkit serve builds it
// for a toolset of type "agentkit" in mcp.toolsets and serves it at
// /mcp/<name> like any other toolset.
//
// The tools are
//
//   - agents_list: the agents the caller may use, with their toolsets;
//   - agent_run: run an agent on a message, in a new conversation or a given
//     one, and wait for the answer (see "Waiting" below);
//   - run_get: a run, as agent_run returns it;
//   - conversations_list: a page of the caller's conversations;
//   - conversation_get: a conversation and its messages.
//
// # Where the agents run
//
// The tools are a client of the agent API (/v1, package agentapi) through
// package agentclient, so they see exactly what that API shows the caller.
// With [WithHandler] (agentkit serve passes the agent API it runs in the same
// process) the requests go to that handler in process, through no socket.
// Otherwise the toolset's settings name the API's URL ("url"), as when the
// MCP server runs in another container than the agent API.
//
// # Who is calling
//
// The caller's own credential goes with every request: [ForwardCredentials],
// in front of the MCP server's authentication, keeps the Authorization header
// of each MCP request for these tools, and they send it to the agent API,
// which authenticates it again. So an MCP caller has the same tenant, scopes
// and agents as with the API: it sees only its tenant's conversations, and
// only the agents whose toolsets its key allows all of. A request with no
// credential is sent with none.
//
// # Waiting
//
// agent_run waits for the run to end, up to the "timeout" setting (default
// [DefaultTimeout]), and never longer than the MCP server's call timeout
// allows ([WithCallTimeout]). A run still going then is not stopped: the
// result has status "running" with the run and conversation IDs, and run_get
// follows it. The toolset keeps reading the run's events in the background
// until it ends, since the API interrupts a run nobody follows; [Factory.Close]
// stops that. If the MCP caller goes away while it waits, the run is left as
// a client that went away leaves it (the API's reconnect grace).
//
// A run that ended without answering (failed, canceled) is a result, not a
// tool error: its status says so and "error" says why. Errors before a run
// starts (an unknown agent or conversation, a busy conversation, a missing
// scope) are tool errors carrying the API's message.
//
// The toolset is not for agents: agentkit serve refuses an agent that names
// it. Agents that call agents are what package flow is for.
package agenttools
