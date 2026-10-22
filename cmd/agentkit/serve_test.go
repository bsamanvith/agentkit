package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// agentClash gives the agent two toolsets with the same tools and no prefix.
const agentClash = `{"mcp":{"toolsets":[{"name":"a","type":"demo"},{"name":"b","type":"demo"}]},"agent":{"toolsets":["a","b"]}}`

// TestServeRejectsBadSetup: a mistake in the environment or the config fails
// startup, before anything listens, with a message naming the mistake.
// authHash is the hash of a key, for configs.
var authHash = auth.HashKey("hunter2-hunter2-hunter2")

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
		"bad credentials key":    {env: map[string]string{envCredentialsKey: "k1:aHVudGVyMg=="}, config: good, code: 1, says: envCredentialsKey + ": credentials: bad key ring: key \"k1\" is 7 bytes"},
		// The agent component.
		"agent: no token off loopback": {env: map[string]string{envComponents: "agent", envAddr: "0.0.0.0:0"}, config: good, code: 1, says: envAPIToken},
		"agent: no token on all ifs":   {env: map[string]string{envComponents: "agent", envAddr: ":0"}, config: good, code: 1, says: envAPIToken},
		"agent: short token":           {env: map[string]string{envComponents: "agent", envAPIToken: "short"}, config: good, code: 1, says: "token"},
		"agent: unknown toolset":       {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agent":{"toolsets":["dmeo"]}}`, code: 1, says: "dmeo"},
		"agent: tool name clash":       {env: map[string]string{envComponents: "agent,mcp-server"}, config: agentClash, code: 1, says: "clash"},
		"agent: unknown provider":      {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"provider":"gemini"}}}`, code: 1, says: "gemini"},
		"agent: openai without id":     {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"provider":"openai","base_url":"http://localhost:11434/v1"}}}`, code: 1, says: "agent.model.id"},
		"agent: openai native memory":  {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"provider":"openai","id":"m"},"memory":{"store":{"backend":"memory"},"native_tool":true}}}`, code: 1, says: "native_tool"},
		"agent: base_url on anthropic": {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"base_url":"http://localhost:11434/v1"}}}`, code: 1, says: "base_url"},
		"agent: unknown field":         {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"max_turn":3}}`, code: 1, says: "max_turn"},
		"agent: no concurrent runs":    {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"max_concurrent_runs":0}}`, code: 1, says: "concurrent"},
		"agent: bad effort":            {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"effort":"extreme"}}}`, code: 1, says: "agent.model"},
		"agent: unknown store":         {env: map[string]string{envComponents: "agent", envStore: "mysql://u:hunter2@db/x"}, config: good, code: 1, says: envStore + ": unknown store"},
		"agent: sqlite without a path": {env: map[string]string{envComponents: "agent", envStore: "sqlite:"}, config: good, code: 1, says: "file path"},
		"agent: bad store migrate":     {env: map[string]string{envComponents: "agent", envStore: "postgres://localhost:1/x", envStoreMigrate: "maybe"}, config: good, code: 1, says: envStoreMigrate},
		"agent: unknown bus":           {env: map[string]string{envComponents: "agent", envBus: "nats://u:hunter2@bus"}, config: good, code: 1, says: envBus + ": unknown bus"},
		"agent: unreachable bus":       {env: map[string]string{envComponents: "agent", envBus: "redis://u:hunter2@127.0.0.1:1/0"}, config: good, code: 1, says: envBus + ": redis bus: ping"},
		// The a2a component (T067).
		"a2a: without the agent API": {env: map[string]string{envComponents: "a2a"}, config: good, code: 1, says: a2aSays("a2a: needs the agent component")},
		"a2a: bad public URL":        {env: map[string]string{envComponents: "agent,a2a", envA2AURL: "agents.example.com/a2a"}, config: good, code: 1, says: a2aSays(envA2AURL)},
		// Several agents.
		"agents: and agent":               {env: map[string]string{envComponents: "agent"}, config: `{"agent":{},"agents":[{"name":"a"}]}`, code: 1, says: `"agent" is the one-agent form`},
		"agents: agent_api and agent":     {env: map[string]string{envComponents: "agent"}, config: `{"agent":{},"agent_api":{}}`, code: 1, says: `"agent" is the one-agent form`},
		"agents: empty":                   {env: map[string]string{envComponents: "agent"}, config: `{"agents":[]}`, code: 1, says: "agents is empty"},
		"agents: no name":                 {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{}]}`, code: 1, says: "agents[0].name"},
		"agents: bad name":                {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a b"}]}`, code: 1, says: "agents[0].name"},
		"agents: duplicate name":          {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"},{"name":"a"}]}`, code: 1, says: "agents[1].name"},
		"agents: unknown field":           {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","max_turn":3}]}`, code: 1, says: "max_turn"},
		"agents: compact past the window": {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","model":{"compact_at":1.5}}]}`, code: 1, says: "agents.a.model.compact_at 1.5"},
		"agents: negative window":         {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","model":{"context_window":-1}}]}`, code: 1, says: "negative"},
		"agents: server limit":            {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","max_runs":3}]}`, code: 1, says: "max_runs"},
		"agents: unknown toolset":         {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agents":[{"name":"a","toolsets":["dmeo"]}]}`, code: 1, says: "agents.a.toolsets"},
		"runtime: unknown field":          {env: map[string]string{envComponents: "agent"}, config: `{"runtime_toolsets":{"alow_hosts":[]}}`, code: 1, says: "alow_hosts"},
		"runtime: off but named":          {env: map[string]string{envComponents: "agent"}, config: `{"runtime_toolsets":{"enabled":false},"agents":[{"name":"a","runtime_toolsets":["linear"]}]}`, code: 1, says: "agents.a.runtime_toolsets"},
		"runtime: a config toolset":       {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agents":[{"name":"a","runtime_toolsets":["demo"]}]}`, code: 1, says: "demo"},
		"runtime: on the remote runner":   {env: map[string]string{envComponents: "agent", envRunnerURL: "http://runner:8080"}, config: `{"agents":[{"name":"a","runtime_toolsets":["linear"]}]}`, code: 1, says: "cannot use toolsets added at run time"},
		"runtime: bad allowed host":       {env: map[string]string{envComponents: "agent"}, config: `{"runtime_toolsets":{"allow_hosts":["http://x"]}}`, code: 1, says: "allow_hosts"},
		"runtime: negative limit":         {env: map[string]string{envComponents: "agent"}, config: `{"runtime_toolsets":{"max_toolsets":-1}}`, code: 1, says: "negative"},
		"agents: unknown provider":        {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"},{"name":"b","model":{"provider":"gemini"}}]}`, code: 1, says: "agents.b.model.provider"},
		"agents: bad effort":              {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","model":{"effort":"extreme"}}]}`, code: 1, says: "agents.a.model"},
		"agents: tool name clash":         {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"a","type":"demo"},{"name":"b","type":"demo"}]},"agents":[{"name":"x","toolsets":["a","b"]}]}`, code: 1, says: "agents.x.toolsets"},
		"agent_api: unknown field":        {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"}],"agent_api":{"system":"x"}}`, code: 1, says: "system"},
		"agent_api: no concurrent runs":   {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"}],"agent_api":{"max_concurrent_runs":0}}`, code: 1, says: "concurrent"},
		// The split deployment (T102).
		"runner: and its URL":           {env: map[string]string{envComponents: "agent,runner", envRunnerURL: "http://runner:8080"}, config: good, code: 1, says: envRunnerURL},
		"toolsets: and their URL":       {env: map[string]string{envComponents: "toolsets", envToolsetsURL: "http://tools:8080"}, config: good, code: 1, says: envToolsetsURL},
		"toolsets: no config file":      {env: map[string]string{envComponents: "toolsets"}, code: 1, says: "toolsets needs a config file"},
		"runner: no token off loopback": {env: map[string]string{envComponents: "runner", envAddr: ":0"}, config: good, code: 1, says: envRPCToken},
		"toolsets: no token off lo":     {env: map[string]string{envComponents: "toolsets", envAddr: ":0"}, config: good, code: 1, says: envRPCToken},
		"rpc: short token":              {env: map[string]string{envComponents: "runner", envRPCToken: "short"}, config: good, code: 1, says: envRPCToken},
		"runner: bad URL":               {env: map[string]string{envComponents: "agent", envRunnerURL: "runner:8080"}, config: good, code: 1, says: envRunnerURL},
		"toolsets: bad URL":             {env: map[string]string{envComponents: "agent", envToolsetsURL: "tools"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agent":{"toolsets":["demo"]}}`, code: 1, says: envToolsetsURL},
		"toolsets: unreachable":         {env: map[string]string{envComponents: "runner", envToolsetsURL: "http://127.0.0.1:1", envDependencyWait: "0s"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agent":{"toolsets":["demo"]}}`, code: 1, says: "unavailable"},
		// Auth (T040).
		"auth: no credentials off lo": {env: map[string]string{envComponents: "agent", envAddr: ":0"}, config: good, code: 1, says: envAPIKeysFile},
		"mcp: no credentials off lo":  {env: map[string]string{envComponents: "mcp-server", envAddr: ":0"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]}}`, code: 1, says: envAPIToken},
		"auth: toolset typo":          {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"auth":{"api_keys":[{"id":"k","hash":"` + authHash + `","tenant":"t","toolsets":["dmeo"]}]}}`, code: 1, says: `toolset "dmeo" matches none`},
		"auth: bad hash":              {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"api_keys":[{"id":"k","hash":"hunter2","tenant":"t"}]}}`, code: 1, says: `API key "k"`},
		"auth: unknown field":         {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"api_keys":[{"id":"k","key":"hunter2"}]}}`, code: 1, says: "auth:"},
		"auth: keys file missing":     {env: map[string]string{envComponents: "agent", envAPIKeysFile: "/nonexistent/keys.json"}, config: good, code: 1, says: envAPIKeysFile},
		"auth: stateful mcp":          {env: map[string]string{envComponents: "mcp-server", envAPIToken: "a-token-0123456789abcdef"}, config: `{"mcp":{"stateful":true,"toolsets":[{"name":"demo","type":"demo"}]}}`, code: 1, says: "stateful"},
		// Identity provider tokens (T041).
		"auth.jwt: HMAC":                   {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"jwt":{"issuer":"https://id.example","audience":"https://a.example","algorithms":["HS256"]}}}`, code: 1, says: "HS256"},
		"auth.jwt: unknown field":          {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"jwt":{"issuer":"https://id.example","audience":"https://a.example","secret":"x"}}}`, code: 1, says: "secret"},
		"auth.jwt: bad leeway":             {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"jwt":{"issuer":"https://id.example","audience":"https://a.example","leeway":"soon"}}}`, code: 1, says: "auth"},
		"auth.jwt: http issuer":            {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"jwt":{"issuer":"http://id.example","audience":"https://a.example"}}}`, code: 1, says: "auth.jwt"},
		"auth.jwt: MCP audience no origin": {env: map[string]string{envComponents: "mcp-server"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"auth":{"jwt":{"issuer":"http://127.0.0.1:1","audience":"api://agentkit"}}}`, code: 1, says: "origin"},
		"bad dependency wait":              {env: map[string]string{envDependencyWait: "soon"}, config: good, code: 1, says: envDependencyWait},
		// The credential vault's backend (T043).
		"credentials: unknown field":      {config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"credentials":{"backnd":"file"}}`, code: 1, says: "backnd"},
		"credentials: unknown backend":    {config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"credentials":{"backend":"vault"}}`, code: 1, says: `credentials.backend "vault"`},
		"credentials: no key":             {config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"credentials":{"backend":"file","path":"/tmp/x.json"}}`, code: 1, says: `credentials.backend "file" needs the key ring in $` + envCredentialsKey},
		"credentials: file without path":  {env: map[string]string{envCredentialsKey: testCredentialsKey}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"credentials":{"backend":"file"}}`, code: 1, says: `backend "file" needs a path`},
		"credentials: memory with path":   {env: map[string]string{envCredentialsKey: testCredentialsKey}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"credentials":{"path":"x.json"}}`, code: 1, says: `backend "memory" keeps nothing in a file`},
		"credentials: postgres, no store": {env: map[string]string{envCredentialsKey: testCredentialsKey, envStore: "sqlite:/tmp/x.db"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"credentials":{"backend":"postgres"}}`, code: 1, says: `backend "postgres"`},
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
