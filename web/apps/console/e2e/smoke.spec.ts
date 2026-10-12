import { expect, test, type Page } from "@playwright/test";
import { token } from "./token";

/**
 * Collects console errors, CSP violations among them, and uncaught
 * exceptions. The 401 that opens the token prompt is expected.
 */
function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("status of 401")) errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

const background = (page: Page) => page.evaluate(() => getComputedStyle(document.body).backgroundColor);

test("the shell loads, asks for the token, and sends it to /v1/agents", async ({ page }) => {
  const errors = watchErrors(page);
  await page.goto("/agents");

  await expect(page.getByRole("dialog", { name: "Sign in to agentkit" })).toBeVisible();
  await page.getByLabel("API token").fill(token);
  const agents = page.waitForRequest((r) => new URL(r.url()).pathname === "/v1/agents");
  await page.getByRole("button", { name: "Continue" }).click();
  expect(await (await agents).headerValue("authorization")).toBe(`Bearer ${token}`);

  await expect(page.getByRole("heading", { level: 1, name: "Agents" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Sections" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");
  await expect(page).toHaveTitle("Agents · agentkit");

  // The token lasts for the tab: a reload doesn't ask again.
  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: "Agents" })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("the theme switch pins light and dark, and system follows the OS", async ({ page }) => {
  const errors = watchErrors(page);
  await page.addInitScript((t) => sessionStorage.setItem("agentkit.token", t), token);
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1, name: "New chat" })).toBeVisible();
  const light = await background(page);

  const pick = async (name: string) => {
    await page.getByRole("button", { name: "Account and theme" }).click();
    await page.getByRole("menuitemradio", { name }).click();
    await expect(page.getByRole("menu")).toHaveCount(0);
  };

  await pick("Dark");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  const dark = await background(page);
  expect(dark).not.toBe(light);

  // Kept on reload (theme.js pins it before first paint; see the next test).
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  expect(await background(page)).toBe(dark);

  await pick("Light");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  expect(await background(page)).toBe(light);

  await pick("System");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme");
  expect(await background(page)).toBe(light);
  await page.emulateMedia({ colorScheme: "dark" });
  expect(await background(page)).toBe(dark);
  expect(errors).toEqual([]);
});

test("theme.js pins the theme before the app's script runs", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("agentkit.theme", "dark"));
  await page.emulateMedia({ colorScheme: "light" });
  await page.route("**/assets/*.js", (r) => r.abort()); // no app, only theme.js
  await page.goto("/");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});
