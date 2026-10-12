// Writes src/styles/tokens.css from the token block in docs/UI.md ("Tokens"),
// with every custom property prefixed --ak- (shadcn uses some of the same
// names differently). Run it when docs/UI.md's tokens change:
// `pnpm --filter @agentkit/ui tokens`. A test fails while the two differ.
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const uiDoc = fileURLToPath(new URL("../../../../docs/UI.md", import.meta.url));
export const tokensFile = fileURLToPath(new URL("../src/styles/tokens.css", import.meta.url));

/** The tokens.css that docs/UI.md's token block makes. */
export function tokensFromDoc(doc) {
  const section = doc.slice(doc.indexOf("\n## Tokens"));
  const m = section.match(/```css\n([\s\S]*?)```/);
  if (!m) throw new Error("docs/UI.md: no css block under ## Tokens");
  const block = m[1].replace(/--([a-z][a-z0-9-]*)/g, "--ak-$1");
  return (
    '/* Generated from docs/UI.md ("Tokens") by scripts/tokens.mjs. Do not edit:\n' +
    "   change docs/UI.md, then run `pnpm --filter @agentkit/ui tokens`. */\n" +
    block
  );
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  writeFileSync(tokensFile, tokensFromDoc(readFileSync(uiDoc, "utf8")));
}
