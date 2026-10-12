package agentclient_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// FuzzSSE reads arbitrary bytes as an event stream. Whatever they are, the
// client never panics, and:
//
//   - it reads exactly the events an independent reading of the SSE standard
//     finds (referenceSSE), with the API's rules on top (expectedRead): it
//     never invents, drops or reorders one, and ends the way the rules say;
//   - with a small limit and the bytes followed by an endless line, it reads a
//     prefix of those events, never reads more than the limit and its buffer
//     past the input, and stops (it never waits for the end of the line).
func FuzzSSE(f *testing.F) {
	for _, name := range goldenStreams {
		f.Add(readGolden(f, name))
	}
	for _, seed := range []string{
		"data: {\"v\":1,\"type\":\"run_start\"}\r\n\r\ndata: {\"type\":\"run_done\"}\r\r",
		"\uFEFF: comment\ndata:{\"type\":\"x\"}\ndata\n\n",
		"data: {\"type\":\n data: \"run_error\"}\n\n",
		"event: run_done\nid: 1\n\ndata: null\n\n",
		"data: {\"type\":\"text_delta\",\"seq\":\"zero\"}\n\n",
		"\r\n\r\r\n\n",
		"data",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		want := expectedRead(referenceSSE(body))
		s, _ := streamOf(t, body)
		got := readAll(s)
		if diff := sameRead(got, want); diff != "" {
			t.Fatalf("%q: %s", body, diff)
		}

		const limit = 256
		s, bs := streamFrom(t, func() io.Reader { return io.MultiReader(bytes.NewReader(body), endless("x")) },
			agentclient.WithMaxResponseBytes(limit))
		cut := readAll(s)
		if len(cut.events) > len(want.events) || len(cut.events) > 0 && !jsonEqual(cut.events, want.events[:len(cut.events)]) {
			t.Fatalf("%q with a limit read %q, not a prefix of %q", body, cut.raw, want.raw)
		}
		if errors.Is(cut.err, agentclient.ErrStreamTruncated) {
			t.Fatalf("%q followed by an endless line ended with %v", body, cut.err)
		}
		if n := bs.read.Load(); n > int64(len(body)+limit+readSlack) {
			t.Fatalf("%q: read %d bytes, past the input, the limit and the buffer (%d)", body, n, len(body)+limit+readSlack)
		}
	})
}
