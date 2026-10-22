// Package client calls remote A2A agents: the client half of the
// Agent2Agent protocol, version 1.0 (E13, "Remote agents"), on net/http with
// the wire types of package a2a.
//
//	c, err := client.Connect(ctx, "https://agents.example.com/.well-known/agent-card.json",
//		client.Config{Header: http.Header{"Authorization": {"Bearer " + token}}})
//	task, err := c.Start(ctx, client.UserText("Summarize the open invoices."))
//	task, err = c.Wait(ctx, task.ID) // until it ends, or needs input
//	answer := client.Answer(task)
//
// # Cards
//
// [FetchCard] reads an Agent Card and [ValidateCard] checks it: a name, a
// description, a version, and at least one interface this client can use (the
// JSON-RPC or the HTTP+JSON binding, protocol version 1.x, an http(s) URL on
// the card's own origin). A card that fails is [ErrInvalidCard]. The client
// prefers JSON-RPC.
//
// # Methods
//
// SendMessage, SendStreamingMessage, GetTask, CancelTask and SubscribeToTask,
// on either binding. [Client.Start] sends a message and returns the task at
// once (returnImmediately), so a caller can keep the task's ID before it
// waits; [Client.Wait] follows a task until it is terminal or needs input,
// subscribing when the card says the agent streams and polling otherwise.
// Errors the agent answers with are *[a2a.Error]s that match the sentinels of
// package a2a (errors.Is(err, a2a.ErrTaskNotFound)); a refused credential or
// another HTTP failure is a *[StatusError]; a response that is not A2A is
// [ErrProtocol].
//
// # Credentials
//
// [Config.Header] is the agent's credential. It is sent to the card's URL and
// to the interface the client picked, which must be on the card's origin, and
// nowhere else: redirects are not followed, an interface on another origin is
// never used, and every value of the header is replaced by "[redacted]" in
// whatever the agent sends back (tasks, messages, errors) before a caller
// sees it.
//
// # Toolset
//
// [Factory] is the toolset type "a2a": one tool, ask_<name>, per configured
// remote agent, described from its card. Its output is labelled as
// third-party data (E10's prompt-injection rule). Package a2astep runs a
// remote agent as a flow step.
package client
