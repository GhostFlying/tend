// What the service worker does with a request. It caches only the app
// shell (D70): the API always goes to the network, so the ledger is never
// read from a cache.

/** The shell files of one build, which the build writes into sw.js. */
export interface ShellManifest {
  version: string;
  /** Paths from the site root, such as "index.html" and "assets/index-1a2b.js". */
  files: string[];
}

export const cachePrefix = "tend-shell-";

/**
 * network: not the worker's business.
 * page: a navigation; the network first, the cached shell when offline.
 * shell: a file of the shell; the cache first.
 */
export type Route = "network" | "page" | "shell";

export interface RequestFacts {
  method: string;
  url: string;
  mode: string;
}

export function route(req: RequestFacts, origin: string, shell: ReadonlySet<string>): Route {
  if (req.method !== "GET") {
    return "network";
  }
  const url = new URL(req.url);
  if (url.origin !== origin) {
    return "network";
  }
  const path = url.pathname;
  if (path === "/api" || path.startsWith("/api/") || path.startsWith("/runner/")) {
    return "network";
  }
  if (req.mode === "navigate") {
    return "page";
  }
  return shell.has(path) ? "shell" : "network";
}

/** The cache names a worker of this version removes on activation. */
export function staleCaches(names: readonly string[], version: string): string[] {
  return names.filter((n) => n.startsWith(cachePrefix) && n !== cachePrefix + version);
}
