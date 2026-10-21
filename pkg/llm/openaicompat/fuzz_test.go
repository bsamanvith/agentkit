package openaicompat_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// FuzzStream feeds arbitrary event streams to the adapter: whatever a server
// (or a broken connection) sends, the adapter must not panic, and must end
// with either a final message consistent with its events, with valid tool
// input, or an error that maps to an llm sentinel.
func FuzzStream(f *testing.F) {
	files, _ := filepath.Glob("testdata/*/*.http")
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), nil)
		if err != nil {
			f.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		m, _ := streamModel(t, string(body))
		events, err := drain(m.Generate(t.Context(), userText("Hello.")))
		if err != nil {
			if !isAny(err, llm.ErrOverloaded, llm.ErrRateLimited, llm.ErrInvalidRequest, llm.ErrAuth, llm.ErrContextTooLong) {
				t.Fatalf("error %v matches no llm sentinel", err)
			}
			return
		}
		checkEvents(t, events)
	})
}

func isAny(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
