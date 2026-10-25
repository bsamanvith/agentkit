package gemini_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
)

func ExampleNew() {
	// A stand-in for the API that replays a recorded reply. Against
	// Google's, gemini.New() is enough: GEMINI_API_KEY supplies the key and
	// the model is gemini.DefaultModel.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile("testdata/conformance/text.http")
		resp, _ := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), r)
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		_, _ = io.Copy(w, resp.Body)
	}))
	defer srv.Close()

	model, err := gemini.New(
		gemini.WithModel("gemini-replay"),
		gemini.WithBaseURL(srv.URL+"/v1beta"),
		gemini.WithAPIKey("example-key"),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	req := llm.Request{
		System:   "You are terse.",
		Messages: []llm.Message{llm.User(llm.Text{Text: "Say hello."})},
		// Optional: settings of this adapter for this request.
		Provider: gemini.Options{ThinkingLevel: "low"},
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
