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
		"unusable address":       {env: map[string]string{envAddr: "127.0.0.1:not-a-port"}, config: good, code: 1, says: "not-a-port"},
		"positional argument":    {config: good, args: []string{"extra"}, code: 2},
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
		})
	}
}
