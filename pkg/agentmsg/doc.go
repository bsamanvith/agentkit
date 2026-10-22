// Package agentmsg lets agents message each other (T073): mailboxes in the
// store's message log ([store.MessageLog]), the tools an agent sends and reads
// with, and the dispatcher that hands each message to its recipient as a
// background run.
//
// # Tools
//
// An agent whose config lets it message others ([Agent.CanMessage]) gets
// three tools ([Messenger.Tools]):
//
//   - agents_list: the agents it may message, with their descriptions;
//   - agent_message(to, text, thread?, wait?, timeout?): send a message to
//     "agent:<name>", in a new thread or a given one (a run that a message
//     started sends in that message's thread, so that every send of a
//     loop counts against one thread's hop limit). With wait the call
//     returns the reply (or a timeout with the thread ID); without, it
//     returns the thread ID at once and the reply wakes the sender later;
//   - thread_read(thread): the messages of a thread the agent is part of.
//
// "a2a:" addresses are refused with a clear error until E13's T068.
//
// # Delivery
//
// A message is stored first, then delivered at least once by a [Dispatcher]:
// it claims the next due message with a lease ([store.MessageAdmin]) and
// starts a background run of the recipient in the message's conversation
// (one per thread and agent, made on first use) through [Runs]. Messages of a
// thread to one conversation are processed in order, and one whose
// conversation is busy waits until its run has ended. Processing is once per
// message ID: before it starts a run, the dispatcher looks for a prompt of
// that message in the conversation since the message was first delivered, so
// a dispatcher that stopped between the two steps does not run it twice.
//
// When the run of a request ends, its answer goes back as a reply to the
// sender's conversation. A sender that waits ([wait: true]) takes the reply
// in its tool call; one that does not is woken: the reply starts a
// background run in its conversation, as any message does. A reply and a
// notice get no reply of their own, so a thread ends unless an agent sends
// again.
//
// # Trust
//
// A message reaches its recipient as data, never as instructions: the run's
// input is a fixed label saying that what follows is a message from another
// agent, then the message as one line of JSON (sender, thread, text), so its
// text can never pass for the system prompt or the user's words.
//
// # Limits
//
// The sender must list the recipient in CanMessage. A thread has a hop
// limit, and a token and time budget ([Limits]); a send that would go over
// one stops the thread: the sender gets the error, and every other
// conversation in the thread a notice saying why. Each agent's sends are
// rate limited per tenant. A stopped thread takes no more messages.
//
// # Who sends
//
// The tools learn the sending agent and conversation from the context
// ([WithSender]); agentkit serve sets it for every run through the agent
// API's run context (agentapi.WithRunContext).
package agentmsg
