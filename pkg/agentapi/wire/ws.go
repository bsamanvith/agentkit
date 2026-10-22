package wire

import "encoding/json"

// The WebSocket binding of the API (T091, E18): one connection at [WSPath]
// that carries the same commands and event streams as the HTTP endpoints, as
// JSON text messages ([WSMessage]). It adds no message of its own to the
// API: a command's result is the HTTP response it stands for (its status and
// body), and a stream's events are the SSE stream's events, the same JSON.
//
//	client                                      server
//	{"type":"hello","id":"1","token":"..."}  →
//	                                         ←  {"type":"result","id":"1","status":200,"body":<Me>}
//	{"type":"subscribe","id":"2","run_id":"run_1","after":"12"} →
//	                                         ←  {"type":"result","id":"2","status":200,"run_id":"run_1",...}
//	                                         ←  {"type":"event","sub":"2","event":<Event>}  (each event)
//	                                         ←  {"type":"end","sub":"2"}                     (the stream ended)
//	{"type":"stop","id":"3","run_id":"run_1"} →
//	                                         ←  {"type":"result","id":"3","status":200,"body":<Run>}
//
// The first message is a hello: until it is answered with status 200, the
// connection takes nothing else. A later hello renews the credentials, which
// the commands after it are authorized by; a hello that fails closes the
// connection ([WSCloseUnauthorized]). Commands run concurrently: results
// come in the order they are ready, matched by id.
//
// A client too slow to read what its streams send is not waited for: each
// stream it has open gets a gap message, whose after is the ID of the last
// event the connection sent on it (to resume from, with subscribe), and the
// connection is closed ([WSCloseSlow]).

// WSPath is the WebSocket endpoint of the API.
const WSPath = "/v1/ws"

// Types of the messages a client sends ([WSMessage.Type]).
const (
	// WSHello authenticates the connection, and later renews it: Token is
	// its bearer token, CSRF the CSRF token of a session cookie (sent on
	// the upgrade). Without either, the upgrade request's credentials
	// stand. Its result is GET /v1/auth/me's.
	WSHello = "hello"
	// WSSubscribe opens a run's event stream (GET /v1/runs/{id}/events):
	// RunID, and After as its Last-Event-ID. A task is a run: its ID is
	// its run's.
	WSSubscribe = "subscribe"
	// WSUnsubscribe closes the stream Sub, which ends with an end message,
	// as closing the SSE response does.
	WSUnsubscribe = "unsubscribe"
	// WSSend starts or continues a run (POST /v1/runs), Body a
	// [RunRequest]; its events follow as a stream named by the message's
	// ID.
	WSSend = "send"
	// WSResume continues run RunID (POST /v1/runs/{id}/resume); its
	// events follow as a stream named by the message's ID.
	WSResume = "resume"
	// WSStop interrupts run RunID (POST /v1/runs/{id}/stop).
	WSStop = "stop"
	// WSCancel ends run RunID for good (POST /v1/runs/{id}/cancel).
	WSCancel = "cancel"
	// WSAnswer answers run RunID's interrupts (POST /v1/runs/{id}/input),
	// Body an [InputRequest].
	WSAnswer = "answer"
	// WSDecide answers a flow run's approval (POST
	// /v1/runs/{id}/decisions), Body a [DecisionRequest].
	WSDecide = "decide"
	// WSSteer adds a message to task RunID (POST /v1/tasks/{id}/messages),
	// Body a [SteerRequest]; Key is its Idempotency-Key.
	WSSteer = "steer"
	// WSPause pauses task RunID (POST /v1/tasks/{id}/pause).
	WSPause = "pause"
	// WSResumeTask resumes a paused task RunID (POST
	// /v1/tasks/{id}/resume).
	WSResumeTask = "resume_task"
)

// Types of the messages the server sends ([WSMessage.Type]).
const (
	// WSResult answers the client's message ID: the HTTP status, and the
	// response body (absent for one with none, such as a stream's). The
	// result of a command that opens a stream (subscribe, send, resume)
	// with status 200 also has the run's RunID and ConversationID, and is
	// followed by the stream's messages, named by Sub (the command's ID).
	WSResult = "result"
	// WSEvent is an event of stream Sub: Event, as the SSE stream's data.
	WSEvent = "event"
	// WSEnd ends stream Sub, as the end of an SSE response does: after its
	// last event, or when the client unsubscribed.
	WSEnd = "end"
	// WSGap ends stream Sub because the client read too slowly: After is
	// the ID of the last event the connection sent on it ("" for none), to
	// subscribe from again. The connection then closes ([WSCloseSlow]).
	WSGap = "gap"
)

// Close codes of the WebSocket connection, besides RFC 6455's.
const (
	// WSCloseUnauthorized: no hello came in time, a message came before
	// it, or a hello was refused.
	WSCloseUnauthorized = 4001
	// WSCloseSlow: the client read too slowly; each of its streams got a
	// gap message first.
	WSCloseSlow = 4008
)

// WSMessage is one message of the WebSocket binding, either way. Which
// fields it has depends on Type.
type WSMessage struct {
	Type string `json:"type"`
	// ID names a client's message, and the result that answers it. It is
	// unique among the client's messages not yet answered and streams open.
	ID string `json:"id,omitempty"`
	// Sub is the stream an event, end, gap or unsubscribe is about: the ID
	// of the message that opened it.
	Sub string `json:"sub,omitempty"`

	// hello
	Token string `json:"token,omitempty"`
	CSRF  string `json:"csrf,omitempty"`

	// The run a command or subscribe is about; in a stream's result, the
	// run streamed.
	RunID string `json:"run_id,omitempty"`
	// subscribe: the Last-Event-ID; gap: the last event's ID sent.
	After string `json:"after,omitempty"`
	// A command's Idempotency-Key (steer).
	Key string `json:"key,omitempty"`
	// A command's request body; a result's response body.
	Body json.RawMessage `json:"body,omitempty"`

	// result
	Status         int    `json:"status,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	// RetryAfter is the response's Retry-After, on a 503 or 429.
	RetryAfter string `json:"retry_after,omitempty"`

	// event
	Event json.RawMessage `json:"event,omitempty"`
}
