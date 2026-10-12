package agentclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Stream is a run's event stream. Read it with [Stream.Next] until io.EOF,
// and close it. Only Close may be called concurrently with the other methods
// (to cancel the run from another goroutine).
type Stream struct {
	runID, convID string
	body          io.ReadCloser
	dec           *decoder
	ctx           context.Context
	release       func() // cancels the request and closes the body, once
	closed        atomic.Bool
	raw           json.RawMessage
	err           error // what Next returns from now on
}

func newStream(resp *http.Response, cancel context.CancelFunc, maxBytes int) *Stream {
	s := &Stream{
		runID:  resp.Header.Get("Agentkit-Run-Id"),
		convID: resp.Header.Get("Agentkit-Conversation-Id"),
		body:   resp.Body,
		dec:    newDecoder(resp.Body, maxBytes),
		ctx:    resp.Request.Context(),
	}
	s.release = sync.OnceFunc(func() {
		cancel()
		_ = resp.Body.Close()
	})
	return s
}

// RunID is the run's ID, from the response headers (or, if a proxy dropped
// them, from run_start once [Stream.Next] has returned it).
func (s *Stream) RunID() string { return s.runID }

// ConversationID is the run's conversation, to continue it: from the
// response headers, or from run_start like [Stream.RunID].
func (s *Stream) ConversationID() string { return s.convID }

// Next returns the next event. After the run's last event (run_done or
// run_error) it returns io.EOF. A stream that ends or breaks before that
// gives [ErrStreamTruncated]; after [Stream.Close], [ErrClosed]; when the
// context of [Client.Stream] is done, its error. Events of a type the client
// does not know are returned like any other and do not end the stream.
func (s *Stream) Next() (wire.Event, error) {
	if s.err == nil && s.closed.Load() {
		s.err = ErrClosed
	}
	if s.err != nil {
		return wire.Event{}, s.err
	}
	data, err := s.dec.next()
	if err != nil {
		return wire.Event{}, s.fail(s.readError(err))
	}
	var e wire.Event
	if err := json.Unmarshal(data, &e); err != nil {
		return wire.Event{}, s.fail(fmt.Errorf("%w: an event's data is not an event: %w", ErrBadResponse, err))
	}
	if e.Type == "" {
		return wire.Event{}, s.fail(fmt.Errorf("%w: an event without a type", ErrBadResponse))
	}
	s.raw = data
	if e.Type == wire.TypeRunStart {
		if s.runID == "" {
			s.runID = e.RunID
		}
		if s.convID == "" {
			s.convID = e.ConversationID
		}
	}
	if e.Type == wire.TypeRunDone || e.Type == wire.TypeRunError {
		s.err = io.EOF
		s.release()
	}
	return e, nil
}

// RawData is the JSON of the event [Stream.Next] last returned, as the server
// sent it (unknown fields included). It is valid until the next call to Next.
func (s *Stream) RawData() json.RawMessage { return s.raw }

// Close closes the stream. Before the run's last event, this cancels the run
// on the server. It is safe to call more than once, and concurrently with
// [Stream.Next], which then returns [ErrClosed].
func (s *Stream) Close() error {
	s.closed.Store(true)
	s.release()
	return nil
}

func (s *Stream) fail(err error) error {
	s.err = err
	s.release()
	return err
}

// readError is what Next reports for an error reading the stream.
func (s *Stream) readError(err error) error {
	switch {
	case s.closed.Load():
		return ErrClosed
	case errors.Is(err, ErrResponseTooLarge):
		return err
	case s.ctx.Err() != nil:
		return context.Cause(s.ctx)
	case errors.Is(err, io.EOF):
		return ErrStreamTruncated
	default:
		return fmt.Errorf("%w: %w", ErrStreamTruncated, err)
	}
}

// readBufferSize is the decoder's read buffer: how far it may read past the
// limit before it knows a line is too long.
const readBufferSize = 32 << 10

// decoder reads Server-Sent Events as the HTML standard defines them
// ("Interpreting an event stream"): lines end with CRLF, LF or CR; a line
// starting with a colon is a comment; "field: value" drops one space after
// the colon; data lines are joined with LF; an empty line dispatches the
// event if it has data. Only the data matters here (the JSON carries the
// type and seq), so the event, id and retry fields are skipped. An event cut
// off by the end of the stream is not dispatched. No line and no event's data
// may exceed max bytes.
type decoder struct {
	r       *bufio.Reader
	max     int
	line    []byte
	skipLF  bool // the last line ended with CR: an LF right after belongs to it
	started bool // the byte order mark has been checked for
}

func newDecoder(r io.Reader, maxBytes int) *decoder {
	return &decoder{r: bufio.NewReaderSize(r, readBufferSize), max: maxBytes}
}

// next returns the data of the next event.
func (d *decoder) next() ([]byte, error) {
	if !d.started {
		d.started = true
		if b, _ := d.r.Peek(3); bytes.Equal(b, []byte("\xEF\xBB\xBF")) {
			_, _ = d.r.Discard(3)
		}
	}
	var data []byte
	for {
		line, err := d.readLine()
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			if len(data) == 0 {
				continue
			}
			return data[:len(data)-1], nil // without the last LF
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if found && len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if string(field) != "data" {
			continue
		}
		if len(data)+len(value)+1 > d.max {
			return nil, fmt.Errorf("%w: an event's data is larger than %d bytes", ErrResponseTooLarge, d.max)
		}
		data = append(append(data, value...), '\n')
	}
}

// readLine returns the next line without its end, valid until the next call.
// A line cut off by the end of the stream is not returned (io.EOF).
func (d *decoder) readLine() ([]byte, error) {
	d.line = d.line[:0]
	for {
		if d.r.Buffered() == 0 {
			if _, err := d.r.Peek(1); err != nil {
				return nil, err
			}
		}
		b, _ := d.r.Peek(d.r.Buffered())
		if d.skipLF {
			d.skipLF = false
			if b[0] == '\n' {
				_, _ = d.r.Discard(1)
				continue
			}
		}
		i := bytes.IndexAny(b, "\r\n")
		n := i
		if i < 0 {
			n = len(b)
		}
		if len(d.line)+n > d.max {
			return nil, fmt.Errorf("%w: an SSE line is longer than %d bytes", ErrResponseTooLarge, d.max)
		}
		d.line = append(d.line, b[:n]...)
		if i >= 0 {
			d.skipLF = b[i] == '\r'
			_, _ = d.r.Discard(i + 1)
			return d.line, nil
		}
		_, _ = d.r.Discard(n)
	}
}
