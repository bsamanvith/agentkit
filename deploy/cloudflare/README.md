# One owner, behind Cloudflare Tunnel and Access

agentkit on one VM, reachable only through a Cloudflare Tunnel, with a
Cloudflare Access application that lets in one email. Two locks, and a
check that they stay on:

1. **Access** at Cloudflare's edge: nobody reaches the tunnel without signing
   in as you.
2. **agentkit checks Access** (`auth.cloudflare_access`): a request without a
   valid `Cf-Access-Jwt-Assertion` for your team and application gets 401,
   so a request that reaches the origin some other way (an open port, a
   tunnel hostname with no Access application) gets nothing but the public
   paths below.
3. **agentkit refuses to start open** (`"required": true` or
   `AGENTKIT_REQUIRE_AUTH=1`). This is no lock of its own: it makes sure
   another one is configured. cloudflared connects over loopback, and on
   loopback with no credentials agentkit would otherwise serve everyone, so a
   config that loses its credentials fails at startup instead of publishing an
   open API. Access counts as a credential here, and so does a key: with
   `AGENTKIT_API_TOKEN` set and Access off, agentkit starts. What keeps
   Access on is the config file's `cloudflare_access` section (section 3).

Use the all-in-one process (`agent,web`, plus `mcp-server` if you use it from
Claude Code or Cursor). Leave out `a2a`, `agui` and the split `runner` and
`toolsets` components: their endpoints are not needed here.

## 1. The Access application

In Zero Trust → Access → Applications, add a self-hosted application for
`agents.example.com`:

- Policy: **Allow**, include **Emails** = your email. Nothing else.
- Session duration: as short as you like (the assertion's `exp` follows it).
- Copy the **Application Audience (AUD) tag** (the application's overview),
  and note your team domain, `<team>.cloudflareaccess.com` (the Zero Trust
  dashboard's settings show it).

Bypass Access only for the paths that callers outside it must reach, and only
those you use. agentkit lets exactly these through without an assertion, and
nothing else:

| Path | Who calls it | Bypass when |
|---|---|---|
| `GET /oauth/callback` | your browser, redirected by an OAuth provider | you connect accounts (credentials or MCP OAuth) |
| `GET /oauth/client-metadata.json` | an MCP authorization server, fetching agentkit's client ID | you add MCP servers with `"auth": "oauth"` |
| `POST /v1/hooks/*` | a webhook sender; each delivery is signed (Standard Webhooks) | you use triggers with `"webhook": true` |

Add each as its own application (or path rule) with a **Bypass** policy for
Everyone. `/healthz` and `/readyz` are open too, for probes on the VM; they
need no bypass, as nothing outside calls them. Without a valid assertion
`GET /readyz` answers its status code and `{"status":"ready"}` or
`{"status":"not ready"}` only, never the components' details. A webhook id
is letters, digits, `-` and `_`; any other `/v1/hooks/` path needs an
assertion. Add a Cloudflare rate-limit rule on `/v1/hooks/*`: it bypasses
Access, so anyone can reach it.

## 2. The tunnel

```sh
cloudflared tunnel create agentkit
cloudflared tunnel route dns agentkit agents.example.com
```

`~/.cloudflared/config.yml`:

```yaml
tunnel: agentkit
credentials-file: /home/me/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: agents.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

Keep the origin's `Host` header (no `httpHostHeader`): the cross-site check
compares the browser's `Origin` with it.

## 3. agentkit

Bind loopback only, and deny inbound traffic on the VM's firewall (cloudflared
dials out; nothing needs to dial in):

```sh
sudo ufw default deny incoming && sudo ufw allow ssh && sudo ufw enable
```

An environment file, mode 600 (`/etc/agentkit/env`, a systemd
`EnvironmentFile`):

```sh
AGENTKIT_COMPONENTS=agent,web,mcp-server
AGENTKIT_ADDR=127.0.0.1:8080
AGENTKIT_REQUIRE_AUTH=1
# Optional: a key, for a client that should act as something other than
# your email. It is no way around Access: its requests still need an
# assertion (cloudflared access token) to get past the gate. Sent beside a
# valid assertion, the key wins: the request acts as the key, not the email.
AGENTKIT_API_TOKEN=<openssl rand -base64 48>
AGENTKIT_CREDENTIALS_KEY=k1:<openssl rand -base64 32>
AGENTKIT_STORE=sqlite:/var/lib/agentkit/agentkit.db
ANTHROPIC_API_KEY=sk-ant-...
```

And Access in the config file:

```json
{"auth": {"required": true, "cloudflare_access": {
  "team_domain": "myteam.cloudflareaccess.com", "aud": "<the AUD tag>",
  "emails": ["me@example.com"]
}}}
```

The `cloudflare_access` section is what makes Access mandatory: with it,
a missing or empty `team_domain` or `aud` fails startup. The variables
`AGENTKIT_CF_ACCESS_TEAM_DOMAIN` and `AGENTKIT_CF_ACCESS_AUD` win over the
file's values, but alone they don't make Access mandatory: a misspelled one
leaves Access off, and with a key configured agentkit still starts (it logs
`Cloudflare Access is not configured`). So keep the section in the file.

`emails` is the second check on the Access policy. Without it every email
the policy admits gets `scopes` (default every scope); agentkit warns at
startup, and with authentication required it refuses to start unless
`scopes` is set in the section.

Don't configure `auth.users`: with Access, the console needs no password; its
requests carry the assertion and your email is the user (tenant `default`,
every scope and toolset).

Access is checked in the processes people reach: `agent`, `mcp-server` and
`web`. A process that runs only `runner` or `toolsets` is never checked: the
API calls it with `AGENTKIT_RPC_TOKEN`, never through Access. In a process
that runs `runner` or `toolsets` beside `agent`, `mcp-server` or `web`, `/rpc/` passes without an
assertion only when `AGENTKIT_RPC_TOKEN` is set. So the split deployment works
with the same Access config on every container.

Start it and check the log for `Cloudflare Access: every request needs a valid
assertion`. If the team's keys can't be fetched at startup it warns and
retries on the next request; every request fails until they are.

## Check it

```sh
# From the VM, without Access: refused.
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/v1/agents   # 401
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz     # 200

# Through Access, from a machine where you've signed in:
cloudflared access login https://agents.example.com
curl -s -H "cf-access-token: $(cloudflared access token -app=https://agents.example.com)" \
  https://agents.example.com/v1/auth/me                                     # your email
```

Then open `https://agents.example.com` in a browser: Access asks you to sign
in, and the console opens with no second sign-in.

Service tokens (Access's machine credentials) have no email and are refused;
use `cloudflared access` for scripts, as above.

## MCP clients

An MCP client (Claude Code, Cursor) can't sign in to Access by itself, and
agentkit's MCP OAuth discovery is behind Access too, so it gets 401 unless it
sends an Access token. Sign in with cloudflared and pass its token as the
`cf-access-token` header; Access turns it into the assertion and your email
is the user:

```sh
cloudflared access login https://agents.example.com
claude mcp add --transport http agentkit https://agents.example.com/mcp/<toolset> \
  --header "cf-access-token: $(cloudflared access token -app=https://agents.example.com)"
```

In Cursor, put the same header under the server's `headers` in `mcp.json`.
The token lasts as long as the Access session (the application's session
duration): when it expires the client gets 401, and you run `cloudflared
access login` and set the header again.
