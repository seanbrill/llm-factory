import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

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
    emptyOutDir: true,
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
