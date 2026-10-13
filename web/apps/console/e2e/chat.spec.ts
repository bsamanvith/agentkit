import { expect, test, type Page } from "@playwright/test";
import { modelPort, token } from "./token";

// Chat against the real server, whose model is e2e/fake-model.ts: the last
// message picks the answer ("echo", "xss", "stall", or "You said: …").

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("status of 401")) errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

const signIn = (page: Page) => page.addInitScript((t) => sessionStorage.setItem("agentkit.token", t), token);
const box = (page: Page) => page.getByRole("textbox", { name: "Message" });
const answers = (page: Page) => page.locator('[data-role="assistant"]');

async function send(page: Page, text: string) {
  await box(page).fill(text);
  await box(page).press("Enter");
}

/** Waits for the last answer to end (the composer has Send again). */
async function ended(page: Page) {
  await expect(page.getByRole("button", { name: "Stop" })).toHaveCount(0);
  await expect(answers(page).last()).toHaveAttribute("aria-busy", "false");
}

test("a new chat streams an answer with a tool step, takes a follow-up, and reloads from history", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await signIn(page);
  await page.goto("/new");
  await expect(page.getByRole("heading", { level: 1, name: "New chat" })).toBeVisible();

  await send(page, "Please echo something");
  await expect(page).toHaveURL(/\/c\/[^/]+$/);
  const thread = page.getByRole("log");
  await expect(thread.getByText("The echo tool said", { exact: false })).toBeVisible();
  await expect(thread.getByText("pineapple", { exact: false }).first()).toBeVisible();
  await expect(thread.getByRole("button", { name: /Echo/ })).toBeVisible();
  await ended(page);
  await expect(thread.getByText(/2 turns · [\d,]+ tokens/)).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Please echo something" })).toBeVisible();
  const list = page.getByRole("navigation", { name: "Conversations" });
  await expect(list.getByRole("link", { name: /Please echo something/ })).toHaveAttribute("aria-current", "page");

  await send(page, "And again");
  await expect(thread.getByText("You said: And again. That's 2 messages from you.")).toBeVisible();
  await ended(page);

  // The history has both exchanges and the tool step.
  const url = page.url();
  await page.reload();
  await expect(page).toHaveURL(url);
  await expect(thread.getByText("You said: And again.", { exact: false })).toBeVisible();
  await expect(thread.getByText("The echo tool said", { exact: false })).toBeVisible();
  await expect(thread.getByRole("button", { name: /Echo/ })).toBeVisible();
  await expect(thread.locator('[data-role="user"]')).toHaveCount(2);
  expect(errors).toEqual([]);
});

test("Stop closes the stream and the server cancels the run", async ({ page, request }) => {
  const errors = watchErrors(page);
  await signIn(page);
  await page.goto("/new");
  await send(page, "Please stall");
  await expect(page.getByRole("log").getByText("Working on it")).toBeVisible();
  const runId = await answers(page).last().getAttribute("data-run-id");
  expect(runId).toBeTruthy();
  const before = (await (await request.get(`http://127.0.0.1:${modelPort}/stalls`)).json()) as { ended: number };

  await page.getByRole("button", { name: "Stop" }).click();
  await expect(page.getByText("Stopped. This part isn't saved.")).toBeVisible();
  await expect(box(page)).toBeEnabled();

  // The run is cancelled on the server, and its model request ended.
  await expect
    .poll(async () => {
      const r = await request.get(`/v1/runs/${runId}`, { headers: { Authorization: `Bearer ${token}` } });
      const run = (await r.json()) as { status: string; error?: { code: string } };
      return `${run.status}/${run.error?.code ?? ""}`;
    })
    .toBe("failed/cancelled");
  await expect
    .poll(async () => ((await (await request.get(`http://127.0.0.1:${modelPort}/stalls`)).json()) as { ended: number }).ended)
    .toBeGreaterThan(before.ended);
  expect(errors).toEqual([]);
});

test("hostile model output renders as text, under the CSP", async ({ page }) => {
  const errors = watchErrors(page);
  await signIn(page);
  const doc = await page.goto("/new");
  expect(doc?.headers()["content-security-policy"]).toContain("default-src 'self'");
  await send(page, "Say something xss");
  const thread = page.getByRole("log");
  await expect(thread.getByText("Hostile:", { exact: false })).toBeVisible();
  await ended(page);

  await expect(thread.locator("img, script, iframe")).toHaveCount(0);
  await expect(thread.getByText('<img src=x onerror="window.__xss=1">', { exact: false })).toBeVisible();
  // The javascript: link isn't a link.
  for (const href of await thread.locator("a").evaluateAll((as) => as.map((a) => a.getAttribute("href") ?? ""))) {
    expect(href).not.toMatch(/^javascript:/i);
  }
  await expect(thread.locator("strong", { hasText: "bold" })).toBeVisible();
  expect(await page.evaluate(() => (window as { __xss?: unknown }).__xss)).toBeUndefined();

  // The same after a reload, from history.
  await page.reload();
  await expect(thread.getByText("Hostile:", { exact: false })).toBeVisible();
  await expect(thread.locator("img, script, iframe")).toHaveCount(0);
  expect(await page.evaluate(() => (window as { __xss?: unknown }).__xss)).toBeUndefined();
  expect(errors).toEqual([]);
});

test("with no token or a wrong one, the page asks for it and doesn't loop", async ({ page }) => {
  const calls: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.startsWith("/v1/")) calls.push(new URL(r.url()).pathname);
  });
  await page.addInitScript(() => sessionStorage.setItem("agentkit.token", "wrong-token"));
  await page.goto("/new");
  const prompt = page.getByRole("dialog", { name: "Sign in to agentkit" });
  await expect(prompt).toBeVisible();
  await page.waitForTimeout(1000);
  const seen = calls.length;
  expect(seen).toBeLessThan(10);
  await page.waitForTimeout(1000);
  expect(calls.length).toBe(seen); // no retry loop behind the prompt
  // The token lives in sessionStorage only, and a bad one is forgotten.
  expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.includes("token")))).toEqual([]);

  await page.getByLabel("API token").fill(token);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(prompt).toHaveCount(0);
  await expect(box(page)).toBeVisible();
});
