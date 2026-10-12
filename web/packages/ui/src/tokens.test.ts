// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
// @ts-expect-error: a plain .mjs script, without types.
import { tokensFile, tokensFromDoc, uiDoc } from "../scripts/tokens.mjs";

const tokens = readFileSync(tokensFile as string, "utf8");
const theme = readFileSync(new URL("./styles/index.css", import.meta.url), "utf8");

describe("tokens", () => {
  it("are docs/UI.md's, prefixed (run `pnpm --filter @agentkit/ui tokens` after changing UI.md)", () => {
    expect(tokens).toBe((tokensFromDoc as (doc: string) => string)(readFileSync(uiDoc as string, "utf8")));
  });

  it("keep no unprefixed custom property that would clash with shadcn's", () => {
    expect(tokens.match(/--(?!ak-)[a-z][a-z0-9-]*\s*:/g)).toBeNull();
  });

  // docs/UI.md "In packages/ui": shadcn's --muted is a background, ours is
  // text; its default muted-foreground failed contrast in v2 (T070).
  it.each([
    ["--muted", "--ak-surface-2"],
    ["--muted-foreground", "--ak-muted"],
    ["--background", "--ak-surface"],
    ["--input", "--ak-control"],
    ["--ring", "--ak-focus"],
    ["--sidebar", "--ak-bg"],
    ["--sidebar-accent", "--ak-select"],
    ["--destructive", "--ak-bad"],
  ])("map shadcn's %s to %s", (shadcn, ours) => {
    expect(theme).toContain(`  ${shadcn}: var(${ours});`);
  });

  it("set the radii directly", () => {
    expect(theme).toContain("--radius-sm: var(--ak-r-sm);");
    expect(theme).toContain("--radius-md: var(--ak-r-md);");
    expect(theme).toContain("--radius-lg: var(--ak-r-lg);");
  });
});
