package openaicompat

import (
	"bufio"
	"bytes"
	"io"
)

// maxEventBytes bounds one event of the stream (and one line of it). A chunk
// is a few hundred bytes; a server that sends more without a line break is
// not sending a reply.
const maxEventBytes = 8 << 20

// events reads a text/event-stream (the HTML server-sent events format) as
// far as Chat Completions uses it: the data of each event, its data lines
// joined with "\n". Comments (keep-alives), event names, IDs and retry
// fields are skipped, and so are events without data.
type events struct {
	sc   *bufio.Scanner
	data []byte
}

func newEvents(r io.Reader) *events {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxEventBytes)
	sc.Split(scanLines)
	return &events{sc: sc}
}

// next returns the data of the next event. At the end of the stream it
// returns io.EOF, after dispatching an event the stream ended in the middle
// of, as a cut-off chunk is better reported as garbled than not at all. The
// data is valid until the next call.
func (e *events) next() ([]byte, error) {
	e.data = e.data[:0]
	hasData := false
	for e.sc.Scan() {
		line := e.sc.Bytes()
		if len(line) == 0 {
			if hasData {
				return e.data, nil
			}
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		if string(field) != "data" {
			continue // a comment (empty field), event, id, retry or unknown field
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		if hasData {
			e.data = append(e.data, '\n')
		}
		e.data = append(e.data, value...)
		hasData = true
		if len(e.data) > maxEventBytes {
			return nil, bufio.ErrTooLong
		}
	}
	if err := e.sc.Err(); err != nil {
		return nil, err
	}
	if hasData {
		return e.data, nil
	}
	return nil, io.EOF
}

// scanLines splits at "\n", "\r\n" or a lone "\r", the line ends of the
// event stream format, and drops them.
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	i := bytes.IndexAny(data, "\r\n")
	switch {
	case i < 0:
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	case data[i] == '\n':
		return i + 1, data[:i], nil
	case i+1 < len(data):
		if data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
		return i + 1, data[:i], nil
	case atEOF:
		return i + 1, data[:i], nil
	default:
		return 0, nil, nil // a "\r" last: wait to see whether "\n" follows
	}
}
