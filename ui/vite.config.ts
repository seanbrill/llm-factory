import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// Vite config runs in Node; declare `process` so tsc/svelte-check don't need
// @types/node just for this one env read.
declare const process: { env: Record<string, string | undefined> };

// During the migration we build to ui/dist and verify it compiles. At cut-over
// (P6) this outDir flips to ../internal/server/web so `go build` embeds it and the
// vanilla files are deleted. Dev runs `vite` (HMR) proxying the API to the Go
// server on :8799, so the new UI can be previewed without touching the live one.
export default defineConfig({
  plugins: [svelte()],
  build: {
    // Output straight into the dir the Go server embeds (//go:embed web). Stable
    // filenames (no content hash) keep git churn down across rebuilds.
    outDir: "../internal/server/web",
    // Clean builds empty the dir; the dev `--watch` MUST NOT (VITE_WATCH=1) — it
    // would briefly leave the dir empty mid-rebuild, and //go:embed (compiled even
    // in dev) races into "cannot embed directory web: contains no embeddable
    // files" when wgo recompiles. Stable filenames make not-emptying safe.
    emptyOutDir: process.env.VITE_WATCH !== "1",
    rollupOptions: {
      output: {
        entryFileNames: "assets/app.js",
        chunkFileNames: "assets/[name].js",
        assetFileNames: "assets/app.[ext]",
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8799",
      "/v1": "http://localhost:8799",
      "/healthz": "http://localhost:8799",
      "/readyz": "http://localhost:8799",
    },
  },
});
