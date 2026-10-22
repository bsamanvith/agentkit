package client

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"mime"
)

func isEventStream(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "text/event-stream"
}

// readEvents yields the data of each Server-Sent Event in r (its data lines
// joined by newlines), each at most limit bytes. Comments, event names and
// IDs are skipped. A stream that ends in the middle of an event drops it.
func readEvents(r io.Reader, limit int64) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		br := bufio.NewReaderSize(r, 64<<10)
		var data []byte
		has := false
		for {
			line, err := readLine(br, limit)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(nil, err)
				}
				return
			}
			switch {
			case len(line) == 0:
				if has && !yield(data, nil) {
					return
				}
				data, has = nil, false
			case line[0] == ':':
			default:
				field, value, _ := bytes.Cut(line, []byte(":"))
				if string(field) != "data" {
					continue
				}
				value = bytes.TrimPrefix(value, []byte(" "))
				if has {
					data = append(data, '\n')
				}
				data, has = append(data, value...), true
				if int64(len(data)) > limit {
					yield(nil, fmt.Errorf("%w: a stream event is larger than %d bytes", ErrProtocol, limit))
					return
				}
			}
		}
	}
}

// readLine reads one line without its end ("\n" or "\r\n"), at most limit
// bytes.
func readLine(br *bufio.Reader, limit int64) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if int64(len(line)) > limit+2 {
			return nil, fmt.Errorf("%w: a stream line is larger than %d bytes", ErrProtocol, limit)
		}
		switch {
		case err == nil:
			return bytes.TrimSuffix(line[:len(line)-1], []byte("\r")), nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return nil, io.EOF
		default:
			return nil, fmt.Errorf("%w: the stream broke off", ErrProtocol)
		}
	}
}
