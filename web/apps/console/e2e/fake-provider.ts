// A fake OAuth2 provider for the admin pages' browser tests (admin.spec.ts):
// the server's "mail" slot connects accounts here (e2e/admin.json,
// credentials.oauth2), so Connect runs the whole authorization code flow
// with no real account.
//
//   GET  /authorize  a consent page with Authorize and Cancel links, which
//                    go back to the redirect URI with a code, or with
//                    error=access_denied
//   POST /token      exchanges a code (or a refresh token) for tokens
//   GET  /healthz    for Playwright's webServer
//
// Run with Node 24 (type stripping): node e2e/fake-provider.ts <port> <canary>,
// where the canary goes into every token it issues (token.ts, providerCanary).
import { createServer } from "node:http";

const port = Number(process.argv[2] ?? 18483);
const providerCanary = process.argv[3] ?? "canary";
const codes = new Set<string>();

const page = (title: string, body: string) =>
  `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>${title}</title></head><body>${body}</body></html>`;

const escape = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

createServer((req, res) => {
  const url = new URL(req.url ?? "/", `http://127.0.0.1:${port}`);
  if (url.pathname === "/healthz") {
    res.end("ok");
    return;
  }
  if (req.method === "GET" && url.pathname === "/authorize") {
    const redirect = url.searchParams.get("redirect_uri") ?? "";
    const state = url.searchParams.get("state") ?? "";
    const code = `code-${codes.size + 1}`;
    codes.add(code);
    const ok = new URL(redirect);
    ok.searchParams.set("code", code);
    ok.searchParams.set("state", state);
    const no = new URL(redirect);
    no.searchParams.set("error", "access_denied");
    no.searchParams.set("state", state);
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end(
      page(
        "Fake provider",
        `<h1>Authorize agentkit</h1><p>agentkit wants to read your mail.</p>` +
          `<a href="${escape(ok.href)}">Authorize</a> <a href="${escape(no.href)}">Cancel</a>`,
      ),
    );
    return;
  }
  if (req.method === "POST" && url.pathname === "/token") {
    let body = "";
    req.on("data", (c: Buffer) => (body += c.toString()));
    req.on("end", () => {
      const form = new URLSearchParams(body);
      const grant = form.get("grant_type");
      const code = form.get("code") ?? "";
      if (grant === "authorization_code" && !codes.delete(code)) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "invalid_grant" }));
        return;
      }
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(
        JSON.stringify({
          access_token: `access-${providerCanary}`,
          refresh_token: `refresh-${providerCanary}`,
          token_type: "Bearer",
          expires_in: 3600,
        }),
      );
    });
    return;
  }
  res.writeHead(404);
  res.end();
}).listen(port, "127.0.0.1");
