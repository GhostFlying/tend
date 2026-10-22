// A Vite plugin that writes the app shell's file list into the service
// worker, so the worker caches exactly the files of its own build.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import type { Plugin, ResolvedConfig } from "vite";

const placeholder = /(["'`])__TEND_SHELL_MANIFEST__\1/;

function publicFiles(dir: string): string[] {
  try {
    return readdirSync(dir, { recursive: true, withFileTypes: true })
      .filter((e) => e.isFile())
      .map((e) => relative(dir, join(e.parentPath, e.name)).split(sep).join("/"));
  } catch {
    return [];
  }
}

/** worker is the output file name of the service worker's entry. */
export function shellManifest(worker: string): Plugin {
  let config: ResolvedConfig;
  return {
    name: "tend-shell-manifest",
    apply: "build",
    // After Vite has added index.html to the bundle.
    enforce: "post",
    configResolved(resolved) {
      config = resolved;
    },
    generateBundle(_options, bundle) {
      const chunk = bundle[worker];
      if (!chunk || chunk.type !== "chunk") {
        this.error(`no ${worker} in the bundle`);
      }
      const hash = createHash("sha256");
      const files = new Map<string, string | Uint8Array>();
      for (const [name, out] of Object.entries(bundle)) {
        if (name !== worker && !name.endsWith(".map")) {
          files.set(name, out.type === "chunk" ? out.code : out.source);
        }
      }
      if (!files.has("index.html")) {
        // The offline page; its absence means the plugin ran too early.
        this.error("index.html is not in the bundle");
      }
      for (const name of publicFiles(config.publicDir)) {
        files.set(name, readFileSync(join(config.publicDir, name)));
      }
      const names = [...files.keys()].sort();
      for (const name of names) {
        hash.update(name).update("\0").update(files.get(name)!).update("\0");
      }
      const manifest = { version: hash.digest("hex").slice(0, 16), files: names };
      if (!placeholder.test(chunk.code)) {
        this.error(`${worker} has no shell manifest placeholder`);
      }
      // A string literal holding the JSON, which the worker parses.
      chunk.code = chunk.code.replace(placeholder, () => JSON.stringify(JSON.stringify(manifest)));
    },
  };
}
