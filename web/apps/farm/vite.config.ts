import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";

// The build is embedded in the farm binary (apps/farm/webdist, go:embed) and
// committed, so it must be the same on every machine for the same sources:
// no source maps, no timestamps, hashed names from content only. The same
// settings as the console's (web/apps/console/vite.config.ts).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: fileURLToPath(new URL("../../../apps/farm/webdist/dist", import.meta.url)),
    emptyOutDir: true,
    sourcemap: false,
    // The CSP allows no data: fonts.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    reportCompressedSize: false,
    chunkSizeWarningLimit: 1024,
  },
  server: {
    // `pnpm dev` talks to a running farm server (FARM_URL, default :8090).
    proxy: { "/api": process.env.FARM_URL ?? "http://127.0.0.1:8090" },
  },
});
