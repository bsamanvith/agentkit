// Package agentclient is the Go client of the agent API (`/v1`, served by
// [github.com/bsamanvith/agentkit/pkg/agentapi]): start a run and read its
// events as they come, or wait for it as JSON; continue a conversation; read
// runs and conversations; list the agents, conversations and toolsets. The CLI, headless runs, the end-to-end tests and the
// evals all use it, so the API has one tested client.
//
// It speaks only the wire format ([wire]) over net/http, and imports neither
// the server nor the harness.
//
// # Runs
//
//	c, err := agentclient.New("http://localhost:8080", agentclient.WithToken(token))
//	s, err := c.Stream(ctx, agentclient.RunRequest{Message: "hi"})
//	defer s.Close()
//	for {
//		e, err := s.Next()
//		if errors.Is(err, io.EOF) {
//			break // after run_done or run_error
//		}
//		if err != nil {
//			return err // the stream broke: ErrStreamTruncated, ...
//		}
//		// e.Type is wire.TypeTextDelta, wire.TypeToolCall, ...
//	}
//
// A run that has started is not a Go error, whatever its outcome: the stream
// ends with a run_done or a run_error event, and [Client.Run] returns a
// [wire.Run] whose Status says which (and whose Error is the run's error).
// Pass [Stream.ConversationID] in the next [RunRequest] to continue the
// conversation.
//
// Closing a stream before its last event cancels the run on the server: the
// run ends with the error code "cancelled", and its conversation keeps the
// completed turns (it is "interrupted" if the run stopped before answering;
// post an empty message to resume it).
//
// # Agents, conversations and toolsets
//
// A server runs one or more agents ([Client.Agents], [Client.Agent]); a run
// names one in [RunRequest].Agent, or gets the default. A conversation stays
// with its agent. [Client.Conversations] pages through the caller's
// conversations, newest first, and [Client.DeleteConversation] deletes one.
// [Client.Toolsets] and [Client.Tools] describe the toolsets the agents use,
// and [Client.MCPURL] where each is served over MCP.
//
// # Errors
//
// An HTTP error is an [*APIError] with the status, the API's error code and
// Retry-After. It matches the sentinel of its code with errors.Is:
// [ErrInvalidRequest], [ErrUnauthorized], [ErrNotFound], [ErrRequestTooLarge],
// [ErrConversationBusy], [ErrConversationInterrupted], [ErrAgentMismatch],
// [ErrOverCapacity] and [ErrShuttingDown]. A code this client does not know
// matches none of them; its Code is still set.
//
// A stream that ends without run_done or run_error gives
// [ErrStreamTruncated]; one that breaks the wire format gives
// [ErrBadResponse]; an event larger than the limit gives
// [ErrResponseTooLarge].
//
// # The additive rule
//
// The wire format only grows within a version. The client ignores fields it
// does not know, messages and their blocks included; keeps a content block of
// a type it does not know as an
// [github.com/bsamanvith/agentkit/pkg/llm.Unknown] block; and passes events of
// a type it does not know through [Stream.Next] like any other (they never end
// the stream).
// [Stream.RawData] is each event's JSON as the server sent it, unknown fields
// included, for callers that print or forward events.
package agentclient
