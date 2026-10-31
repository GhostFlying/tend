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

export async function editIssueFilters(page: Page, edit: () => Promise<void>) {
  await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
  const trigger = page.getByRole("button", { name: "筛选", exact: true });
  const sheet = await trigger.isVisible();
  if (sheet) await trigger.click();
  await edit();
  if (sheet)
    await page
      .getByRole("dialog", { name: "筛选 Issue", exact: true })
      .getByRole("button", { name: "完成", exact: true })
      .click();
}
