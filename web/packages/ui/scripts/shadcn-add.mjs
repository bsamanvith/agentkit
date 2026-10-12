// Adds shadcn/ui components to this package: `pnpm --filter @agentkit/ui add-component button`.
//
// shadcn 4.21 rewrites the utils alias of a workspace package
// (@agentkit/ui/lib/utils) to the npm package "cn", and adds that package as
// a dependency. This runs the CLI, then puts the import back and drops the
// dependency.
import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const names = process.argv.slice(2);
if (names.length === 0) {
  console.error("usage: pnpm --filter @agentkit/ui add-component <component>...");
  process.exit(2);
}
execFileSync("pnpm", ["dlx", "shadcn@4.21.4", "add", "-y", ...names], { stdio: "inherit" });

for (const dir of ["src/components", "src/hooks"]) {
  for (const file of readdirSync(dir)) {
    const path = join(dir, file);
    const src = readFileSync(path, "utf8");
    const fixed = src.replaceAll('from "cn"', 'from "@agentkit/ui/lib/utils"');
    if (fixed !== src) writeFileSync(path, fixed);
  }
}
const pkg = JSON.parse(readFileSync("package.json", "utf8"));
if (pkg.dependencies?.cn) {
  delete pkg.dependencies.cn;
  writeFileSync("package.json", JSON.stringify(pkg, null, 2) + "\n");
  execFileSync("pnpm", ["install"], { stdio: "inherit" });
}
console.log("Now run `pnpm format` and review the new files (docs/UI.md says which variants we use).");
