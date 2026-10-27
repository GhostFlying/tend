// Fixed for this page's lifetime. Another tab changing localStorage must not
// silently retarget an existing queue or a pending control request.
let deployment = "";
export function configureDeployment(id: string): void {
  if (!id || (deployment && deployment !== id)) throw new Error("Tend 实例已变更，请刷新页面");
  deployment = id;
}
export function deploymentStorageName(name: string): string {
  return deployment ? `${name}:deployment:${deployment}` : name;
}

export async function clearOldDeploymentData(previous: string): Promise<void> {
  // A slower old tab may enumerate after a new tab already saved fresh data.
  // Delete only its pinned deployment and legacy unscoped business storage.
  const belongsToPrevious = (name: string) =>
    !name.includes(":deployment:") || (!!previous && name.endsWith(`:deployment:${previous}`));
  for (const getStorage of [() => localStorage, () => sessionStorage]) {
    try {
      const storage = getStorage();
      for (const key of Object.keys(storage)) {
        if (!key.startsWith("tend.") || key === "tend.client_id") continue;
        if (key === "tend.token" || key === "tend.deployment_id") continue;
        if (belongsToPrevious(key)) storage.removeItem(key);
      }
      if ((storage.getItem("tend.deployment_id") ?? "") === previous) {
        storage.removeItem("tend.token");
        storage.removeItem("tend.deployment_id");
      }
    } catch {
      /* Browser policy can make storage inaccessible. */
    }
  }
  try {
    if (typeof indexedDB.databases === "function") {
      for (const db of await indexedDB.databases()) {
        if (
          db.name &&
          belongsToPrevious(db.name) &&
          (db.name === "tend" || db.name.startsWith("tend-") || db.name.startsWith("tend:"))
        )
          indexedDB.deleteDatabase(db.name);
      }
    }
  } catch {
    /* The queue separately reports unavailable persistent storage. */
  }
  try {
    if ("caches" in globalThis) {
      // Build-versioned shell files contain no business data. A same-build
      // instance reset does not reinstall its service worker to refill them.
      for (const key of await caches.keys()) {
        if (key.startsWith("tend") && !key.startsWith("tend-shell-") && belongsToPrevious(key))
          await caches.delete(key);
      }
    }
  } catch {
    /* Cache storage may be disabled independently of localStorage. */
  }
}

// Broadcasts are scoped to their sender's old identity. A delayed A -> B
// notification must never cause a tab already connected to C to erase C.
export function deploymentChangeTarget(message: unknown, current: string): string | null {
  if (typeof message !== "object" || message === null) return null;
  const change = message as { previous?: unknown; deployment?: unknown };
  return change.previous === current &&
    typeof change.deployment === "string" &&
    change.deployment !== "" &&
    change.deployment !== current
    ? change.deployment
    : null;
}
