package anthropic_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

func ExampleNew() {
	// A stand-in for the API that replays a recorded reply. Against the real
	// API, anthropic.New() needs no options: ANTHROPIC_API_KEY (or another
	// credential the SDK finds) is enough.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile("testdata/conformance/text.http")
		resp, _ := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), r)
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		_, _ = io.Copy(w, resp.Body)
	}))
	defer srv.Close()

	model, err := anthropic.New(
		anthropic.WithBaseURL(srv.URL),
		anthropic.WithClientOptions(option.WithAPIKey("sk-ant-example")),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	req := llm.Request{
		System:   "You are terse.",
		Messages: []llm.Message{llm.User(llm.Text{Text: "Say hello."})},
		// Optional: Claude-specific settings for this request.
		Provider: anthropic.Options{Effort: anthropic.EffortLow},
	}
	for ev, err := range model.Generate(context.Background(), req) {
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		switch ev.Kind {
		case llm.EventTextDelta:
			fmt.Printf("%q\n", ev.Text)
		case llm.EventMessageDone:
			fmt.Println(ev.Response.Stop.Kind, ev.Response.Usage.OutputTokens)
		}
	}
	// Output:
	// "Hello from the model, "
	// "streamed in "
	// "several pieces."
	// end_turn 9
}
