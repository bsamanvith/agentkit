import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["**/dist", "**/node_modules", "**/test-results", "**/playwright-report"] },
  {
    files: ["**/*.{ts,tsx}"],
    extends: [js.configs.recommended, ...tseslint.configs.strict],
    languageOptions: { globals: { ...globals.browser } },
    plugins: { "react-hooks": reactHooks, "react-refresh": reactRefresh },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
      // Untrusted text is rendered as text (docs/UI.md "Rules"): no raw HTML.
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: "Render untrusted content as text; Markdown goes through the allowlist renderer (docs/UI.md).",
        },
        { selector: "MemberExpression[property.name=/^(innerHTML|outerHTML)$/]", message: "No raw HTML (docs/UI.md)." },
        {
          selector: "MemberExpression[property.value=/^(innerHTML|outerHTML)$/]",
          message: "No raw HTML (docs/UI.md).",
        },
        {
          selector: "CallExpression[callee.property.name=/^(insertAdjacentHTML|createContextualFragment)$/]",
          message: "No raw HTML (docs/UI.md).",
        },
        {
          selector: "MemberExpression[object.name='document'][property.name=/^write(ln)?$/]",
          message: "No raw HTML (docs/UI.md).",
        },
      ],
    },
  },
  // The packages are libraries: their modules export hooks and helpers next
  // to components by design. Fast refresh matters in the apps.
  { files: ["packages/**"], rules: { "react-refresh/only-export-components": "off" } },
  // shadcn/ui components are vendored from the CLI and kept as generated, so
  // that re-adding one is a clean diff. SidebarMenuSkeleton's random width
  // is the one impurity.
  { files: ["packages/ui/src/components/**"], rules: { "react-hooks/purity": "off" } },
  {
    files: ["**/*.test.{ts,tsx}", "**/e2e/**", "**/*.config.ts"],
    languageOptions: { globals: { ...globals.node } },
    rules: { "@typescript-eslint/no-non-null-assertion": "off" },
  },
  { files: ["**/*.{js,mjs}"], extends: [js.configs.recommended], languageOptions: { globals: { ...globals.node } } },
  {
    files: ["apps/console/public/**/*.js"],
    languageOptions: { globals: { ...globals.browser }, sourceType: "script" },
  },
);
