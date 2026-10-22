// Package servers adds external MCP servers as toolsets at run time (T096,
// E19): an admin gives a URL, the [Manager] probes the server and stores it
// as a toolset of the admin's tenant (store.ToolsetStore), and the admin
// reviews its tools before any agent may call them. It implements
// agentapi.ToolsetAdmin, and [Manager.Registry] gives a harness the tools.
//
// Every tool is pinned. Approving a tool stores the fingerprint of the
// definition the admin saw (client.Fingerprint). An agent is offered a
// tool only while the server lists it with that fingerprint: a tool the
// server adds is new, and one it changes (a "rug pull") is changed, and
// both stay off until approved, whether the change shows on connecting or
// in a notifications/tools/list_changed. A call checks again, so a tool
// that changed between a turn's listing and its call fails rather than
// run.
//
// Every connection goes through a netguard.Guard: https only, on a public
// address checked as the connection is made, redirects too, unless the
// operator allows the host. Only the HTTP transports are used: a web
// request never makes the server run a command. Header values are fixed,
// or name a variable of the server's environment that the operator allows
// ([WithHeaderEnv]); a header that carries a secret must name one, so a
// secret is never stored.
//
// Tools are named "<toolset>__<tool>", so two servers cannot shadow each
// other. A toolset's connection is made when it is first used and kept,
// one per tenant and toolset, until its settings change or it is removed.
//
// A toolset with auth "oauth" ([WithOAuth], T095) signs in with MCP OAuth
// (package mcp/oauth): its authorization server is found and pinned when
// it is added, and it is not probed then, since its tools are listed
// through a connection. Its scope says whose: "tenant", one connection the
// tenant shares, or "user", each user's own. The connection is made in the
// agent API's connect flow ([Manager.Dynamic], one slot per toolset) and
// kept in the vault; whose it is and that it is usable is checked on every
// use, and a connection is kept per tenant, toolset and user. A user who
// has not connected is offered the approved tools and gets "not
// connected" from them, without the server being called; a refused
// refresh gives "reconnect needed", and a missing scope "needs more
// access".
package servers
