package main

// End-to-end tests of agentkit ask and agentkit chat: the built binary as the
// client, against the built binary serving the agent API, against a local
// server replaying scripted Claude responses (testdata/e2e/cli-ask and
// cli-chat). The scripts were written by hand in the recorded fixtures' form;
// recording them again from the real API (see e2e_test.go) gives the same
// shape, and the assertions hold for any reasonable answer.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// startAgentServe starts agentkit serve with the agent API (agentConfigJSON)
// on the replay server, with e2eToken.
func startAgentServe(t *testing.T, api *httptest.Server) *serveProcess {
	t.Helper()
	p := startServe(t, agentConfigJSON, 20*time.Second,
		envComponents+"=agent,mcp-server", envAPIToken+"="+e2eToken,
		"ANTHROPIC_BASE_URL="+api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key")
	t.Cleanup(func() { // the token is never logged
		p.mu.Lock()
		defer p.mu.Unlock()
		if strings.Contains(p.logs.String(), e2eToken) {
			t.Error("the server's log holds the API token")
		}
	})
	return p
}

// cliCommand is agentkit with args as a client of p, with token (none if
// empty) and stdin.
func cliCommand(t *testing.T, c *replayAPI, api *httptest.Server, p *serveProcess, token, stdin string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd, stdout, stderr := c.command(t, api, args[0], args...)
	cmd.Env = append(cmd.Env, envServer+"="+p.url)
	if token != "" {
		cmd.Env = append(cmd.Env, envAPIToken+"="+token)
	}
	cmd.Stdin = strings.NewReader(stdin)
	return cmd, stdout, stderr
}

// checkCLIOutput checks what holds for every output of the client when it is
// not on a terminal: no escape sequence, and no token.
func checkCLIOutput(t *testing.T, out string, tokens ...string) {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Errorf("output not on a terminal has an escape sequence: %q", out)
	}
	for _, tok := range tokens {
		if tok != "" && strings.Contains(out, tok) {
			t.Errorf("output holds the token %q", tok)
		}
	}
}

// askRun runs agentkit ask to its end; it returns stdout, stderr and the
// exit code, after checkCLIOutput on both.
func askRun(t *testing.T, c *replayAPI, api *httptest.Server, p *serveProcess, token, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd, out, errOut := cliCommand(t, c, api, p, token, stdin, append([]string{"ask"}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stuck := time.AfterFunc(2*time.Minute, func() { // only a run that waits on the wrong model call gets here
		t.Errorf("agentkit ask %q still running after 2 minutes: killed", args)
		_ = cmd.Process.Kill()
	})
	err := cmd.Wait()
	stuck.Stop()
	t.Logf("agentkit ask %q: exit %d\nstdout:\n%s\nstderr:\n%s", args, cmd.ProcessState.ExitCode(), out, errOut)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	checkCLIOutput(t, out.String()+errOut.String(), e2eToken, token)
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

// createRun runs agentkit create and returns the ID it printed.
func createRun(t *testing.T, c *replayAPI, api *httptest.Server, p *serveProcess, token string, args ...string) string {
	t.Helper()
	cmd, out, errOut := cliCommand(t, c, api, p, token, "", append([]string{"create"}, args...)...)
	if err := cmd.Run(); err != nil {
		t.Fatalf("agentkit create %q: %v\nstderr:\n%s", args, err, errOut)
	}
	checkCLIOutput(t, out.String()+errOut.String(), e2eToken, token)
	id, ok := strings.CutSuffix(out.String(), "\n")
	if !ok || strings.Contains(id, "\n") {
		t.Fatalf("agentkit create printed %q, want one line", out)
	}
	return id
}

// parseJSONL decodes ask -json output: every line one event.
func parseJSONL(t *testing.T, out string) []wire.Event {
	t.Helper()
	var events []wire.Event
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var e wire.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.Type == "" {
			t.Fatalf("line %d is not an event: %q (%v)", i+1, line, err)
		}
		events = append(events, e)
	}
	return events
}

// TestE2EAsk runs agentkit ask against agentkit serve: -json events, a
// plain answer that continues the conversation (message on stdin), and each
// exit code from a real case: 0 run_done, 1 run_error, 2 HTTP and usage
// errors, 3 a busy conversation.
func TestE2EAsk(t *testing.T) {
	t.Parallel()
	c, api := newReplayAPI(t, "cli-ask")
	c.holdAt = 5
	p := startAgentServe(t, api)

	// create: an empty conversation, its ID alone on stdout.
	conv := createRun(t, c, api, p, e2eToken)
	if !regexp.MustCompile(`^conv_\w+$`).MatchString(conv) {
		t.Fatalf("agentkit create printed %q, want a conversation ID", conv)
	}

	// Plain, in the created conversation: the answer only (not the text
	// before the tool call); -v shows the conversation and the tool call on
	// stderr.
	stdout, stderr, code := askRun(t, c, api, p, e2eToken, "", "-v", "-conversation", conv,
		"Use demo-a__calculate to multiply 48271 by 9733, then tell me the result.")
	if code != exitDone || stdout != "48271 × 9733 = 469821643.\n" {
		t.Fatalf("ask: exit %d, stdout %q; want 0 and the final answer alone", code, stdout)
	}
	mustContain(t, stderr, "→ demo-a__calculate", `← {"result":469821643}`, "2 turns", "conversation "+conv)

	// -json, the message on stdin, continuing the conversation: one event
	// per line, ending with run_done.
	out, _, code := askRun(t, c, api, p, e2eToken, "Add one to that result. Answer with just the number.\n",
		"-json", "-conversation", conv, "-")
	if code != exitDone {
		t.Fatalf("ask -json: exit %d", code)
	}
	events := parseJSONL(t, out)
	checkAgentStream(t, events)
	if end := events[len(events)-1]; end.Type != wire.TypeRunDone || end.Answer != "469821644" || events[0].ConversationID != conv {
		t.Errorf("events %v ending with %+v; want run_done answering 469821644 in %s", eventTypes(events), end, conv)
	}
	reqs := c.all()
	if len(reqs) != 3 || !bytes.Contains(reqs[2].body, []byte("469821643")) || !bytes.Contains(reqs[2].body, []byte("Add one to that result")) {
		t.Errorf("the follow-up's model request does not carry the first run and the new message: %d requests", len(reqs))
	}
	checkConversation(t, reqs) // the follow-up extends the first run's request

	// A run that fails: exit 1, its code on stderr, nothing on stdout.
	stdout, stderr, code = askRun(t, c, api, p, e2eToken, "", "Say hi.")
	if code != exitRunFailed || stdout != "" || !strings.Contains(stderr, wire.CodeModelRejected) {
		t.Errorf("failed run: exit %d, stdout %q, stderr %q; want 1 and %s", code, stdout, stderr, wire.CodeModelRejected)
	}

	// Refused before a run, or no run asked for: exit 2, and no model call.
	for name, args := range map[string]struct {
		token string
		args  []string
	}{
		"wrong token":          {"wrong-token-canary-42", []string{"-v", "hi"}},
		"no token":             {"", []string{"hi"}},
		"unknown conversation": {e2eToken, []string{"-conversation", "conv_nosuchconversation", "hi"}},
		"unknown agent":        {e2eToken, []string{"-agent", "nosuchagent", "hi"}},
		"server down":          {e2eToken, []string{"-server", "http://127.0.0.1:1", "hi"}},
		"no message":           {e2eToken, nil},
		"two messages":         {e2eToken, []string{"hi", "there"}},
		"bad flag":             {e2eToken, []string{"-nosuchflag", "hi"}},
	} {
		if _, _, code := askRun(t, c, api, p, args.token, "", args.args...); code != exitClient {
			t.Errorf("%s: exit %d, want %d", name, code, exitClient)
		}
	}
	// chat with stdout on a pipe, though $TERM names a terminal: the prompt,
	// the tool lines of /history and an error, all without escape sequences.
	chat, chatOut, chatErr := cliCommand(t, c, api, p, e2eToken, "/history\n/bogus\n", "chat", "-conversation", conv)
	chat.Env = append(chat.Env, "TERM=xterm")
	if err := chat.Run(); err != nil {
		t.Errorf("chat on a pipe: %v\nstderr:\n%s", err, chatErr)
	}
	mustContain(t, chatOut.String(), "> ", "→ demo-a__calculate", "unknown command /bogus")
	checkCLIOutput(t, chatOut.String()+chatErr.String(), e2eToken)

	// create refused: exit 2, nothing on stdout. With -json, the
	// conversation.
	for name, args := range map[string][]string{
		"unknown agent": {"-agent", "nosuchagent"},
		"an argument":   {"hi"},
		"server down":   {"-server", "http://127.0.0.1:1"},
		"-conversation": {"-conversation", "x"}, // create has no use for it
	} {
		cmd, out, errOut := cliCommand(t, c, api, p, e2eToken, "", append([]string{"create"}, args...)...)
		if err := cmd.Run(); cmd.ProcessState.ExitCode() != exitClient || out.Len() != 0 {
			t.Errorf("create %s: %v, stdout %q, stderr %q; want exit 2 and no stdout", name, err, out, errOut)
		}
		checkCLIOutput(t, out.String()+errOut.String(), e2eToken)
	}
	create, cOut, cErr := cliCommand(t, c, api, p, "", "", "create")
	if err := create.Run(); create.ProcessState.ExitCode() != exitClient || !strings.Contains(cErr.String(), envAPIToken) {
		t.Errorf("create without a token: %v, stdout %q, stderr %q; want exit 2 and a hint", err, cOut, cErr)
	}
	var created wire.Conversation
	if out := createRun(t, c, api, p, e2eToken, "-json", "-agent", "default"); json.Unmarshal([]byte(out), &created) != nil || created.Agent != "default" || created.Status != wire.StatusIdle || len(created.Messages) != 0 {
		t.Errorf("create -json printed %q; want an empty conversation of default", out)
	}

	if n := len(c.all()); n != 4 {
		t.Errorf("%d model requests after the refused runs, chat and create, want 4", n)
	}

	// A busy conversation: exit 3 while another run of it goes on.
	held, heldOut, heldErr := cliCommand(t, c, api, p, e2eToken, "", "ask", "-conversation", conv, "Reply with just the word: ready")
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.held:
	case <-time.After(2 * time.Minute):
		_ = held.Process.Kill()
		t.Fatalf("the run never reached the model\nstderr:\n%s", heldErr)
	}
	_, stderr, code = askRun(t, c, api, p, e2eToken, "", "-conversation", conv, "hi")
	if code != exitRetryable || !strings.Contains(stderr, wire.CodeBusy) {
		t.Errorf("busy conversation: exit %d, stderr %q; want 3 and %s", code, stderr, wire.CodeBusy)
	}
	close(c.release)
	if err := held.Wait(); err != nil || heldOut.String() != "ready\n" {
		t.Errorf("the held run: %v, stdout %q, stderr %q", err, heldOut, heldErr)
	}
	checkCLIOutput(t, heldOut.String()+heldErr.String(), e2eToken)
}

// TestE2EChat drives agentkit chat on a pseudo-terminal, as a person would:
// two turns (the first with a tool call), /new, Ctrl-C while the model is
// answering (the run is cancelled on the server, chat returns to the
// prompt), Enter to resume the cancelled run, and Ctrl-D to quit.
func TestE2EChat(t *testing.T) {
	t.Parallel()
	c, api := newReplayAPI(t, "cli-chat")
	c.holdAt = 4
	p := startAgentServe(t, api)
	a := agentClient{t: t, url: p.url, token: e2eToken}

	cmd, _, _ := cliCommand(t, c, api, p, e2eToken, "", "chat")
	cmd.Env = append(cmd.Env, "TERM=xterm")
	term := startPTY(t, cmd)
	term.expect(t, "/help for commands")
	term.expect(t, "> ")

	term.typeIn(t, "Use demo-a__calculate to multiply 48271 by 9733, then tell me the result.\n")
	term.expect(t, "→ demo-a__calculate")
	term.expect(t, `← {"result":469821643}`)
	term.expect(t, "469821643.")
	term.expect(t, "2 turns")
	term.expect(t, "> ")
	term.typeIn(t, "Add one to that result.\n")
	term.expect(t, "469821644.")
	term.expect(t, "1 turn")
	term.expect(t, "> ")
	first := convID(t, term)

	term.typeIn(t, "/new\n")
	term.expect(t, "new conversation")
	term.expect(t, "> ")
	term.typeIn(t, "Reply with just the word: ready\n")
	select {
	case <-c.held:
	case <-time.After(2 * time.Minute):
		t.Fatal("the third message never reached the model")
	}
	term.typeIn(t, "\x03") // Ctrl-C
	term.expect(t, "cancelled before it answered")
	term.expect(t, "> ")
	second := convID(t, term)
	if second == first {
		t.Fatalf("/new kept conversation %s", first)
	}
	got, err := a.client().GetConversation(t.Context(), second)
	if err != nil || got.Status != agentapi.StatusInterrupted || len(got.Messages) != 1 {
		t.Errorf("after Ctrl-C: %+v (%v), want the new conversation interrupted, with only the message", got, err)
	}
	if got, err := a.client().GetConversation(t.Context(), first); err != nil || got.Status != agentapi.StatusIdle || len(got.Messages) != 6 {
		t.Errorf("first conversation: %+v (%v), want idle with both turns (6 messages)", got, err)
	}

	term.typeIn(t, "\n") // resume the cancelled run
	term.expect(t, "ready")
	term.expect(t, "1 turn")
	term.expect(t, "> ")
	if got, err := a.client().GetConversation(t.Context(), second); err != nil || got.Status != agentapi.StatusIdle || len(got.Messages) != 2 {
		t.Errorf("after resuming: %+v (%v), want idle with the message and the answer", got, err)
	}

	term.typeIn(t, "\x04") // Ctrl-D
	select {
	case <-term.done:
	case <-time.After(time.Minute):
		t.Fatal("chat did not exit on Ctrl-D")
	}
	if code := cmd.ProcessState.ExitCode(); code != exitDone {
		t.Errorf("exit %d after Ctrl-D, want 0", code)
	}
	out := term.transcript()
	if !strings.Contains(out, "\x1b[") {
		t.Error("no styling on a terminal")
	}
	if strings.Contains(out, e2eToken) {
		t.Error("the terminal shows the API token")
	}
	// Each conversation's requests extend each other (/new started the second).
	reqs := c.all()
	if len(reqs) != 5 {
		t.Fatalf("%d model requests, want 5", len(reqs))
	}
	checkConversation(t, reqs[:3])
	checkConversation(t, reqs[3:])
}

// convID asks chat for the conversation's ID (/conv).
func convID(t *testing.T, term *ptyProcess) string {
	t.Helper()
	term.typeIn(t, "/conv\n")
	term.expect(t, "conv_")
	id := "conv_" + strings.TrimSpace(term.expect(t, "\n"))
	term.expect(t, "> ")
	return id
}
