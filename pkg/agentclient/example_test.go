package agentclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// parrot is a model that repeats the user's last message.
type parrot struct{}

func (parrot) Generate(_ context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	text := "You said: " + req.Messages[len(req.Messages)-1].Text()
	return func(yield func(llm.Event, error) bool) {
		_ = yield(llm.Event{Kind: llm.EventTextDelta, Text: text}, nil) &&
			yield(llm.Event{Kind: llm.EventBlockDone, Block: llm.Text{Text: text}}, nil) &&
			yield(llm.Event{Kind: llm.EventMessageDone, Response: &llm.Response{
				Message: llm.Assistant(llm.Text{Text: text}), Stop: llm.StopReason{Kind: llm.StopEndTurn},
			}}, nil)
	}
}

// A streamed run, then a follow-up in the same conversation as JSON, against
// an in-process agent API.
func Example() {
	ctx := context.Background()
	tools, _ := tool.NewRegistry(nil)
	h, _ := harness.New(parrot{}, tools)
	srv, _ := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c, err := agentclient.New(ts.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := c.Stream(ctx, agentclient.RunRequest{Message: "hi"})
	if err != nil {
		fmt.Println(err) // an *APIError: errors.Is(err, agentclient.ErrOverCapacity), ...
		return
	}
	defer s.Close() // before the run's end, this cancels it
	for {
		e, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Println(err) // agentclient.ErrStreamTruncated, ...
			return
		}
		switch e.Type {
		case wire.TypeTextDelta:
			fmt.Print(e.Text)
		case wire.TypeRunDone:
			fmt.Println()
		case wire.TypeRunError:
			fmt.Println("failed:", e.Error.Code)
		}
	}

	run, err := c.Run(ctx, agentclient.RunRequest{Message: "again", ConversationID: s.ConversationID()})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(run.Status, run.Answer)
	// Output:
	// You said: hi
	// done You said: again
}
