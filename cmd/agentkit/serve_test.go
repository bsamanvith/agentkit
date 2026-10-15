package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentClash gives the agent two toolsets with the same tools and no prefix.
const agentClash = `{"mcp":{"toolsets":[{"name":"a","type":"demo"},{"name":"b","type":"demo"}]},"agent":{"toolsets":["a","b"]}}`

// TestServeRejectsBadSetup: a mistake in the environment or the config fails
// startup, before anything listens, with a message naming the mistake.
func TestServeRejectsBadSetup(t *testing.T) {
	t.Parallel()
	const good = `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]}}`
	for name, tc := range map[string]struct {
		env    map[string]string
		config string // file contents; "" for no -config
		args   []string
		code   int
		says   string // part of the error message
	}{
		"unknown component":      {env: map[string]string{envComponents: "mcp-server,tools"}, config: good, code: 1, says: `unknown component "tools"`},
		"component listed twice": {env: map[string]string{envComponents: "mcp-server, mcp-server"}, config: good, code: 1, says: "mcp-server"},
		"no config file":         {code: 1, says: envConfig},
		"missing config file":    {args: []string{"-config", "/nonexistent/agentkit.json"}, code: 1, says: "/nonexistent/agentkit.json"},
		"malformed JSON":         {config: `{"mcp":`, code: 1, says: "config"},
		"unknown config field":   {config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}],"call_timout":"1s"}}`, code: 1, says: "call_timout"},
		"trailing data":          {config: good + `{}`, code: 1, says: "trailing"},
		"no toolsets":            {config: `{"mcp":{}}`, code: 1, says: "toolsets"},
		"unknown toolset type":   {config: `{"mcp":{"toolsets":[{"name":"crm","type":"salesforce"}]}}`, code: 1, says: "salesforce"},
		"unknown toolset field":  {config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo","inclde":["echo"]}]}}`, code: 1, says: "inclde"},
		"required toolset fails": {config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo","settings":{"utc_offset_minutes":9999}}]}}`, code: 1, says: "utc_offset_minutes"},
		"bad duration":           {config: `{"mcp":{"call_timeout":"soon","toolsets":[{"name":"demo","type":"demo"}]}}`, code: 1, says: "soon"},
		"negative limit":         {config: `{"mcp":{"max_in_flight":-1,"toolsets":[{"name":"demo","type":"demo"}]}}`, code: 1, says: "negative"},
		"bad base path":          {config: `{"mcp":{"base_path":"mcp","toolsets":[{"name":"demo","type":"demo"}]}}`, code: 1, says: "base path"},
		"bad log level":          {env: map[string]string{envLogLevel: "loud"}, config: good, code: 1, says: envLogLevel},
		"bad shutdown timeout":   {env: map[string]string{envShutdownTimeout: "0s"}, config: good, code: 1, says: envShutdownTimeout},
		"short run lease":        {env: map[string]string{envRunLease: "10ms"}, config: good, code: 1, says: envRunLease},
		"unusable address":       {env: map[string]string{envAddr: "127.0.0.1:not-a-port"}, config: good, code: 1, says: "not-a-port"},
		"positional argument":    {config: good, args: []string{"extra"}, code: 2},
		// The agent component.
		"agent: no token off loopback": {env: map[string]string{envComponents: "agent", envAddr: "0.0.0.0:0"}, config: good, code: 1, says: envAPIToken},
		"agent: no token on all ifs":   {env: map[string]string{envComponents: "agent", envAddr: ":0"}, config: good, code: 1, says: envAPIToken},
		"agent: short token":           {env: map[string]string{envComponents: "agent", envAPIToken: "short"}, config: good, code: 1, says: "token"},
		"agent: unknown toolset":       {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agent":{"toolsets":["dmeo"]}}`, code: 1, says: "dmeo"},
		"agent: tool name clash":       {env: map[string]string{envComponents: "agent,mcp-server"}, config: agentClash, code: 1, says: "clash"},
		"agent: unknown provider":      {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"provider":"openai"}}}`, code: 1, says: "openai"},
		"agent: unknown field":         {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"max_turn":3}}`, code: 1, says: "max_turn"},
		"agent: no concurrent runs":    {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"max_concurrent_runs":0}}`, code: 1, says: "concurrent"},
		"agent: bad effort":            {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"effort":"extreme"}}}`, code: 1, says: "agent.model"},
		"agent: unknown store":         {env: map[string]string{envComponents: "agent", envStore: "mysql://u:hunter2@db/x"}, config: good, code: 1, says: envStore + ": unknown store"},
		"agent: sqlite without a path": {env: map[string]string{envComponents: "agent", envStore: "sqlite:"}, config: good, code: 1, says: "file path"},
		"agent: bad store migrate":     {env: map[string]string{envComponents: "agent", envStore: "postgres://localhost:1/x", envStoreMigrate: "maybe"}, config: good, code: 1, says: envStoreMigrate},
		"agent: unknown bus":           {env: map[string]string{envComponents: "agent", envBus: "nats://u:hunter2@bus"}, config: good, code: 1, says: envBus + ": unknown bus"},
		"agent: unreachable bus":       {env: map[string]string{envComponents: "agent", envBus: "redis://u:hunter2@127.0.0.1:1/0"}, config: good, code: 1, says: envBus + ": redis bus: ping"},
		// Several agents.
		"agents: and agent":             {env: map[string]string{envComponents: "agent"}, config: `{"agent":{},"agents":[{"name":"a"}]}`, code: 1, says: `"agent" is the one-agent form`},
		"agents: agent_api and agent":   {env: map[string]string{envComponents: "agent"}, config: `{"agent":{},"agent_api":{}}`, code: 1, says: `"agent" is the one-agent form`},
		"agents: empty":                 {env: map[string]string{envComponents: "agent"}, config: `{"agents":[]}`, code: 1, says: "agents is empty"},
		"agents: no name":               {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{}]}`, code: 1, says: "agents[0].name"},
		"agents: bad name":              {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a b"}]}`, code: 1, says: "agents[0].name"},
		"agents: duplicate name":        {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"},{"name":"a"}]}`, code: 1, says: "agents[1].name"},
		"agents: unknown field":         {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","max_turn":3}]}`, code: 1, says: "max_turn"},
		"agents: server limit":          {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","max_runs":3}]}`, code: 1, says: "max_runs"},
		"agents: unknown toolset":       {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agents":[{"name":"a","toolsets":["dmeo"]}]}`, code: 1, says: "agents.a.toolsets"},
		"agents: unknown provider":      {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"},{"name":"b","model":{"provider":"openai"}}]}`, code: 1, says: "agents.b.model.provider"},
		"agents: bad effort":            {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","model":{"effort":"extreme"}}]}`, code: 1, says: "agents.a.model"},
		"agents: tool name clash":       {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"a","type":"demo"},{"name":"b","type":"demo"}]},"agents":[{"name":"x","toolsets":["a","b"]}]}`, code: 1, says: "agents.x.toolsets"},
		"agent_api: unknown field":      {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"}],"agent_api":{"system":"x"}}`, code: 1, says: "system"},
		"agent_api: no concurrent runs": {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"}],"agent_api":{"max_concurrent_runs":0}}`, code: 1, says: "concurrent"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{envAddr: "127.0.0.1:0"}
			for k, v := range tc.env {
				env[k] = v
			}
			args := append([]string{"serve"}, tc.args...)
			if tc.config != "" {
				path := filepath.Join(t.TempDir(), "agentkit.json")
				if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
				env[envConfig] = path
			}
			// A broken check would start serving; the deadline then ends the
			// test with exit 0 instead of hanging it.
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			c := cli{stdout: &stdout, stderr: &stderr, getenv: func(k string) string { return env[k] }}
			code := c.main(ctx, args)
			if code != tc.code || !strings.Contains(stderr.String(), tc.says) {
				t.Errorf("agentkit %q = exit %d, stderr:\n%s\nwant exit %d and a message containing %q", args, code, &stderr, tc.code, tc.says)
			}
			if strings.Contains(stderr.String(), "hunter2") {
				t.Errorf("stderr echoes the store's password:\n%s", &stderr)
			}
		})
	}
}
