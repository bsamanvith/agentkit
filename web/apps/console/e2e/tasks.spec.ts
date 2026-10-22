import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { tasksModelPort, tasksPort, token } from "./token";

// Tasks (T074) against its own server (e2e/tasks.json: boss, which may
// message worker) and fake model. The tests run in order: the first holds an
// answer and releases it; the last holds one it never releases.

const base = `http://127.0.0.1:${tasksPort}`;
const model = `http://127.0.0.1:${tasksModelPort}`;
const auth = { Authorization: `Bearer ${token}` };

test.use({ baseURL: base });
test.describe.configure({ mode: "serial" });

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("status of 401")) errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

const signIn = (page: Page) => page.addInitScript((t) => sessionStorage.setItem("agentkit.token", t), token);

async function startTask(request: APIRequestContext, input: string): Promise<string> {
  const resp = await request.post(`${base}/v1/tasks`, { headers: auth, data: { input, agent: "boss" } });
  expect(resp.status()).toBe(202);
  return ((await resp.json()) as { id: string }).id;
}

async function runStatus(request: APIRequestContext, id: string): Promise<string> {
  const resp = await request.get(`${base}/v1/runs/${id}`, { headers: auth });
  return ((await resp.json()) as { status: string }).status;
}

/** How many answers the fake model holds. */
async function held(request: APIRequestContext): Promise<number> {
  return ((await (await request.get(`${model}/gates`)).json()) as { waiting: number }).waiting;
}

const runRow = (page: Page, id: string) =>
  page.getByRole("navigation", { name: "Tasks" }).locator(`li[data-run="${id}"]`);

test("a background run outlives the page: reopened, it is still listed, and its detail replays and follows the stream", async ({
  page,
  context,
  request,
}) => {
  const errors = watchErrors(page);
  await signIn(page);
  const id = await startTask(request, "Run in the background: echo something");
  // Its first turn (the echo) is done and the answer is held.
  await expect.poll(() => held(request)).toBe(1);

  await page.goto("/tasks");
  await expect(runRow(page, id)).toContainText("Running");
  await runRow(page, id).getByRole("link").click();
  await expect(page.getByRole("heading", { level: 1, name: "Run in the background: echo something" })).toBeVisible();
  const log = page.getByRole("log");
  await expect(log.getByRole("button", { name: /echo/i })).toBeVisible();
  await page.close();

  // A new page: the run goes on without it.
  const again = await context.newPage();
  const errors2 = watchErrors(again);
  await signIn(again);
  await again.goto("/tasks");
  await expect(runRow(again, id)).toContainText("Running");
  expect(await runStatus(request, id)).toBe("running");
  await runRow(again, id).getByRole("link").click();
  const log2 = again.getByRole("log");
  // Replayed: the stored turn's step.
  await expect(log2.getByRole("button", { name: /echo/i })).toBeVisible();
  await expect(again.getByRole("button", { name: "Cancel…", exact: true })).toBeVisible();
  // Followed: the held answer arrives live, and the run ends.
  await request.get(`${model}/release`);
  await expect(log2.getByText("Background work done", { exact: false })).toBeVisible();
  await expect(again.getByRole("button", { name: "Cancel…", exact: true })).toHaveCount(0);
  await expect(again.locator("main").getByText("Done", { exact: true }).first()).toBeVisible();
  await expect(runRow(again, id)).toContainText("Done");
  expect(await runStatus(request, id)).toBe("done");
  expect([...errors, ...errors2]).toEqual([]);
});

test("a thread between two agents shows both sides in order, with links to their runs", async ({ page, request }) => {
  const errors = watchErrors(page);
  await signIn(page);
  const resp = await request.post(`${base}/v1/runs`, {
    headers: { ...auth, Accept: "application/json" },
    data: { message: "Please delegate this", agent: "boss" },
  });
  expect(resp.status()).toBe(200);
  expect(((await resp.json()) as { answer: string }).answer).toContain("42");

  await page.goto("/tasks/threads");
  const row = page.getByRole("navigation", { name: "Threads" }).getByRole("link", { name: /boss ⇄ worker/ });
  await expect(row).toContainText("2 hops");
  await row.click();
  await expect(page.getByRole("heading", { level: 1, name: "boss ⇄ worker" })).toBeVisible();
  const items = page.getByRole("list", { name: "Messages" }).getByRole("listitem");
  await expect(items).toHaveCount(2);
  await expect(items.nth(0)).toHaveAttribute("data-hop", "1");
  await expect(items.nth(0)).toContainText("boss");
  await expect(items.nth(0)).toContainText("What is 6*7?");
  await expect(items.nth(1)).toHaveAttribute("data-hop", "2");
  await expect(items.nth(1)).toContainText("worker");
  await expect(items.nth(1)).toContainText("42");

  // The request was handled by the worker's run, which wrote the reply.
  const handled = items.nth(0).getByRole("link");
  await expect(handled).toHaveCount(1);
  const workerRun = (await handled.textContent()) ?? "";
  await expect(items.nth(1).getByRole("link").first()).toHaveText(workerRun);
  await handled.click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${workerRun}$`));
  await expect(page.getByRole("region", { name: "About this task" })).toContainText("worker");
  // Its answer, as the Result and in the transcript.
  await expect(page.getByRole("region", { name: "Result" })).toContainText("42");
  await expect(page.getByRole("log").getByText("42", { exact: true })).toHaveCount(2);
  expect(errors).toEqual([]);
});

test("Cancel in the list ends the run on the server", async ({ page, request }) => {
  const errors = watchErrors(page);
  await signIn(page);
  const id = await startTask(request, "Run in the background: echo and wait");
  await expect.poll(() => runStatus(request, id)).toBe("running");
  await page.goto("/tasks");
  await expect(runRow(page, id)).toContainText("Running");
  // Cancel… asks first.
  page.once("dialog", (d) => void d.accept());
  await runRow(page, id).getByRole("button", { name: "Cancel task of boss" }).click();
  await expect(runRow(page, id)).toContainText("Canceled");
  await expect(runRow(page, id).getByRole("button")).toHaveCount(0);
  expect(await runStatus(request, id)).toBe("canceled");
  expect(errors).toEqual([]);
});
