import { expect, test, type Page } from "@playwright/test";
import { adminPort, providerCanary, providerPort, serverKeyCanary, typedKeyCanary, users } from "./token";

// The admin pages (T064) against their own server (e2e/admin.json): local
// users ada (admin) and uma (user), a credential vault, a skipped optional
// toolset, a server key that must never reach a page (EXA_API_KEY is a
// canary), and an account slot that connects through fake-provider.ts.

const base = `http://127.0.0.1:${adminPort}`;
// The clipboard is one of the places a value must never reach (expectNoSecrets reads it).
test.use({ baseURL: base, permissions: ["clipboard-read", "clipboard-write"] });

const secrets = [serverKeyCanary, typedKeyCanary, providerCanary];

/**
 * What left the browser's pages, in every window (the popup too): each
 * request's method, URL and body, and each console line.
 */
type Traffic = { requests: { method: string; url: string; body: string }[]; logs: string[] };

function watchTraffic(page: Page): Traffic {
  const t: Traffic = { requests: [], logs: [] };
  const context = page.context();
  context.on("request", (r) => t.requests.push({ method: r.method(), url: r.url(), body: r.postData() ?? "" }));
  context.on("console", (m) => t.logs.push(m.text()));
  return t;
}

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    // Only the console's own: the fake provider, when this tab goes there, has no favicon.
    const from = m.location().url;
    if (from && !from.startsWith(base)) return;
    // A signed-out tab's first requests are 401s, which open the sign-in card.
    if (m.type() === "error" && !m.text().includes("status of 401")) errors.push(`${m.text()} (${m.location().url})`);
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

async function signIn(page: Page, who: { username: string; password: string }, path: string) {
  await page.goto(path);
  const card = page.getByRole("dialog", { name: "Sign in to agentkit" });
  await card.getByLabel("Username").fill(who.username);
  await card.getByLabel("Password").fill(who.password);
  await card.getByRole("button", { name: "Sign in" }).click();
  await expect(card).toHaveCount(0);
}

/**
 * Every value that must never reach a page, checked against its HTML, its
 * fields' values (which the HTML leaves out), its storage, the clipboard and
 * its URL; with traffic, also against every URL and console line, and every
 * request body but the one PUT that sets the typed key.
 */
async function expectNoSecrets(page: Page, traffic?: Traffic) {
  const html = await page.content();
  const values = await page.evaluate(() =>
    Array.from(document.querySelectorAll("input, textarea"), (e) => (e as HTMLInputElement).value).join("\n"),
  );
  const storage = await page.evaluate(() => JSON.stringify({ ...sessionStorage, ...localStorage }));
  const clipboard = await page.evaluate(() => navigator.clipboard.readText());
  for (const secret of secrets) {
    expect(html).not.toContain(secret);
    expect(values).not.toContain(secret);
    expect(storage).not.toContain(secret);
    expect(clipboard).not.toContain(secret);
    expect(page.url()).not.toContain(secret);
    if (!traffic) continue;
    for (const r of traffic.requests) {
      expect(r.url, `${r.method} ${r.url}`).not.toContain(secret);
      const keyPut = secret === typedKeyCanary && r.method === "PUT" && r.url === `${base}/v1/credentials/crm`;
      if (!keyPut) expect(r.body, `${r.method} ${r.url}`).not.toContain(secret);
    }
    for (const line of traffic.logs) expect(line).not.toContain(secret);
  }
}

test("agents: the list, the detail with the system prompt for an admin, and Chat with this agent", async ({ page }) => {
  const errors = watchErrors(page);
  await signIn(page, users.admin, "/agents/researcher");
  await expect(page.getByRole("heading", { level: 1, name: "researcher" })).toBeVisible();
  const list = page.getByRole("navigation", { name: "Agents" });
  await expect(list.getByRole("link")).toHaveCount(2);
  await expect(page.getByText("Searches the web and cites its sources.").last()).toBeVisible();
  await expect(page.getByText(/3 of its 5 toolsets aren't ready: parallel, crm, mail/)).toBeVisible();

  const prompt = page.getByText("You are the admin pages' test agent.");
  await expect(prompt).toBeHidden();
  await page.getByText("System prompt (admins only)").click();
  await expect(prompt).toBeVisible();

  await page.getByRole("link", { name: "Chat with researcher" }).click();
  await expect(page).toHaveURL(/\/new\?agent=researcher$/);
  await expect(page.getByRole("button", { name: "Agent: researcher" })).toBeVisible();
  await expectNoSecrets(page);
  expect(errors).toEqual([]);
});

test("toolsets: a skipped one with its reason, the MCP command without a token, and the tools' fields", async ({
  page,
  context,
}) => {
  const errors = watchErrors(page);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await signIn(page, users.admin, "/toolsets/parallel");
  await expect(page.getByRole("heading", { level: 1, name: "parallel" })).toBeVisible();
  await expect(page.getByText(/^Skipped: .*PARALLEL_API_KEY/)).toBeVisible();
  await expect(page.getByLabel("Command")).toHaveCount(0);

  await page.getByRole("navigation", { name: "Toolsets" }).getByRole("link", { name: /demo/ }).click();
  await expect(page.getByRole("heading", { level: 1, name: "demo" })).toBeVisible();
  const url = `${base}/mcp/demo`;
  await expect(page.getByLabel("MCP URL")).toHaveText(url);
  const command = `claude mcp add --transport http demo ${url} --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`;
  await expect(page.getByLabel("Command")).toHaveText(command);
  await page.getByRole("button", { name: "Copy command" }).click();
  await expect(page.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(command);

  // The MCP URL works: the toolset answers there (this test server's MCP has no auth).
  const mcp = await page.request.post(url, {
    headers: { Accept: "application/json, text/event-stream", "Content-Type": "application/json" },
    data: { jsonrpc: "2.0", id: 1, method: "tools/list", params: {} },
  });
  expect(mcp.status()).toBe(200);
  expect(await mcp.text()).toContain('"calculate"');

  await page.getByText("calculate", { exact: true }).click();
  const input = page.getByRole("table", { name: "Input" });
  await expect(input.getByRole("row", { name: /^op/ })).toContainText("required");
  await expect(input.getByRole("row", { name: /^op/ })).toContainText("operation: add, sub, mul or div");
  await expect(input.getByRole("row", { name: /^a / })).toContainText("integer");
  await expect(page.getByRole("table", { name: "Output" }).getByRole("row", { name: /^result/ })).toBeVisible();
  await page.getByRole("button", { name: "Show raw schema" }).click();
  await expect(page.getByLabel("calculate raw schema")).toContainText('"output_schema"');

  // The exa toolset reads its key from the server: the page says where, never what.
  await page.getByRole("navigation", { name: "Toolsets" }).getByRole("link", { name: /exa/ }).click();
  await expect(page.getByText("Server key EXA_API_KEY")).toBeVisible();
  await expectNoSecrets(page);
  expect(errors).toEqual([]);
});

test("connectors: statuses and names, a key set once, an account connected, no value on the page", async ({ page }) => {
  const errors = watchErrors(page);
  const traffic = watchTraffic(page);
  await signIn(page, users.user, "/connections");
  await expect(page.getByRole("heading", { level: 1, name: "Connectors" })).toBeVisible();
  const row = (name: string) => page.getByRole("article", { name: new RegExp(`^${name}`, "i") });
  await expect(row("exa")).toContainText("Using the server key");
  await expect(row("exa")).toContainText("EXA_API_KEY");
  await expect(row("parallel")).toContainText("Server key not set");
  await expect(row("parallel")).toContainText("Set PARALLEL_API_KEY");
  await expect(row("crm")).toContainText("Not connected");
  await expect(row("demo")).toHaveCount(0);
  await expectNoSecrets(page);

  // A key: typed once, sent once, never shown.
  await row("crm").getByRole("button", { name: "Connect crm" }).click();
  const dialog = page.getByRole("dialog", { name: "Connect crm" });
  await dialog.getByLabel("API key").fill(typedKeyCanary);
  const put = page.waitForRequest((r) => r.method() === "PUT");
  await dialog.getByRole("button", { name: "Save" }).click();
  const sent = await put;
  expect(sent.url()).toBe(`${base}/v1/credentials/crm`); // the slot's own, the caller's scope in the body
  expect(sent.postDataJSON()).toEqual({ scope: "user", kind: "api_key", api_key: typedKeyCanary });
  await expect(dialog).toHaveCount(0);
  await expect(row("crm")).toContainText("Connected");
  await expect(row("crm")).toContainText("Your key, added");
  await expectNoSecrets(page);

  // An account: the provider in a popup, which reports back and closes.
  const popupOpened = page.waitForEvent("popup");
  await row("mail").getByRole("button", { name: "Connect mail" }).click();
  await expect(row("mail")).toContainText("Waiting for mail");
  const popup = await popupOpened;
  await popup.getByRole("link", { name: "Authorize" }).click();
  await popup.waitForEvent("close");
  await expect(row("mail")).toContainText("Connected");
  await expect(row("mail")).toContainText("Your account, connected");
  await expect(page.getByRole("status").filter({ hasText: "mail connected." })).toHaveCount(1);

  // Disconnect asks first.
  await row("crm").getByRole("button", { name: "Disconnect crm" }).click();
  await page.getByRole("dialog", { name: "Disconnect crm?" }).getByRole("button", { name: "Disconnect" }).click();
  await expect(row("crm")).toContainText("Not connected");

  await page.reload();
  await expect(row("mail")).toContainText("Connected");
  await expectNoSecrets(page, traffic);
  // The key went in one request, and nothing else carried it.
  expect(traffic.requests.filter((r) => r.body.includes(typedKeyCanary))).toHaveLength(1);
  // The API never sent a value either.
  const listed = await page.request.get("/v1/credentials");
  expect(listed.status()).toBe(200);
  for (const secret of secrets) expect(await listed.text()).not.toContain(secret);
  expect(errors).toEqual([]);
});

test("connectors: Cancel at the provider says nothing was saved", async ({ page }) => {
  await signIn(page, users.admin, "/connections");
  const mail = page.getByRole("article", { name: /^mail/ });
  await expect(mail).toContainText("Not connected");
  const popupOpened = page.waitForEvent("popup");
  await mail.getByRole("button", { name: "Connect mail" }).click();
  const popup = await popupOpened;
  await popup.getByRole("link", { name: "Cancel" }).click();
  await popup.waitForEvent("close");
  await expect(mail).toContainText("You didn't allow access. Nothing was saved.");
  await expect(mail).toContainText("Not connected");
});

test("connectors: with popups blocked, the same tab goes to the provider and comes back", async ({ page }) => {
  const errors = watchErrors(page);
  const traffic = watchTraffic(page);
  await page.addInitScript(() => {
    window.open = () => null; // a popup blocker
  });
  await signIn(page, users.admin, "/connections");
  const mail = page.getByRole("article", { name: /^mail/ });
  await expect(mail).toContainText("Not connected");
  await mail.getByRole("button", { name: "Connect mail" }).click();
  await expect(page).toHaveURL(new RegExp(`^http://127.0.0.1:${providerPort}/`));
  await page.getByRole("link", { name: "Authorize" }).click();
  // Back on the route it left from, with the result, and the hand-over not kept.
  await expect(page).toHaveURL(`${base}/connections`);
  await expect(mail).toContainText("Your account, connected");
  await expect(page.getByRole("status").filter({ hasText: "mail connected." })).toHaveCount(1);
  expect(await page.evaluate(() => JSON.stringify({ ...sessionStorage }))).not.toContain("agentkit.connect");
  await expectNoSecrets(page, traffic);
  expect(errors).toEqual([]);
});

test("on a phone, the pages fit without sideways scroll", async ({ page }) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 375, height: 812 });
  await signIn(page, users.admin, "/connections");
  await expect(page.getByRole("article", { name: /^mail/ })).toBeVisible();
  for (const path of ["/connections", "/toolsets/demo", "/agents/researcher"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByLabel("Loading")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth), path).toBeLessThanOrEqual(375);
  }
  expect(errors).toEqual([]);
});

test("roles: a user sees no admin navigation, gets the 403 page, and the API refuses admin data", async ({ page }) => {
  const errors = watchErrors(page);
  await signIn(page, users.user, "/agents/researcher");
  await expect(page.getByRole("heading", { level: 1, name: "Admins only" })).toBeVisible();
  const sections = page.getByRole("navigation", { name: "Sections" });
  await expect(sections.getByRole("link", { name: "Connectors" })).toBeVisible();
  await expect(sections.getByRole("link", { name: "Agents" })).toHaveCount(0);
  await expect(sections.getByRole("link", { name: "Toolsets" })).toHaveCount(0);
  await expect(page.getByText("You are the admin pages' test agent.")).toHaveCount(0);

  // Direct calls with the user's session (the page's cookies).
  for (const path of [
    "/v1/agents/researcher/versions",
    "/v1/triggers",
    "/v1/webhooks/secret",
    "/v1/toolsets/demo/review",
  ]) {
    const r = await page.request.get(path);
    expect(r.status(), path).toBe(403);
    expect((await r.json()).error.code).toBe("forbidden");
  }
  const agent = await (await page.request.get("/v1/agents/researcher")).json();
  expect(agent.name).toBe("researcher");
  expect(agent).not.toHaveProperty("system");
  expect(errors).toEqual([]);
});
