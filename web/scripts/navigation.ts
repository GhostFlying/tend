/// <reference lib="dom" />
import type { Page } from "playwright";
import { resolve } from "node:path";

export function fixtureRoot(kind: "app" | "cache" = "app"): string {
  const directory = process.env[kind === "app" ? "TEND_LAYOUT_APP_ROOT" : "TEND_LAYOUT_CACHE_DIR"];
  if (!directory) throw new Error("Use scripts/layout-fixture.ts to run an isolated fixture");
  return resolve(directory);
}

export async function navigate(page: Page, path: string) {
  await page.evaluate(
    (target) => (window as unknown as { tendNavigate(target: string): void }).tendNavigate(target),
    path,
  );
  await page.waitForURL((url) => url.pathname + url.search === path);
}
