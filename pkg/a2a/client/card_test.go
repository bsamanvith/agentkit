package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/a2a/a2atest"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestCardValidation (criterion 3): malformed cards are refused with
// ErrInvalidCard; the valid card of our own server is accepted.
func TestCardValidation(t *testing.T) {
	t.Parallel()
	const cardURL = "https://agents.example.com/.well-known/agent-card.json"
	const at = "https://agents.example.com/a2a"
	for name, edit := range map[string]func(*a2a.AgentCard){
		"no name":             func(c *a2a.AgentCard) { c.Name = " " },
		"long name":           func(c *a2a.AgentCard) { c.Name = strings.Repeat("n", 257) },
		"no description":      func(c *a2a.AgentCard) { c.Description = "" },
		"no version":          func(c *a2a.AgentCard) { c.Version = "" },
		"no interfaces":       func(c *a2a.AgentCard) { c.SupportedInterfaces = nil },
		"interface no url":    func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "" },
		"interface bad url":   func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "ftp://agents.example.com/a2a" },
		"relative url":        func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "/a2a" },
		"user in url":         func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "https://u:p@agents.example.com/a2a" },
		"no binding":          func(c *a2a.AgentCard) { c.SupportedInterfaces[0].ProtocolBinding = "" },
		"no version on iface": func(c *a2a.AgentCard) { c.SupportedInterfaces[0].ProtocolVersion = "" },
		"only grpc":           func(c *a2a.AgentCard) { c.SupportedInterfaces[0].ProtocolBinding = "GRPC" },
		"only version 0.3":    func(c *a2a.AgentCard) { c.SupportedInterfaces[0].ProtocolVersion = "0.3" },
		"only version 2":      func(c *a2a.AgentCard) { c.SupportedInterfaces[0].ProtocolVersion = "2.0" },
		"other origin":        func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "https://elsewhere.example.com/a2a" },
		"other port":          func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "https://agents.example.com:8443/a2a" },
		"downgrade to http":   func(c *a2a.AgentCard) { c.SupportedInterfaces[0].URL = "http://agents.example.com/a2a" },
	} {
		c := cardAt(at)
		edit(&c)
		if err := client.ValidateCard(&c, cardURL); !errors.Is(err, client.ErrInvalidCard) {
			t.Errorf("%s: %v, want ErrInvalidCard", name, err)
		}
	}
	good := cardAt(at)
	good.SupportedInterfaces = append([]a2a.AgentInterface{{URL: "https://agents.example.com/grpc", ProtocolBinding: "GRPC", ProtocolVersion: "1.0"}}, good.SupportedInterfaces...)
	if err := client.ValidateCard(&good, cardURL); err != nil {
		t.Errorf("a card with a gRPC interface besides JSON-RPC: %v", err)
	}
	if err := client.ValidateCard(nil, cardURL); !errors.Is(err, client.ErrInvalidCard) {
		t.Errorf("nil card: %v", err)
	}

	// Bodies that are not cards, over HTTP.
	for name, body := range map[string]string{
		"not json":       `<html>hello</html>`,
		"wrong types":    `{"name": 7}`,
		"empty":          `{}`,
		"array":          `[]`,
		"interfaces obj": `{"name": "n", "description": "d", "version": "1", "supportedInterfaces": {"url": "x"}}`,
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		}))
		_, err := client.FetchCard(t.Context(), ts.URL+"/card", client.Config{HTTPClient: ts.Client()})
		ts.Close()
		if !errors.Is(err, client.ErrInvalidCard) && !errors.Is(err, client.ErrProtocol) {
			t.Errorf("%s: %v, want ErrInvalidCard", name, err)
		}
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name": "n", "description": %q}`, strings.Repeat("x", client.MaxCardBytes))
	}))
	t.Cleanup(big.Close)
	if _, err := client.FetchCard(t.Context(), big.URL+"/card", client.Config{HTTPClient: big.Client()}); !errors.Is(err, client.ErrProtocol) {
		t.Errorf("a card over the limit: %v, want ErrProtocol", err)
	}

	s := a2atest.NewServer(t)
	if _, err := client.FetchCard(t.Context(), s.CardURL(), client.Config{HTTPClient: s.Client(), Header: http.Header{"Authorization": {"Bearer " + a2atest.Key}}}); err != nil {
		t.Errorf("our server's card: %v", err)
	}
	var se *client.StatusError
	if _, err := client.FetchCard(t.Context(), s.CardURL(), client.Config{HTTPClient: s.Client()}); !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Errorf("our server's card without the key: %v, want HTTP 401", err)
	}
}

// attacker is a server that must never see the key.
type attacker struct {
	*httptest.Server
	hits atomic.Int64
}

func newAttacker(t *testing.T) *attacker {
	a := &attacker{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits.Add(1)
		if strings.Contains(fmt.Sprint(r.Header, r.URL), a2atest.Key) {
			t.Errorf("the attacker got the key: %s %s", r.Method, r.URL)
		}
	}))
	t.Cleanup(a.Close)
	return a
}

// TestCredentialNeverLeaks (criterion 3, the canary): the key goes to the
// card's origin only, and never comes back in an output, an error or a log
// line, even when the remote agent echoes it.
func TestCredentialNeverLeaks(t *testing.T) {
	t.Parallel()
	settings := func(cardURL string) string {
		return `{"agents": [{"name": "remote", "card_url": "` + cardURL + `", "token_env": "` + keyEnv + `"}]}`
	}

	t.Run("interface on another origin", func(t *testing.T) {
		t.Parallel()
		evil := newAttacker(t)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cardAt(evil.URL + "/a2a"))
		}))
		t.Cleanup(ts.Close)
		_, err := buildTools(t, settings(ts.URL+"/card"), buildOpts{hc: ts.Client()})
		if !errors.Is(err, client.ErrInvalidCard) {
			t.Errorf("Build: %v, want ErrInvalidCard", err)
		}
		if n := evil.hits.Load(); n != 0 {
			t.Errorf("the other origin got %d requests", n)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		t.Parallel()
		evil := newAttacker(t)
		ts := httptest.NewServer(http.RedirectHandler(evil.URL+"/card", http.StatusFound))
		t.Cleanup(ts.Close)
		var se *client.StatusError
		if _, err := buildTools(t, settings(ts.URL+"/card"), buildOpts{hc: ts.Client()}); !errors.As(err, &se) || se.StatusCode != http.StatusFound {
			t.Errorf("Build: %v, want the redirect not followed", err)
		}
		if n := evil.hits.Load(); n != 0 {
			t.Errorf("the redirect's target got %d requests", n)
		}
	})

	t.Run("echoed", func(t *testing.T) {
		t.Parallel()
		// A remote agent that puts the Authorization header it got into
		// everything it answers: its card, task, artifacts and errors.
		var url string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			echo := r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				c := cardAt(url + "/a2a")
				c.Description = "I got " + echo
				_ = json.NewEncoder(w).Encode(c)
				return
			}
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
				Params struct {
					Message struct {
						Parts []a2a.Part `json:"parts"`
					} `json:"message"`
				} `json:"params"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if *req.Params.Message.Parts[0].Text == "fail" {
				fmt.Fprintf(w, `{"jsonrpc": "2.0", "id": %d, "error": {"code": -32603, "message": "bad key %s"}}`, req.ID, echo)
				return
			}
			task := a2a.Task{
				ID: "t1", Status: a2a.TaskStatus{State: a2a.StateFailed, Message: &a2a.Message{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.TextPart("your key " + echo)}}},
				Artifacts: []a2a.Artifact{{ArtifactID: "a", Parts: []a2a.Part{a2a.TextPart("token=" + strings.TrimPrefix(echo, "Bearer "))}}},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"task": task}})
		}))
		t.Cleanup(ts.Close)
		url = ts.URL
		var log logs
		tools, err := buildTools(t, settings(ts.URL+"/card"), buildOpts{hc: ts.Client(), log: log.logger()})
		if err != nil {
			t.Fatal(err)
		}
		if d := tools[0].Definition().Description; strings.Contains(d, a2atest.Key) {
			t.Errorf("the key is in the description: %q", d)
		}
		out, err := ask(t.Context(), t, tools[0], `{"message": "hello"}`)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "failed" || !strings.Contains(out.Answer, "[redacted]") || !strings.Contains(out.Message, "[redacted]") {
			t.Errorf("output %+v, want the echoed key redacted", out)
		}
		_, err = ask(t.Context(), t, tools[0], `{"message": "fail"}`)
		if !errors.Is(err, tool.ErrToolFailed) || strings.Contains(err.Error(), a2atest.Key) {
			t.Errorf("error %v: want a failed call without the key", err)
		}
		if strings.Contains(log.String(), a2atest.Key) {
			t.Errorf("the key is in the logs: %s", log.String())
		}
	})

	t.Run("our server", func(t *testing.T) {
		t.Parallel()
		s := a2atest.NewServer(t)
		var log logs
		tl := helperTool(t, s, "", buildOpts{log: log.logger()})
		if _, err := ask(t.Context(), t, tl, `{"message": "hello"}`); err != nil {
			t.Fatal(err)
		}
		checkKeyOnlyInAuthorization(t, s)
		if strings.Contains(log.String(), a2atest.Key) {
			t.Errorf("the key is in the logs")
		}
	})
}

// FuzzValidateCard: any card body is a card or an error, never a panic.
func FuzzValidateCard(f *testing.F) {
	b, _ := json.Marshal(cardAt("https://agents.example.com/a2a"))
	f.Add(b)
	f.Add([]byte(`{"supportedInterfaces": [{"url": "https://agents.example.com", "protocolBinding": "HTTP+JSON", "protocolVersion": "1"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var c a2a.AgentCard
		if json.Unmarshal(data, &c) != nil {
			return
		}
		_ = client.ValidateCard(&c, "https://agents.example.com/.well-known/agent-card.json")
	})
}
