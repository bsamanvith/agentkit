package llmhttp

import (
	"bufio"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// readAll reads every event of stream, then the error that ended it (nil at
// the end of the stream).
func readAll(r io.Reader) ([]string, error) {
	e := NewEvents(r)
	var got []string
	for {
		data, err := e.Next()
		if errors.Is(err, io.EOF) {
			return got, nil
		}
		if err != nil {
			return got, err
		}
		got = append(got, string(data))
	}
}

// TestEvents checks the event stream reader against the parts of the format
// servers use: line ends, comments, other fields, multi-line data, the space
// after the colon, and a stream cut off in an event.
func TestEvents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, stream string
		want         []string
	}{
		{"LF", "data: a\n\ndata: b\n\n", []string{"a", "b"}},
		{"CRLF", "data: a\r\n\r\ndata: b\r\n\r\n", []string{"a", "b"}},
		{"CR", "data: a\r\rdata: b\r\r", []string{"a", "b"}},
		{"comments and fields", ": keep-alive\n\nevent: message\nid: 1\nretry: 10\ndata: a\n\n: ping\n\n", []string{"a"}},
		{"multi-line data", "data: {\"a\":\ndata: 1}\n\n", []string{"{\"a\":\n1}"}},
		{"multi-line data, CRLF", "data: {\"a\":\r\ndata: 1}\r\n\r\n", []string{"{\"a\":\n1}"}},
		{"multi-line data, CR", "data: {\"a\":\rdata: 1}\r\r", []string{"{\"a\":\n1}"}},
		{"no space", "data:a\n\ndata:  b\n\n", []string{"a", " b"}},
		{"field without colon", "data\n\n", []string{""}},
		{"colon in value", "data: {\"t\":\"x:y\"}\n\n", []string{`{"t":"x:y"}`}},
		{"blank lines", "\n\n\ndata: a\n\n\n\n", []string{"a"}},
		{"cut off", "data: a\n\ndata: {\"par", []string{"a", `{"par`}},
		{"no blank line at end", "data: a\n", []string{"a"}},
		{"other field names", "datum: x\nDATA: y\n\n", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for name, r := range map[string]io.Reader{
				"whole":    strings.NewReader(c.stream),
				"bytewise": iotest.OneByteReader(strings.NewReader(c.stream)),
			} {
				got, err := readAll(r)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("%s: events = %q, want %q", name, got, c.want)
				}
			}
		})
	}
}

// TestEventsArriveLive checks that an event is returned as soon as its
// blank line arrives, whatever the line ends, while the stream stays open.
func TestEventsArriveLive(t *testing.T) {
	t.Parallel()
	for name, event := range map[string]string{"LF": "data: a\n\n", "CRLF": "data: a\r\n\r\n", "CR": "data: a\r\r"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pr, pw := io.Pipe()
			defer pw.Close()
			go func() { _, _ = io.WriteString(pw, event+"data: b") }()
			got := make(chan string, 1)
			go func() {
				data, _ := NewEvents(pr).Next()
				got <- string(data)
			}()
			select {
			case data := <-got:
				if data != "a" {
					t.Errorf("event = %q, want a", data)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the event did not come while the stream was open")
			}
		})
	}
}

// TestEventsTooLong checks that an endless line is an error, not a stream
// read into memory.
func TestEventsTooLong(t *testing.T) {
	t.Parallel()
	_, err := readAll(io.LimitReader(endless{}, MaxEventBytes+1024))
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("err = %v, want bufio.ErrTooLong", err)
	}
	many := strings.Repeat("data: "+strings.Repeat("x", 1<<20)+"\n", MaxEventBytes>>20+1)
	if _, err := readAll(strings.NewReader(many)); !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("many data lines: err = %v, want bufio.ErrTooLong", err)
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestEventsReadError checks that a failed read is returned, not taken for
// the end of the stream.
func TestEventsReadError(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection reset")
	_, err := readAll(io.MultiReader(strings.NewReader("data: a\n\n"), iotest.ErrReader(boom)))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}
