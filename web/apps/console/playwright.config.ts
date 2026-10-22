// The console's browser tests, against the real binary: agentkit serve with
// the agent and web components (make web-smoke builds it and sets
// AGENTKIT_BIN), and a fake model (e2e/fake-model.ts) in place of the API.
//
// Browsers are never downloaded (no `playwright install`): CI uses the
// runner's Google Chrome; locally, the Chromium Playwright finds in
// PLAYWRIGHT_BROWSERS_PATH or its cache.
import { defineConfig } from "@playwright/test";
import {
  adminPort,
  modelPort,
  providerCanary,
  providerPort,
  serverKeyCanary,
  tasksModelPort,
  tasksPort,
  token,
} from "./e2e/token";

const port = 18478;
const bin = process.env.AGENTKIT_BIN;
if (!bin) throw new Error("set AGENTKIT_BIN to an agentkit binary (make web-smoke does)");

export default defineConfig({
  testDir: "e2e",
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    channel: process.env.CI ? "chrome" : undefined,
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: `node e2e/fake-model.ts ${modelPort}`,
      url: `http://127.0.0.1:${modelPort}/healthz`,
      reuseExistingServer: false,
      timeout: 10_000,
    },
    {
      command: `"${bin}" serve`,
      url: `http://127.0.0.1:${port}/readyz`,
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "pipe",
      env: {
        AGENTKIT_ADDR: `127.0.0.1:${port}`,
        AGENTKIT_COMPONENTS: "agent,web",
        AGENTKIT_CONFIG: "e2e/agentkit.json",
        AGENTKIT_API_TOKEN: token,
        ANTHROPIC_API_KEY: "smoke-dummy-key",
        ANTHROPIC_BASE_URL: `http://127.0.0.1:${modelPort}`,
      },
    },
    // The Tasks tests' (e2e/tasks.spec.ts) own model and server.
    {
      command: `node e2e/fake-model.ts ${tasksModelPort}`,
      url: `http://127.0.0.1:${tasksModelPort}/healthz`,
      reuseExistingServer: false,
      timeout: 10_000,
    },
    {
      command: `"${bin}" serve`,
      url: `http://127.0.0.1:${tasksPort}/readyz`,
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "pipe",
      env: {
        AGENTKIT_ADDR: `127.0.0.1:${tasksPort}`,
        AGENTKIT_COMPONENTS: "agent,web",
        AGENTKIT_CONFIG: "e2e/tasks.json",
        AGENTKIT_API_TOKEN: token,
        ANTHROPIC_API_KEY: "smoke-dummy-key",
        ANTHROPIC_BASE_URL: `http://127.0.0.1:${tasksModelPort}`,
      },
    },
    // The admin pages' tests (e2e/admin.spec.ts): a fake OAuth2 provider and
    // a server with local users, a credential vault and the MCP server.
    {
      command: `node e2e/fake-provider.ts ${providerPort} ${providerCanary}`,
      url: `http://127.0.0.1:${providerPort}/healthz`,
      reuseExistingServer: false,
      timeout: 10_000,
    },
    {
      command: `"${bin}" serve`,
      url: `http://127.0.0.1:${adminPort}/readyz`,
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "pipe",
      env: {
        AGENTKIT_ADDR: `127.0.0.1:${adminPort}`,
        AGENTKIT_COMPONENTS: "agent,web,mcp-server",
        AGENTKIT_CONFIG: "e2e/admin.json",
        // The vault's key ring, for these tests only (the memory backend: nothing is kept).
        AGENTKIT_CREDENTIALS_KEY: "e2e:ZioRPt9Lvn72IhkndjYpyWCJUjzmiBUs1HX8fZGrYZc=",
        MAIL_CLIENT_SECRET: "e2e-mail-client-secret",
        EXA_API_KEY: serverKeyCanary,
        ANTHROPIC_API_KEY: "smoke-dummy-key",
        ANTHROPIC_BASE_URL: `http://127.0.0.1:${modelPort}`,
      },
    },
  ],
});
