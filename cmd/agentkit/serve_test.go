package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/cfaccess"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/auth/login/logintest"
)

// agentClash gives the agent two toolsets with the same tools and no prefix.
const agentClash = `{"mcp":{"toolsets":[{"name":"a","type":"demo"},{"name":"b","type":"demo"}]},"agent":{"toolsets":["a","b"]}}`

// TestServeRejectsBadSetup: a mistake in the environment or the config fails
// startup, before anything listens, with a message naming the mistake.
// authHash is the hash of a key, for configs.
var authHash = auth.HashKey("hunter2-hunter2-hunter2")

// userHash is a well-formed password hash, for configs (made with the
// test hasher: no test here signs in with it).
var userHash = logintest.PHC((&logintest.Hasher{}).Hash, "hunter2-hunter2", login.DefaultParams)

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
		"agent: unknown provider":      {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"model":{"provider":"mistral"}}}`, code: 1, says: "mistral"},
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
		// The agui component (T069).
		"agui: without the agent API": {env: map[string]string{envComponents: "agui"}, config: good, code: 1, says: aguiSays("agui: needs the agent component")},
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
		"runtime: bad allow pattern":      {env: map[string]string{envComponents: "agent"}, config: `{"runtime_toolsets":{"tenants":{"acme":{"mcp_servers":{"allow":["https://a*b.com"]}}}}}`, code: 1, says: "mcp_servers.allow"},
		"runtime: bad registry url":       {env: map[string]string{envComponents: "agent"}, config: `{"runtime_toolsets":{"registry":{"url":"ftp://x"}}}`, code: 1, says: "registry.url"},
		"public url: a path":              {env: map[string]string{envComponents: "agent", envPublicURL: "https://agents.example.com/x"}, config: `{}`, code: 1, says: envPublicURL},
		"public url: http":                {env: map[string]string{envComponents: "agent", envPublicURL: "http://agents.example.com"}, config: `{}`, code: 1, says: envPublicURL},
		"agents: unknown provider":        {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"},{"name":"b","model":{"provider":"mistral"}}]}`, code: 1, says: "agents.b.model.provider"},
		"agents: bad effort":              {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a","model":{"effort":"extreme"}}]}`, code: 1, says: "agents.a.model"},
		"agents: tool name clash":         {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"a","type":"demo"},{"name":"b","type":"demo"}]},"agents":[{"name":"x","toolsets":["a","b"]}]}`, code: 1, says: "agents.x.toolsets"},
		"agent_api: unknown field":        {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"}],"agent_api":{"system":"x"}}`, code: 1, says: "system"},
		"agent_api: no concurrent runs":   {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"a"}],"agent_api":{"max_concurrent_runs":0}}`, code: 1, says: "concurrent"},
		// Runner pools (T105).
		"runners: unknown field":          {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pool":[]}}`, code: 1, says: "pool"},
		"runners: route to unknown pool":  {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"chat"}],"routes":[{"pool":"chta","runs":"chat"}]}}`, code: 1, says: `runners.routes[0].pool: "chta"`},
		"runners: route to no agents":     {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"chat"}],"routes":[{"pool":"chat","agents":[]}]}}`, code: 1, says: `runners.routes[0].agents: empty`},
		"runners: unknown default":        {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"chat"}],"default":"bg"}}`, code: 1, says: `runners.default: "bg"`},
		"runners: no default of two":      {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a"},{"name":"b"}]}}`, code: 1, says: "runners.default"},
		"runners: routes without pools":   {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"routes":[{"pool":"chat"}]}}`, code: 1, says: "runners.pools"},
		"runners: duplicate pool":         {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a"},{"name":"a"}],"default":"a"}}`, code: 1, says: "runners.pools[1].name"},
		"runners: bad pool name":          {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a b"}]}}`, code: 1, says: "runners.pools[0].name"},
		"runners: negative limit":         {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a","limit":-1}]}}`, code: 1, says: "runners.pools.a.limit"},
		"runners: unknown isolation":      {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a","isolation":"container"}]}}`, code: 1, says: `isolation "container"`},
		"runners: sandbox pool":           {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a","isolation":"sandbox"}]}}`, code: 1, says: "sandbox pools come with T106"},
		"runners: vm pool":                {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a","isolation":"vm"}]}}`, code: 1, says: "vm pools come with T107"},
		"runners: bad runs":               {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a"}],"routes":[{"pool":"a","runs":"batch"}]}}`, code: 1, says: "runners.routes[0].runs"},
		"runners: unknown agent":          {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"x"}],"runners":{"pools":[{"name":"a"}],"routes":[{"pool":"a","agents":["y"]}]}}`, code: 1, says: "runners.routes[0].agents"},
		"runners: route isolation":        {env: map[string]string{envComponents: "agent"}, config: `{"runners":{"pools":[{"name":"a"}],"routes":[{"pool":"a","isolation":"gvisor"}]}}`, code: 1, says: "runners.routes[0].isolation"},
		"agents: unknown isolation":       {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"x","isolation":"jail"}]}`, code: 1, says: "agents.x.isolation"},
		"agents: sandbox, no pools":       {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"x","isolation":"sandbox"}]}`, code: 1, says: "agents.x.isolation: sandbox"},
		"agents: vm, only process pools":  {env: map[string]string{envComponents: "agent"}, config: `{"agents":[{"name":"x","isolation":"vm"}],"runners":{"pools":[{"name":"a"}]}}`, code: 1, says: "agents.x.isolation: no pool"},
		"agent: unknown isolation (form)": {env: map[string]string{envComponents: "agent"}, config: `{"agent":{"isolation":"jail"}}`, code: 1, says: "agent.isolation"},
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
		// Required authentication, and Cloudflare Access.
		"require auth: open on loopback":  {env: map[string]string{envComponents: "agent", envRequireAuth: "1"}, config: good, code: 1, says: "authentication is required"},
		"require auth: from the config":   {env: map[string]string{envComponents: "mcp-server"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"auth":{"required":true}}`, code: 1, says: envCFAccessTeamDomain},
		"require auth: not turned off":    {env: map[string]string{envComponents: "agent", envRequireAuth: "0"}, config: `{"auth":{"required":true}}`, code: 1, says: "authentication is required"},
		"require auth: users not for MCP": {env: map[string]string{envComponents: "agent,mcp-server", envRequireAuth: "true"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"auth":{"users":[{"username":"ann","password_hash":"` + userHash + `","tenant":"t","roles":["user"]}]}}`, code: 1, says: "authentication is required"},
		"require auth: bad value":         {env: map[string]string{envComponents: "agent", envRequireAuth: "maybe"}, config: good, code: 1, says: envRequireAuth},
		"require auth: runner no token":   {env: map[string]string{envComponents: "runner", envRequireAuth: "1"}, config: good, code: 1, says: envRPCToken},
		"require auth: toolsets no token": {env: map[string]string{envComponents: "toolsets", envRequireAuth: "1"}, config: good, code: 1, says: envRPCToken},
		"require auth: access, no emails": {env: map[string]string{envComponents: "agent", envRequireAuth: "1", envCFAccessTeamDomain: "http://127.0.0.1:1", envCFAccessAUD: "x"}, config: good, code: 1, says: "list the emails"},
		"require auth: web, no emails":    {env: map[string]string{envComponents: "web", envCFAccessTeamDomain: "http://127.0.0.1:1", envCFAccessAUD: "x"}, config: `{"auth":{"required":true}}`, code: 1, says: "list the emails"},
		"cf access: no aud":               {env: map[string]string{envComponents: "agent", envCFAccessTeamDomain: "myteam"}, config: good, code: 1, says: "aud"},
		"cf access: no team":              {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"cloudflare_access":{"aud":"x"}}}`, code: 1, says: "team_domain"},
		"cf access: http team":            {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"cloudflare_access":{"team_domain":"http://myteam.example","aud":"x"}}}`, code: 1, says: "auth.cloudflare_access"},
		"cf access: unknown field":        {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"cloudflare_access":{"team":"myteam","aud":"x"}}}`, code: 1, says: "team"},
		"cf access: toolset typo":         {env: map[string]string{envComponents: "agent"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"auth":{"cloudflare_access":{"team_domain":"myteam","aud":"x","toolsets":["dmeo"]}}}`, code: 1, says: `toolset "dmeo" matches none`},
		"cf access: web only, bad config": {env: map[string]string{envComponents: "web", envCFAccessAUD: "x"}, code: 1, says: "team_domain"},
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
		// Local users (T061).
		"users: bad hash":           {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"users":[{"username":"ann","password_hash":"hunter2","tenant":"t","roles":["user"]}]}}`, code: 1, says: `user "ann"`},
		"users: unknown role":       {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"users":[{"username":"ann","password_hash":"` + userHash + `","tenant":"t","roles":["owner"]}]}}`, code: 1, says: `role "owner"`},
		"users: a password":         {env: map[string]string{envComponents: "agent"}, config: `{"auth":{"users":[{"username":"ann","password":"hunter2","tenant":"t","roles":["user"]}]}}`, code: 1, says: "password"},
		"users: file missing":       {env: map[string]string{envComponents: "agent", envUsersFile: "/nonexistent/users.json"}, config: good, code: 1, says: envUsersFile},
		"users: not enough for MCP": {env: map[string]string{envComponents: "agent,mcp-server", envAddr: ":0"}, config: `{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"auth":{"users":[{"username":"ann","password_hash":"` + userHash + `","tenant":"t","roles":["user"]}]}}`, code: 1, says: envAPIToken},
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

// TestAccessPublic: exactly the health probes, the OAuth callback and its
// client metadata, and inbound webhooks pass Cloudflare Access's gate without
// an assertion.
func TestAccessPublic(t *testing.T) {
	t.Parallel()
	for req, want := range map[[2]string]bool{
		{"GET", "/healthz"}:                               true,
		{"GET", "/readyz"}:                                true,
		{"GET", "/oauth/callback"}:                        true,
		{"GET", "/oauth/client-metadata.json"}:            true,
		{"POST", "/v1/hooks/trg_1"}:                       true,
		{"GET", "/version"}:                               false,
		{"GET", "/healthz/x"}:                             false,
		{"POST", "/healthz"}:                              false,
		{"POST", "/oauth/callback"}:                       false,
		{"GET", "/v1/hooks/trg_1"}:                        false,
		{"POST", "/v1/hooks/"}:                            false,
		{"POST", "/v1/hooks/trg_1/x"}:                     false,
		{"GET", "/v1/auth/config"}:                        false,
		{"POST", "/v1/auth/login"}:                        false,
		{"GET", "/"}:                                      false,
		{"GET", "/.well-known/oauth-protected-resource"}:  false,
		{"HEAD", "/healthz"}:                              true,
		{"HEAD", "/readyz"}:                               true,
		{"GET", "/healthz/"}:                              false,
		{"GET", "//healthz"}:                              false,
		{"POST", "/v1/hooks/.."}:                          false,
		{"POST", "/v1/hooks/."}:                           false,
		{"POST", "/v1/hooks/%2e%2e"}:                      false,
		{"POST", "/v1/hooks/a%2Fb"}:                       false,
		{"POST", "/v1/hooks/trg_A-z09"}:                   true,
		{"POST", "/v1/hooks/" + strings.Repeat("a", 129)}: false,
		{"POST", "/rpc/runner/Agents"}:                    false,
	} {
		if got := accessPublic(httptest.NewRequest(req[0], req[1], nil)); got != want {
			t.Errorf("%s %s: public = %v, want %v", req[0], req[1], got, want)
		}
	}
}

// TestAccessGate: the RPC of a runner or toolsets served beside the API
// passes the gate only with its own token, and /readyz tells a caller
// without an assertion its status alone.
func TestAccessGate(t *testing.T) {
	t.Parallel()
	access, err := cfaccess.New(cfaccess.Config{TeamDomain: "http://127.0.0.1:1", AUD: "x", Tenant: "default"})
	if err != nil {
		t.Fatal(err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"not ready","components":[{"name":"agent","reason":"dial db.internal:5432"}]}`)
		}
	})
	wrap := func(env serveEnv) http.Handler {
		h := http.Handler(inner)
		mw := accessGate(access, env)
		for i := len(mw) - 1; i >= 0; i-- {
			h = mw[i](h)
		}
		return h
	}
	send := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	for name, tc := range map[string]struct {
		env  serveEnv
		want int
	}{
		"runner with a token":    {serveEnv{components: []string{"agent", runnerName}, rpcToken: splitToken}, http.StatusOK},
		"toolsets with a token":  {serveEnv{components: []string{"agent", toolsetsName}, rpcToken: splitToken}, http.StatusOK},
		"runner without a token": {serveEnv{components: []string{"agent", runnerName}}, http.StatusUnauthorized},
		"no runner":              {serveEnv{components: []string{"agent"}, rpcToken: splitToken}, http.StatusUnauthorized},
	} {
		if got := send(wrap(tc.env), "POST", "/rpc/runner/Agents").Code; got != tc.want {
			t.Errorf("%s: POST /rpc/runner/Agents = %d, want %d", name, got, tc.want)
		}
	}
	w := send(wrap(serveEnv{components: []string{"agent"}}), "GET", "/readyz")
	if body := strings.TrimSpace(w.Body.String()); w.Code != http.StatusServiceUnavailable || body != `{"status":"not ready"}` {
		t.Errorf("GET /readyz without an assertion = %d %q, want 503 and the status alone", w.Code, body)
	}
}
