import { expect, test, type Page } from "@playwright/test";
import { mcpPort, users } from "./token";

// The agent builder (T077) against the server that keeps stored agents
// (e2e/mcp.json) and the fake model: build an agent in the console with two
// models and a description override, save it, publish it, and chat with it
// on the second model. Its name is its own, so mcp.spec.ts's agents don't move.

const base = `http://127.0.0.1:${mcpPort}`;
test.use({ baseURL: base });

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !/status of 401/.test(m.text())) errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

async function signIn(page: Page, path: string) {
  await page.goto(path);
  const card = page.getByRole("dialog", { name: "Sign in to agentkit" });
  await card.getByLabel("Username").fill(users.admin.username);
  await card.getByLabel("Password").fill(users.admin.password);
  await card.getByRole("button", { name: "Sign in" }).click();
  await expect(card).toHaveCount(0);
}

test("build an agent, publish it, and chat with it on its second model", async ({ page }) => {
  const errors = watchErrors(page);
  const name = `scout-${Date.now().toString(36)}`;
  await signIn(page, "/agents/new");
  const form = page.getByRole("form", { name: "New agent" });
  await expect(form).toBeVisible();
  await form.getByRole("textbox", { name: "Name" }).fill(name);
  await form.getByRole("textbox", { name: "Description" }).fill("Built in the console.");
  await form.getByRole("combobox", { name: "Default model" }).fill("claude-haiku-5-5");
  await form.getByRole("combobox", { name: "Also allowed" }).fill("claude-sonnet-5-5");
  await form.getByRole("combobox", { name: "Also allowed" }).press("Enter");
  await form.getByRole("textbox", { name: "System prompt" }).fill("Answer plainly.");
  await form.getByRole("checkbox", { name: /^demo/ }).check();
  await form.getByRole("button", { name: "Override the description of echo" }).click();
  await form.getByRole("textbox", { name: "The description echo gets" }).fill("Echo what the person says.");

  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(page.getByRole("heading", { level: 1, name: `Edit ${name}` })).toBeVisible();
  await expect(page.getByText("Not published · draft v1")).toBeVisible();

  await page.getByRole("button", { name: "Publish…" }).click();
  const dialog = page.getByRole("dialog", { name: `Publish v1 of ${name}?` });
  await dialog.getByRole("button", { name: "Publish v1" }).click();
  await expect(page.getByText("Published v1.", { exact: true })).toBeVisible();

  // The server has the definition built, and the agent tells its tools so.
  const got = await (await page.request.get(`${base}/v1/agents/${name}`)).json();
  expect(got.models.allow).toEqual(["anthropic/claude-haiku-5-5", "anthropic/claude-sonnet-5-5"]);
  expect(got.tool_descriptions).toEqual({ echo: "Echo what the person says." });

  // Its versions page lists v1, published.
  await page.getByRole("link", { name: "Versions" }).click();
  await expect(
    page.getByRole("navigation", { name: "Versions" }).getByRole("link", { name: /v1\s*Published/ }),
  ).toBeVisible();

  // A chat with it on the second model.
  await page.goto(`/new?agent=${name}`);
  await page.getByRole("button", { name: "Model: claude-haiku-5-5" }).click();
  await page.getByRole("menuitemradio", { name: /claude-sonnet-5-5/ }).click();
  await page.getByRole("textbox", { name: /Message/ }).fill("Which model");
  await page.keyboard.press("Enter");
  const thread = page.getByRole("log");
  await expect(thread.getByText("You said: Which model", { exact: false })).toBeVisible();
  await expect(thread.getByText(/ · claude-sonnet-5-5$/)).toBeVisible();
  expect(errors).toEqual([]);
});
