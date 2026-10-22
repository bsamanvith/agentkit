package agentclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Sentinels of the API's HTTP error codes: an [*APIError] with the code
// matches its sentinel with errors.Is.
var (
	// ErrInvalidRequest: 400 invalid_request, a malformed request or an empty
	// message.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrUnauthorized: 401 unauthorized, a missing or wrong token.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrNotFound: 404 not_found, an unknown (or evicted) run or conversation.
	ErrNotFound = errors.New("not found")
	// ErrRequestTooLarge: 413 too_large, a message over the server's limit.
	ErrRequestTooLarge = errors.New("request too large")
	// ErrConversationBusy: 409 conversation_busy, a run of the conversation
	// is going on ([APIError.RunID]); retry when it has ended. Also 409
	// run_leased: the run asked to resume is going on.
	ErrConversationBusy = errors.New("conversation busy")
	// ErrAgentMismatch: 409 agent_mismatch, the run names another agent than
	// the conversation's; leave Agent empty to continue it.
	ErrAgentMismatch = errors.New("agent mismatch")
	// ErrConversationInterrupted: 409 conversation_interrupted, which
	// servers since T037 no longer return: a new message to a conversation
	// whose run was interrupted starts a new run.
	ErrConversationInterrupted = errors.New("conversation interrupted")
	// ErrRunState: 409, the run's state does not allow the request: it ended
	// for good (run_final), waits for a decision (awaiting_decision), has no
	// approval at the path decided (not_pending), or its flow changed since
	// it suspended (shape_changed). [APIError.Code] says which.
	ErrRunState = errors.New("run state does not allow the request")
	// ErrOverCapacity: 503 over_capacity, too many runs at once; retry after
	// [APIError.RetryAfter].
	ErrOverCapacity = errors.New("server over capacity")
	// ErrShuttingDown: 503 shutting_down, the server is draining; retry after
	// [APIError.RetryAfter], possibly on another replica.
	ErrShuttingDown = errors.New("server shutting down")
	// ErrTooManyBackgroundRuns: 429 too_many_background_runs, the tenant has
	// as many tasks going on as the server allows; retry after
	// [APIError.RetryAfter].
	ErrTooManyBackgroundRuns = errors.New("too many background runs")
)

// Errors of the client itself.
var (
	// ErrStreamTruncated: the event stream ended, or broke, before its
	// run_done or run_error event. The run's outcome is unknown to the
	// client; [Client.GetRun] may tell.
	ErrStreamTruncated = errors.New("event stream ended before the run did")
	// ErrToolsetExists: a toolset has the name of the one being added
	// (409).
	ErrToolsetExists = errors.New("toolset exists")
	// ErrConflict: what the request was based on changed since it was read,
	// such as a tool approved while the server changed it (409). Read it
	// again.
	ErrConflict = errors.New("conflict")
	// ErrServerUnreachable: an MCP server being added could not be reached
	// or listed (422).
	ErrServerUnreachable = errors.New("MCP server unreachable")
	// ErrAuthRequired: an MCP server being added wants a credential the
	// request did not give (422).
	ErrAuthRequired = errors.New("MCP server requires a credential")
	// ErrURLNotAllowed: the tenant's mcp_servers.allow does not include the
	// URL of an MCP server being added (403 url_not_allowed).
	ErrURLNotAllowed = errors.New("MCP server URL not allowed")
	// ErrRegistryUnavailable: the MCP Registry could not be searched (502
	// registry_unavailable); adding a server by URL still works.
	ErrRegistryUnavailable = errors.New("MCP registry unavailable")
	// ErrBadResponse: the server's response is not what the API defines (not
	// an event stream or JSON, an event that is not a JSON object with a
	// type, a body that does not decode).
	ErrBadResponse = errors.New("bad response from the agent API")
	// ErrResponseTooLarge: an SSE line, an event or a JSON body is larger
	// than [WithMaxResponseBytes].
	ErrResponseTooLarge = errors.New("response larger than the client's limit")
	// ErrClosed: [Stream.Next] after [Stream.Close].
	ErrClosed = errors.New("stream closed")
	// ErrInvalidDefinition: an agent definition was refused (400
	// invalid_definition); [APIError.Fields] says what is wrong with which
	// field.
	ErrInvalidDefinition = errors.New("invalid agent definition")
	// ErrReadOnly: the agent comes from the server's config file and
	// cannot be changed through the API (409 read_only).
	ErrReadOnly = errors.New("agent is read-only")
	// ErrVersionPublished: the version was published and is kept; only
	// drafts are deleted (409 version_published).
	ErrVersionPublished = errors.New("version was published")
)

var codeSentinels = map[string]error{
	wire.CodeInvalidRequest: ErrInvalidRequest,
	wire.CodeUnauthorized:   ErrUnauthorized,
	wire.CodeNotFound:       ErrNotFound,
	wire.CodeTooLarge:       ErrRequestTooLarge,
	wire.CodeBusy:           ErrConversationBusy,
	wire.CodeInterrupted:    ErrConversationInterrupted,
	wire.CodeAgentMismatch:  ErrAgentMismatch,
	wire.CodeOverCapacity:   ErrOverCapacity,
	wire.CodeShuttingDown:   ErrShuttingDown,

	wire.CodeTooManyBackgroundRuns: ErrTooManyBackgroundRuns,

	wire.CodeToolsetExists:     ErrToolsetExists,
	wire.CodeConflict:          ErrConflict,
	wire.CodeServerUnreachable: ErrServerUnreachable,
	wire.CodeAuthRequired:      ErrAuthRequired,

	wire.CodeURLNotAllowed:       ErrURLNotAllowed,
	wire.CodeRegistryUnavailable: ErrRegistryUnavailable,

	wire.CodeInvalidDefinition: ErrInvalidDefinition,
	wire.CodeReadOnly:          ErrReadOnly,
	wire.CodeVersionPublished:  ErrVersionPublished,

	wire.CodeRunLeased:        ErrConversationBusy,
	wire.CodeRunFinal:         ErrRunState,
	wire.CodeAwaitingDecision: ErrRunState,
	wire.CodeNotPending:       ErrRunState,
	wire.CodeShapeChanged:     ErrRunState,
}

// APIError is an HTTP error from the agent API: a request refused before a
// run started.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is the API's error code (wire.CodeBusy, ...), or empty when the
	// body was not the API's error body (for example a proxy's error page).
	Code string
	// Message says what happened, for people. Do not parse it.
	Message string
	// Retryable means the same request may succeed later.
	Retryable bool
	// RetryAfter is the response's Retry-After (set on 503s and 429s), or 0.
	RetryAfter time.Duration
	// RunID is the run the error is about, when there is one: on a busy
	// conversation (409), the run going on, to follow with [Client.Events].
	RunID string
	// Fields says what is wrong with which field of a refused agent
	// definition ([ErrInvalidDefinition]).
	Fields []wire.FieldError
}

func (e *APIError) Error() string {
	what := e.Code
	if what == "" {
		what = http.StatusText(e.Status)
	}
	return fmt.Sprintf("agent API: %d %s: %s", e.Status, what, e.Message)
}

// Is reports whether target is the sentinel of e's code.
func (e *APIError) Is(target error) bool {
	s, ok := codeSentinels[e.Code]
	return ok && s == target
}

// maxErrorBody is how much of an error response the client reads.
const maxErrorBody = 64 << 10

// apiError reads an error response and closes its body.
func (c *Client) apiError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &APIError{Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header)}
	var er wire.ErrorResponse
	if err := json.Unmarshal(body, &er); err == nil && er.Error != nil && er.Error.Code != "" {
		e.Code, e.Message, e.Retryable, e.RunID = er.Error.Code, er.Error.Message, er.Error.Retryable, er.Error.RunID
		e.Fields = er.Error.Fields
		return e
	}
	e.Message = snippet(body)
	return e
}

// retryAfter is the Retry-After of h, in seconds or as an HTTP date (taken
// relative to the response's Date, else to now); 0 if absent or past.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(min(n, math.MaxInt64/int64(time.Second))) * time.Second
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	now := time.Now()
	if d, err := http.ParseTime(h.Get("Date")); err == nil {
		now = d
	}
	return max(at.Sub(now), 0)
}

// snippet is the start of a body that is not the API's, for an error message.
func snippet(body []byte) string {
	const n = 200
	s := strings.ToValidUTF8(string(body), "�")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return strings.TrimSpace(s)
}
