import { expect, test, type Page } from "@playwright/test";
import { modelPort, token } from "./token";

// Agent-made UI (A2UI surfaces, T066) against the real server, whose model
// is e2e/fake-model.ts: "news" shows a form in two ui_render calls and
// answers its Search; "unsafe" shows hostile text and URLs.

const model = `http://127.0.0.1:${modelPort}`;

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

const signIn = (page: Page) => page.addInitScript((t) => sessionStorage.setItem("agentkit.token", t), token);
const box = (page: Page) => page.getByRole("textbox", { name: "Message" });
const surface = (page: Page, id: string) => page.locator(`[data-surface-id="${id}"]`);

async function send(page: Page, text: string) {
  await box(page).fill(text);
  await box(page).press("Enter");
}

async function ended(page: Page) {
  await expect(page.getByRole("button", { name: "Stop" })).toHaveCount(0);
  await expect(page.locator('[data-role="assistant"]').last()).toHaveAttribute("aria-busy", "false");
}

/** Waits until the fake model holds an answer, then lets it go. */
async function release(page: Page) {
  await expect
    .poll(async () => ((await (await page.request.get(`${model}/gates`)).json()) as { waiting: number }).waiting)
    .toBe(1);
  await page.request.get(`${model}/release`);
}

test("a surface streams in, updates in place, sends its values and shows the agent's reply", async ({ page }) => {
  const errors = watchErrors(page);
  await signIn(page);
  await page.goto("/new");
  await send(page, "Narrow the news search slowly");
  await expect(page).toHaveURL(/\/c\/[^/]+$/);

  // Progressive: the first call's card shows while the run goes on.
  const news = surface(page, "news");
  await expect(news.getByRole("heading", { name: "Narrow the news search" })).toBeVisible();
  await expect(news.getByText("Loading the options…")).toBeVisible();
  await expect(page.getByRole("button", { name: "Stop" })).toBeVisible();
  // It waits while the run goes on.
  await expect(news).toHaveAttribute("data-state", "busy");
  // ui_render is the surface, not a tool step.
  await expect(page.getByRole("log").getByRole("button", { name: /ui_render/ })).toHaveCount(0);
  const node = await news.elementHandle();

  // The second call fills the same surface: the same element, now the form.
  await release(page);
  const topic = news.getByRole("textbox", { name: "Topic" });
  await expect(topic).toBeVisible();
  await expect(news.getByText("Loading the options…")).toHaveCount(0);
  expect(await node?.evaluate((n) => n.isConnected)).toBe(true);
  await ended(page);
  await expect(page.getByText("Pick a topic and the sources, then Search.")).toBeVisible();

  // The data model set the chips; the person's edits are sent with the action.
  await expect(news.getByRole("button", { name: "News sites" })).toHaveAttribute("aria-pressed", "true");
  await topic.fill("go 1.27");
  await news.getByRole("button", { name: "Blogs" }).click();
  await news.getByRole("checkbox", { name: "Past week only" }).check();
  const posted = page.waitForRequest((r) => r.method() === "POST" && new URL(r.url()).pathname === "/v1/runs");
  await news.getByRole("button", { name: "Search" }).click();
  const body = (await posted).postDataJSON() as Record<string, unknown>;
  expect(body.message).toBeUndefined();
  expect(body.ui_action).toEqual({
    surface_id: "news",
    component_id: "search",
    action: "search",
    context: { topic: "go 1.27", sources: ["news", "blogs"], recent: true },
  });

  // Sent, and disabled until the agent replies.
  await expect(page.locator('[data-role="user"]').last()).toHaveText("Sent “Search”");
  await expect(news).toHaveAttribute("data-state", "sent");
  await expect(news.getByText("Sent", { exact: true })).toBeVisible();
  await expect(topic).toBeDisabled();
  await expect(topic).toHaveValue("go 1.27");
  await expect(news.getByRole("button", { name: "Search" })).toBeDisabled();

  // The reply changes the surface in place, then says what was asked.
  await release(page);
  await expect(news.getByRole("heading", { name: "Searching for “go 1.27”" })).toBeVisible();
  await expect(page.getByText("Searching news and blogs for “go 1.27” from the past week.")).toBeVisible();
  await ended(page);
  await expect(news).toHaveAttribute("data-state", "live");
  await expect(surface(page, "news")).toHaveCount(1);

  // From history after a reload: the surface as the agent last drew it, and the action as a message.
  await page.reload();
  await expect(news.getByRole("heading", { name: "Searching for “go 1.27”" })).toBeVisible();
  await expect(page.locator('[data-role="user"]')).toHaveText(["Narrow the news search slowly", "Sent “Search”"]);
  await expect(page.getByText("Searching news and blogs for “go 1.27” from the past week.")).toBeVisible();
  expect(errors).toEqual([]);
});

test("a surface's markup is text, and unsafe or outside URLs are dropped", async ({ page }) => {
  const errors = watchErrors(page);
  await signIn(page);
  const before = ((await (await page.request.get(`${model}/hooks`)).json()) as { hooks: number }).hooks;
  await page.goto("/new");
  await send(page, "Show me something unsafe");
  const s = surface(page, "unsafe");
  await expect(s.getByText('<img src=x onerror="window.__xss=7">', { exact: false })).toBeVisible();
  await ended(page);

  for (const pass of ["live", "history"]) {
    if (pass === "history") await page.reload();
    await expect(s.getByText("<script>window.__xss=8</script> **not bold**", { exact: false })).toBeVisible();
    await expect(s.locator("img, script, iframe, strong")).toHaveCount(0);
    await expect(s.getByText("Image not shown: A tracking pixel")).toBeVisible();
    // The javascript: link is no link, and can't be pressed.
    await expect(s.getByRole("button", { name: "Open the page" })).toBeDisabled();
    await expect(s.getByText("Link removed: not a web address.")).toBeVisible();
    const hrefs = await s.locator("a").evaluateAll((as) => as.map((a) => a.getAttribute("href")));
    expect(hrefs).toEqual(["https://example.com/docs"]);
    await expect(s.getByRole("link", { name: "Read the docs" })).toHaveAttribute("rel", "noopener noreferrer");
    expect(await page.evaluate(() => (window as { __xss?: unknown }).__xss)).toBeUndefined();
  }
  const after = ((await (await page.request.get(`${model}/hooks`)).json()) as { hooks: number }).hooks;
  expect(after).toBe(before);
  expect(errors).toEqual([]);
});

for (const theme of ["light", "dark"] as const) {
  test(`a surface fits a phone in the ${theme} theme`, async ({ browser }, info) => {
    const context = await browser.newContext({ viewport: { width: 360, height: 760 }, hasTouch: true, isMobile: true });
    const page = await context.newPage();
    const errors = watchErrors(page);
    await signIn(page);
    await page.addInitScript((t) => localStorage.setItem("agentkit.theme", t), theme);
    await page.goto("/new");
    await send(page, "Narrow the news search");
    const news = surface(page, "news");
    await expect(news.getByRole("textbox", { name: "Topic" })).toBeVisible();
    await ended(page);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);

    // Nothing wider than the screen, and nothing past the thread's edges.
    const widths = await page.evaluate(() => ({
      page: document.documentElement.scrollWidth,
      screen: window.innerWidth,
    }));
    expect(widths.page).toBeLessThanOrEqual(widths.screen);
    const box = await news.boundingBox();
    expect(box && box.x >= 0 && box.x + box.width <= 360).toBe(true);

    // The theme's colors: text against the card has contrast, and dark is dark.
    const colors = await news.evaluate((el) => {
      const card = el.querySelector("div.rounded-xl") as HTMLElement;
      const title = el.querySelector('[role="heading"]') as HTMLElement;
      const input = el.querySelector("input") as HTMLElement;
      const search = [...el.querySelectorAll("button")].find((x) => x.textContent === "Search") as HTMLElement;
      const rgb = (c: string) => (c.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number);
      return {
        card: rgb(getComputedStyle(card).backgroundColor),
        text: rgb(getComputedStyle(title).color),
        input: rgb(getComputedStyle(input).color),
        button: rgb(getComputedStyle(search).backgroundColor),
        label: rgb(getComputedStyle(search.querySelector("div") as HTMLElement).color),
      };
    });
    const lum = ([r = 0, g = 0, b = 0]: number[]) => {
      const f = (v: number) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const contrast = (a: number[], b: number[]) => {
      const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
      return (hi + 0.05) / (lo + 0.05);
    };
    expect(contrast(colors.text, colors.card)).toBeGreaterThan(7);
    expect(contrast(colors.input, colors.card)).toBeGreaterThan(7);
    expect(contrast(colors.label, colors.button)).toBeGreaterThan(7);
    expect(lum(colors.card) < 0.2).toBe(theme === "dark");
    // Targets stay at least 24 px tall.
    for (const b of await news.getByRole("button").all())
      expect((await b.boundingBox())?.height).toBeGreaterThanOrEqual(24);
    await page.screenshot({ path: info.outputPath(`surface-${theme}.png`), fullPage: true });
    expect(errors).toEqual([]);
    await context.close();
  });
}
