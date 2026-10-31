/// <reference lib="dom" />
import type { Page } from "playwright";

export async function navigate(page: Page, path: string) {
  await page.evaluate(
    (target) => (window as unknown as { tendNavigate(target: string): void }).tendNavigate(target),
    path,
  );
  await page.waitForURL((url) => url.pathname + url.search === path);
}
