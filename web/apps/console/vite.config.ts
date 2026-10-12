import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";

// The build is embedded in the agentkit binary (pkg/web, go:embed) and
// committed, so it must be the same on every machine for the same sources:
// no source maps, no timestamps, hashed names from content only.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: fileURLToPath(new URL("../../../pkg/web/dist", import.meta.url)),
    // Outside the project root, so Vite needs leave to empty it; without it
    // old hashed files would pile up in the commit.
    emptyOutDir: true,
    sourcemap: false,
    // The CSP is default-src 'self': no data: URIs for fonts or images.
    assetsInlineLimit: 0,
    // The polyfill is an inline script, which the CSP blocks; every browser
    // the console supports has modulepreload.
    modulePreload: { polyfill: false },
    reportCompressedSize: false,
    chunkSizeWarningLimit: 1024,
  },
  server: {
    // `pnpm dev` talks to a running `agentkit serve` (AGENTKIT_URL, default :8080).
    proxy: { "/v1": process.env.AGENTKIT_URL ?? "http://127.0.0.1:8080" },
  },
});
