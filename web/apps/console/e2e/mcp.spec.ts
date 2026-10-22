import { expect, test, type Page } from "@playwright/test";
import { fakeMcpPort, mcpPort, mcpTokenCanary, users } from "./token";

// Adding an MCP server at run time (T096), against its own server
// (e2e/mcp.json) and fake-mcp.ts, a fake MCP server that wants a bearer
// token and a fake MCP Registry that lists it:
//
//   add (find it in the registry, Connect, sign in with a ${VAR} header)
//   → review (approve one tool, reject the flagged one)
//   → use it (a stored agent gets a published version; a chat calls the tool)
//   → rug pull (the server changes a tool: review needed, the diff, approve)
//
// The fake's tools and registry entry carry markup and an invisible
// character: the page must show them as text. The fake's token (a canary)
// is read by the server from FAKE_MCP_TOKEN and must never reach a page.
// The tests run in order: each starts where the last one left the server.

const base = `http://127.0.0.1:${mcpPort}`;
const fake = `http://127.0.0.1:${fakeMcpPort}`;
test.use({ baseURL: base });
test.describe.configure({ mode: "serial" });

const pasted = "sk-pasted-by-mistake-t096-0f3c";

type Traffic = { requests: { method: string; url: string; body: string }[]; logs: string[] };

function watch(page: Page): { traffic: Traffic; errors: string[] } {
  const traffic: Traffic = { requests: [], logs: [] };
  const errors: string[] = [];
  page
    .context()
    .on("request", (r) => traffic.requests.push({ method: r.method(), url: r.url(), body: r.postData() ?? "" }));
  page.on("console", (m) => {
    traffic.logs.push(m.text());
    // A signed-out tab's first requests are 401s; the first Connect's 422
    // (auth_required) and the refused private address's 400 are expected.
    if (m.type() === "error" && !/status of (401|422|400)/.test(m.text())) errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return { traffic, errors };
}

/** Neither the server's token nor a key pasted by mistake is on the page, in its storage, URL, traffic or console. */
async function expectNoSecrets(page: Page, traffic: Traffic) {
  const html = await page.content();
  const values = await page.evaluate(() =>
    Array.from(document.querySelectorAll("input, textarea"), (e) => (e as HTMLInputElement).value).join("\n"),
  );
  const storage = await page.evaluate(() => JSON.stringify({ ...sessionStorage, ...localStorage }));
  for (const secret of [mcpTokenCanary, pasted]) {
    expect(html).not.toContain(secret);
    expect(values).not.toContain(secret);
    expect(storage).not.toContain(secret);
    expect(page.url()).not.toContain(secret);
    for (const r of traffic.requests) {
      expect(r.url).not.toContain(secret);
      expect(r.body, `${r.method} ${r.url}`).not.toContain(secret);
    }
    for (const line of traffic.logs) expect(line).not.toContain(secret);
  }
}

/** The hostile text ran no script and made no element. */
async function expectNoMarkup(page: Page) {
  expect(await page.evaluate(() => (window as { __xss?: unknown }).__xss)).toBeUndefined();
  await expect(page.locator("main img, main script, main b")).toHaveCount(0);
  await expect(page.locator('a[href*="evil.example"]')).toHaveCount(0);
}

async function signIn(page: Page, who: { username: string; password: string }, path: string) {
  await page.goto(path);
  const card = page.getByRole("dialog", { name: "Sign in to agentkit" });
  await card.getByLabel("Username").fill(who.username);
  await card.getByLabel("Password").fill(who.password);
  await card.getByRole("button", { name: "Sign in" }).click();
  await expect(card).toHaveCount(0);
}

/** A request with the page's session, as the console makes it (CSRF token, Origin). */
async function api(page: Page, method: string, path: string, data?: unknown) {
  const me = await (await page.request.get(`${base}/v1/auth/me`)).json();
  return page.request.fetch(`${base}${path}`, {
    method,
    headers: { "X-Agentkit-CSRF": me.csrf, Origin: base },
    ...(data === undefined ? {} : { data }),
  });
}

test("add from the registry, sign in with a header, review, and a stored agent calls the tool", async ({ page }) => {
  const { traffic, errors } = watch(page);
  expect((await page.request.post(`${fake}/reset`)).ok()).toBe(true);
  await signIn(page, users.admin, "/toolsets");
  // A stored agent for step 4 (the config's default agent is read-only).
  const saved = await api(page, "POST", "/v1/agents/helper/versions", {
    definition: { description: "A stored agent.", system: "Help." },
    publish: true,
  });
  expect([201, 409]).toContain(saved.status());

  await page.getByRole("link", { name: "Add an MCP server" }).click();
  await expect(page).toHaveURL(/\/toolsets\/new$/);
  await expect(page.getByRole("heading", { level: 1, name: "Add an MCP server" })).toBeVisible();
  await expect(page.getByRole("list", { name: "Steps" }).locator('[aria-current="step"]')).toContainText("Server");

  // Find it in the registry: the packages-only entry is dropped, and its text is text.
  await page.getByLabel("Find a server").fill("fake");
  const found = page.getByRole("list", { name: "Servers found" });
  await expect(found.getByText("test.fake/fake-mcp")).toBeVisible();
  await expect(found.getByText("test.fake/packages-only")).toHaveCount(0);
  await expect(found.getByText("<script>window.__xss=3</script>", { exact: false })).toBeVisible();
  await found.getByRole("button", { name: `Use test.fake/fake-mcp at ${fake}/mcp` }).click();
  await expect(page.getByLabel("Server URL")).toHaveValue(`${fake}/mcp`);
  await expect(page.getByLabel("Name")).toHaveValue("fake-mcp");

  // Connect: the server answers 401, so step 2 asks how to sign in. Nothing is saved yet.
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.getByRole("heading", { level: 2, name: "How does agentkit sign in?" })).toBeVisible();
  await expect(page.getByText("The server asked for sign-in (401).")).toBeVisible();
  await expect(page.getByRole("radio", { name: /Each person signs in/ })).toBeDisabled();
  await expect(page.getByRole("radio", { name: /No sign-in/ })).toBeDisabled();
  const listed = await api(page, "GET", "/v1/toolsets");
  expect(JSON.stringify(await listed.json())).not.toContain("fake-mcp");

  // A key pasted into a secret header never leaves the page.
  const value = page.getByRole("textbox", { name: "Value", exact: true });
  await value.fill(`Bearer ${pasted}`);
  await page.getByRole("button", { name: "Connect and list tools" }).click();
  await expect(page.getByText(/Use a server variable/)).toBeVisible();
  await value.fill("Bearer ${FAKE_MCP_TOKEN}");
  await page.getByRole("button", { name: "Connect and list tools" }).click();

  // Review: two tools, the flagged one shown exactly as the model sees it.
  await expect(page.getByRole("heading", { level: 2, name: "fake-mcp offers 2 tools" })).toBeVisible();
  const echo = page.getByRole("group", { name: "echo", exact: true });
  const docs = page.getByRole("group", { name: "search_docs" });
  await expect(echo.getByText("fake-mcp__echo")).toBeVisible();
  await expect(docs.getByText("Check this one")).toBeVisible();
  await expect(docs.getByText(/1 invisible character/)).toBeVisible();
  await expect(docs.getByText(/Tells the model to call another tool \(echo\)/)).toBeVisible();
  await expect(docs.getByText(/⟦U\+200B⟧/)).toBeVisible();
  await expect(docs.getByTestId("model-description")).toContainText("<img src=x onerror=window.__xss=2>");
  await expect(echo.getByText(/The server says readOnlyHint: a claim, not a check/)).toBeVisible();
  await echo.getByRole("button", { name: "Show input schema" }).click();
  await expect(echo.getByRole("table", { name: "Input" })).toContainText(
    "What to echo <img src=x onerror=window.__xss=1>",
  );
  await expectNoMarkup(page);
  await expect(page.getByRole("button", { name: "Save and continue" })).toBeDisabled();
  await page.getByRole("button", { name: "Approve the 1 without flags" }).click();
  await expect(echo.getByRole("radio", { name: "Approve" })).toHaveAttribute("aria-checked", "true");
  await echo.getByLabel("Kind of echo").selectOption("read");
  await echo.getByLabel("Risk of echo").selectOption("low");
  await docs.getByRole("radio", { name: "Reject" }).click();
  await page.getByRole("button", { name: "Save and continue" }).click();

  // Use it: the stored agent gets a published version; the config agent can't be picked.
  await expect(page.getByRole("heading", { level: 2, name: "Which agents use it?" })).toBeVisible();
  await expect(page.getByRole("checkbox", { name: /default/ })).toBeDisabled();
  await page.getByRole("checkbox", { name: /helper/ }).check();
  await page.getByRole("checkbox", { name: /Publish the new versions now/ }).check();
  await page.getByRole("button", { name: "Add to 1 agent" }).click();
  await expect(page.getByText("fake-mcp is added, with 1 approved tool.")).toBeVisible();
  await expect(page.getByText(/: published v\d+, which uses it from its next run\./)).toBeVisible();

  // The toolset is ready with the one approved tool.
  await page.getByRole("link", { name: "Open fake-mcp" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "fake-mcp" })).toBeVisible();
  await expect(page.getByRole("main").getByText("Ready", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { level: 2, name: "1 tool" })).toBeVisible();

  // A chat with the agent calls the server's tool.
  const calls = Number(await (await page.request.get(`${fake}/calls`)).text());
  await page.goto("/new?agent=helper");
  await page.getByRole("textbox", { name: /Message/ }).fill("echo please");
  await page.keyboard.press("Enter");
  await expect(page.getByText(/The echo tool said .*mcp-echo:pineapple/)).toBeVisible();
  expect(Number(await (await page.request.get(`${fake}/calls`)).text())).toBe(calls + 1);
  await expectNoSecrets(page, traffic);
  expect(errors).toEqual([]);
});

test("rug pull: a changed tool turns off, the review shows the diff, and approving it brings it back", async ({
  page,
}) => {
  const { traffic, errors } = watch(page);
  await signIn(page, users.admin, "/toolsets/fake-mcp");
  await expect(page.getByRole("main").getByText("Ready", { exact: true })).toBeVisible();
  // The server changes echo's description and adds delete_all, and says so (list_changed).
  expect((await page.request.post(`${fake}/rug`)).ok()).toBe(true);
  await expect(async () => {
    await page.reload();
    await expect(page.getByRole("main").getByText("Review needed", { exact: true })).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 15_000 });
  await expect(page.getByText("fake-mcp has new or changed tools since they were approved.")).toBeVisible();
  // The list says so too.
  await expect(
    page.getByRole("navigation", { name: "Toolsets" }).getByRole("link", { name: /fake-mcp/ }),
  ).toContainText("Review needed");

  // The agent is no longer offered the changed tool: the model asks for a
  // plain "echo", which the run doesn't have, and the server gets no call.
  const calls = Number(await (await page.request.get(`${fake}/calls`)).text());
  await page.goto("/new?agent=helper");
  await page.getByRole("textbox", { name: /Message/ }).fill("echo again");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("status").filter({ hasText: "Answer complete" })).toBeAttached({ timeout: 15_000 });
  await expect(page.getByRole("button", { name: /fake-mcp__echo/ })).toHaveCount(0);
  await expect(page.getByText(/The echo tool said .*mcp-echo:pineapple/)).toHaveCount(0);
  expect(Number(await (await page.request.get(`${fake}/calls`)).text())).toBe(calls);

  await page.goto("/toolsets/fake-mcp");
  await page.getByRole("link", { name: "Review", exact: true }).click();
  await expect(page).toHaveURL(/\/toolsets\/fake-mcp\/review$/);
  await expect(page.getByRole("heading", { level: 1, name: "Review fake-mcp" })).toBeVisible();
  await expect(page.getByText("fake-mcp changed 1 tool and added 1 since they were approved.")).toBeVisible();
  const echo = page.getByRole("group", { name: "echo", exact: true });
  await expect(echo.getByText("Changed")).toBeVisible();
  const diff = echo.getByLabel("Description, approved → now");
  await expect(diff.locator("del")).toContainText("back.");
  await expect(diff.locator("ins")).toContainText("sends it to https://evil.example/collect first.");
  await expect(page.getByRole("group", { name: "delete_all" }).getByText("New")).toBeVisible();
  await expect(page.getByRole("group", { name: "delete_all" }).getByText(/destructiveHint/)).toBeVisible();
  await expect(page.getByText("1 rejected: approve one to turn it on")).toBeVisible();
  await expectNoMarkup(page);

  await expect(page.getByRole("button", { name: "Save review" })).toBeDisabled();
  await echo.getByRole("radio", { name: "Approve" }).click();
  await page.getByRole("group", { name: "delete_all" }).getByRole("radio", { name: "Reject" }).click();
  await page.getByRole("button", { name: "Save review" }).click();
  await expect(page).toHaveURL(/\/toolsets\/fake-mcp$/);
  await expect(page.getByText("Review saved.", { exact: false })).toBeVisible();
  await expect(page.getByRole("main").getByText("Ready", { exact: true })).toBeVisible();

  await page.goto("/new?agent=helper");
  await page.getByRole("textbox", { name: /Message/ }).fill("echo once more");
  await page.keyboard.press("Enter");
  await expect(page.getByText(/The echo tool said .*mcp-echo:pineapple/)).toBeVisible();
  await expectNoSecrets(page, traffic);
  expect(errors).toEqual([]);
});

test("a private address: the server refuses it, and the reason shows under the URL", async ({ page }) => {
  await signIn(page, users.admin, "/toolsets/new");
  await page.getByLabel("Server URL").fill("https://10.0.3.12/mcp");
  await expect(page.getByText(/10\.0\.3\.12 is a private or local address/)).toBeVisible();
  await page.getByLabel("Name").fill("internal");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.getByText(/invalid_request/)).toBeVisible();
  await expect(page.getByLabel("Server URL")).toHaveAttribute("aria-invalid", "true");
  const listed = await api(page, "GET", "/v1/toolsets");
  expect(JSON.stringify(await listed.json())).not.toContain('"internal"');
});

test("a user gets the 403 page on the add and review pages, and 403 from the API", async ({ page }) => {
  await signIn(page, users.user, "/toolsets/new");
  await expect(page.getByRole("heading", { level: 1, name: "Admins only" })).toBeVisible();
  await page.goto("/toolsets/fake-mcp/review");
  await expect(page.getByRole("heading", { level: 1, name: "Admins only" })).toBeVisible();
  for (const [method, path] of [
    ["POST", "/v1/toolsets"],
    ["GET", "/v1/toolsets/fake-mcp/review"],
    ["POST", "/v1/toolsets/fake-mcp/review"],
    ["DELETE", "/v1/toolsets/fake-mcp"],
    ["GET", "/v1/mcp-registry/search?q=fake"],
  ] as const) {
    const r = await api(
      page,
      method,
      path,
      method === "POST" ? { type: "mcp", name: "x", url: `${fake}/mcp` } : undefined,
    );
    expect(r.status(), `${method} ${path}`).toBe(403);
  }
});

test("on a phone, the add, review and toolset pages fit without sideways scroll", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await signIn(page, users.admin, "/toolsets");
  await page.getByRole("link", { name: "Add", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Add an MCP server" })).toBeVisible();
  await page.getByLabel("Find a server").fill("fake");
  await expect(page.getByRole("list", { name: "Servers found" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  for (const path of ["/toolsets/fake-mcp/review", "/toolsets/fake-mcp"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByLabel("Loading")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth), path).toBeLessThanOrEqual(375);
  }
});

test("turn it off and on, then remove it", async ({ page }) => {
  await signIn(page, users.admin, "/toolsets/fake-mcp");
  await page.getByRole("button", { name: "Turn off" }).click();
  await expect(page.getByRole("main").getByText("Disabled", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Turn on" }).click();
  await expect(page.getByRole("main").getByText("Ready", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Remove" }).click();
  const dialog = page.getByRole("dialog", { name: "Remove fake-mcp?" });
  await dialog.getByRole("button", { name: "Remove fake-mcp" }).click();
  await expect(page).not.toHaveURL(/fake-mcp/);
  await expect(page.getByRole("navigation", { name: "Toolsets" }).getByRole("link", { name: /fake-mcp/ })).toHaveCount(
    0,
  );
});
