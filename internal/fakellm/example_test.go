package fakellm_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/bsamanvith/agentkit/internal/fakellm"
)

func Example() {
	script, err := fakellm.ParseScript("default", strings.NewReader(`
{"match": "hello", "response": {"text": "Hi!"}}
{"response": {"text": "Say hello."}}
`), nil)
	if err != nil {
		panic(err)
	}
	h, err := fakellm.New(map[string]*fakellm.Script{"default": script})
	if err != nil {
		panic(err)
	}
	srv := httptest.NewServer(h) // cmd/fakellm listens on its own port
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
		`{"model": "claude-test", "max_tokens": 10, "messages": [{"role": "user", "content": "hello there"}]}`))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Print(resp.Status, " ", string(body))
	// Output:
	// 200 OK {"id":"msg_fake_64e95a1abd348f2419f71ee4","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"Hi!"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}
}
