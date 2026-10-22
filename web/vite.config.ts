import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { shellManifest } from "./scripts/shell.ts";

const path = (p: string): string => fileURLToPath(new URL(p, import.meta.url));

export default defineConfig({
  plugins: [react(), shellManifest("sw.js")],
  build: {
    // Embedded into tend-server by internal/webui.
    outDir: path("../internal/webui/dist/app"),
    emptyOutDir: true,
    rollupOptions: {
      input: { index: path("index.html"), sw: path("src/sw/sw.ts") },
      output: {
        // The worker keeps a fixed name: the browser checks it for updates.
        entryFileNames: (chunk) => (chunk.name === "sw" ? "sw.js" : "assets/[name]-[hash].js"),
      },
    },
  },
  server: {
    // `pnpm dev` against a local tend-server (listen + local_owner).
    proxy: { "/api": process.env.TEND_DEV_SERVER ?? "http://127.0.0.1:8640" },
  },
  test: {
    environment: "node",
  },
});
