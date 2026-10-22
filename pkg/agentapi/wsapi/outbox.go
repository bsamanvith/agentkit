package wsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"mime"
	"net/http"
	"slices"
	"sync"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// outbox holds a connection's messages for its writer, at most limit of
// them. Putting a message never waits: when the outbox is full, the client
// is slow, and the outbox ends each stream open with a gap, shuts, and calls
// onSlow (which cancels the streams).
type outbox struct {
	mu       sync.Mutex
	limit    int
	msgs     [][]byte
	open     map[string]string // the streams open: the last event ID put on each
	shutDown bool              // nothing more is put; the writer closes once msgs is empty
	code     websocket.StatusCode
	reason   string
	wake     chan struct{}
	onSlow   func()
}

func newOutbox(limit int, onSlow func()) *outbox {
	return &outbox{limit: limit, open: map[string]string{}, wake: make(chan struct{}, 1), onSlow: onSlow}
}

// put adds a message that is not part of a stream.
func (o *outbox) put(m wire.WSMessage) bool { return o.add(m, func() {}) }

// opened adds the result that opens stream m.ID, which starts after the
// event of ID after ("" for the first): a gap before its first event names
// that event.
func (o *outbox) opened(m wire.WSMessage, after string) bool {
	return o.add(m, func() { o.open[m.ID] = after })
}

// event adds an event of stream m.Sub, of ID id.
func (o *outbox) event(m wire.WSMessage, id string) bool {
	return o.add(m, func() { o.open[m.Sub] = id })
}

// end adds the end of stream m.Sub.
func (o *outbox) end(m wire.WSMessage) bool {
	return o.add(m, func() { delete(o.open, m.Sub) })
}

func (o *outbox) add(m wire.WSMessage, added func()) bool {
	data := encode(m)
	o.mu.Lock()
	if o.shutDown {
		o.mu.Unlock()
		return false
	}
	if len(o.msgs) >= o.limit {
		for _, sub := range slices.Sorted(maps.Keys(o.open)) {
			o.msgs = append(o.msgs, encode(wire.WSMessage{Type: wire.WSGap, Sub: sub, After: o.open[sub]}))
		}
		clear(o.open)
		o.shutLocked(wire.WSCloseSlow, "client too slow")
		o.mu.Unlock()
		o.onSlow()
		return false
	}
	o.msgs = append(o.msgs, data)
	added()
	o.signalLocked()
	o.mu.Unlock()
	return true
}

// isShut reports whether the outbox takes no more messages.
func (o *outbox) isShut() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.shutDown
}

// shut takes no more messages: the writer sends those it has, then closes
// the connection with code. The first shut's code stands.
func (o *outbox) shut(code websocket.StatusCode, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.shutLocked(code, reason)
}

func (o *outbox) shutLocked(code websocket.StatusCode, reason string) {
	if o.shutDown {
		return
	}
	o.shutDown, o.code, o.reason = true, code, reason
	o.signalLocked()
}

func (o *outbox) signalLocked() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// next is the writer's next message; nil with the close code once the
// outbox is shut and empty; ok false when ctx is done.
func (o *outbox) next(ctx context.Context) (msg []byte, code websocket.StatusCode, reason string, ok bool) {
	for {
		o.mu.Lock()
		if len(o.msgs) > 0 {
			msg = o.msgs[0]
			o.msgs[0] = nil
			o.msgs = o.msgs[1:]
			o.mu.Unlock()
			return msg, 0, "", true
		}
		if o.shutDown {
			code, reason = o.code, o.reason
			o.mu.Unlock()
			return nil, code, reason, true
		}
		o.mu.Unlock()
		select {
		case <-o.wake:
		case <-ctx.Done():
			return nil, 0, "", false
		}
	}
}

// encode is m's JSON, its raw parts (an event, a body) byte for byte as the
// API wrote them: no HTML escaping.
func encode(m wire.WSMessage) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil { // cannot happen: raw parts are checked
		return []byte(`{"type":"result","status":500}`)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// errGone is what a response's writes return once the client is too slow
// or the connection closes: the API handler stops.
var errGone = errors.New("websocket: the client is gone or too slow")

// response is the [http.ResponseWriter] an API request of a command is
// served with. A response with status 200 and an event stream is sent as
// the result that opens the stream, then an event message per SSE event
// (its data, as the API wrote it), then an end. Any other is sent as one
// result with the status and the body.
type response struct {
	c     *conn
	id    string
	after string // a stream's Last-Event-ID

	mu     sync.Mutex
	header http.Header
	status int
	stream bool
	body   bytes.Buffer // not a stream: the body
	buf    []byte       // a stream: what is not a whole SSE event yet
	failed bool
}

func newResponse(c *conn, id, after string) *response {
	return &response{c: c, id: id, after: after, header: http.Header{}}
}

func (r *response) Header() http.Header { return r.header }

func (r *response) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeHeaderLocked(code)
}

func (r *response) writeHeaderLocked(code int) {
	if r.status != 0 || code < 200 { // already sent, or informational
		return
	}
	r.status = code
	mt, _, _ := mime.ParseMediaType(r.header.Get("Content-Type"))
	if code != http.StatusOK || mt != "text/event-stream" {
		return
	}
	r.stream = true
	if !r.c.out.opened(wire.WSMessage{
		Type: wire.WSResult, ID: r.id, Status: code,
		RunID: r.header.Get("Agentkit-Run-Id"), ConversationID: r.header.Get("Agentkit-Conversation-Id"),
	}, r.after) {
		r.failed = true
	}
}

func (r *response) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeHeaderLocked(http.StatusOK)
	if r.failed {
		return 0, errGone
	}
	if !r.stream {
		return r.body.Write(p)
	}
	r.buf = append(r.buf, p...)
	for {
		i := bytes.Index(r.buf, []byte("\n\n"))
		if i < 0 {
			return len(p), nil
		}
		frame := r.buf[:i]
		r.buf = r.buf[i+2:]
		id, data := parseEvent(frame)
		if data == nil {
			continue // a comment: the heartbeats stand for the keep-alives
		}
		if !r.c.out.event(wire.WSMessage{Type: wire.WSEvent, Sub: r.id, Event: data}, id) {
			r.failed = true
			return 0, errGone
		}
	}
}

// FlushError implements http.ResponseController's flush: messages go out
// as they are put.
func (r *response) FlushError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return errGone
	}
	return nil
}

// reset replaces a response not sent yet with status and body.
func (r *response) reset(status int, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.header = http.Header{"Content-Type": {"application/json"}}
	r.status = status
	r.body.Reset()
	r.body.Write(body)
}

// finish sends what is left once the handler has returned: the result, or
// the stream's end.
func (r *response) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeHeaderLocked(http.StatusOK)
	if r.stream {
		if !r.failed {
			r.c.out.end(wire.WSMessage{Type: wire.WSEnd, Sub: r.id})
		}
		return
	}
	m := wire.WSMessage{Type: wire.WSResult, ID: r.id, Status: r.status, RetryAfter: r.header.Get("Retry-After")}
	if body := bytes.TrimSpace(r.body.Bytes()); len(body) > 0 {
		if json.Valid(body) {
			m.Body = bytes.Clone(body)
		} else { // not the API's: sent as a JSON string
			m.Body, _ = json.Marshal(string(body))
		}
	}
	r.c.out.put(m)
}

// parseEvent reads one SSE event as the API writes them: its id and its
// data (nil for a comment).
func parseEvent(frame []byte) (id string, data []byte) {
	for line := range bytes.SplitSeq(frame, []byte("\n")) {
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "id":
			id = string(value)
		case "data":
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
	return id, data
}
