/// <reference lib="dom" />
// Real client against loopback-only API fixtures. Browser plugin not available.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { extname, resolve, join } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot, navigate } from "./navigation.ts";

const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT;
assert(output, "Set an isolated screenshot output directory");
await mkdir(output, { recursive: true });
let count = 0;
let more = false;
const server = createServer(async (req, res) => {
  const path = new URL(req.url!, "http://localhost").pathname;
  const json = (value: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(value));
  };
  if (req.method !== "GET" && path !== "/api/v1/session") {
    res.writeHead(405).end();
    return;
  }
  if (path === "/api/v1/session")
    return json({
      token: "fixture",
      login: "Example owner",
      scope: "owner",
      deployment_id: "badge",
    });
  if (path === "/api/v1/inbox")
    return json({
      items: Array.from({ length: count }, (_, i) => ({
        attention_id: `att_fixture_${i}`,
        kind: "action",
        status: "open",
        title: "待处理示例",
        created_at: 1,
        payload: {},
        responsibility: { recipient_kind: "owner" },
      })),
      more,
    });
  if (path === "/api/v1/events") {
    res.setHeader("Content-Type", "text/event-stream");
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    return;
  }
  if (path === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], projects: [], hosts: [], queue: [], decisions: [] });
  if (path.startsWith("/api/"))
    return json({ items: [], more: false, repos: [], enabled: false, revision: 0 });
  try {
    const file = resolve(root, extname(path) ? "." + path : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "Content-Type",
      (
        { ".js": "text/javascript", ".css": "text/css", ".json": "application/json" } as Record<
          string,
          string
        >
      )[extname(file)] ?? "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
const browser = await chromium.launch({ headless: true });
const results: unknown[] = [];
try {
  for (const width of [320, 390]) {
    for (const colorScheme of ["light", "dark"] as const) {
      for (const value of [0, 1, 2, 10, 99, 100, 1000]) {
        count = value;
        more = value === 100;
        const context = await browser.newContext({
          viewport: { width, height: 844 },
          isMobile: true,
          hasTouch: true,
          colorScheme,
          serviceWorkers: "block",
        });
        try {
          const page = await context.newPage();
          page.setDefaultTimeout(10_000);
          const errors: string[] = [];
          page.on("pageerror", (error) => errors.push(error.message));
          await context.route("**/*", (route) => {
            if (new URL(route.request().url()).origin !== origin) return route.abort();
            return route.continue();
          });
          await page.goto(origin + "/issues");
          assert.equal(await page.title(), "tend");
          const nav = page.getByRole("navigation", { name: "视图", exact: true });
          const tab = nav.getByRole("link", { name: "收件箱", exact: true });
          const description = `${count}${more ? "+" : ""} 项待处理`;
          await page.waitForFunction(
            (description) =>
              document
                .querySelector('nav[aria-label="视图"] a[href="/inbox"]')
                ?.getAttribute("aria-description") === description,
            description,
          );
          const geometry = await tab.evaluate((element) => {
            const rect = (e: Element) => {
              const r = e.getBoundingClientRect();
              return {
                left: r.left,
                right: r.right,
                top: r.top,
                bottom: r.bottom,
                height: r.height,
              };
            };
            const badge =
              element.querySelector('[data-badge="pending"]') ??
              element.querySelector("span:not(:has(svg))");
            const icon = element.querySelector("svg")!;
            return {
              tab: rect(element),
              icon: rect(icon),
              badge: badge ? rect(badge) : null,
              text: badge?.textContent,
              lineHeight: badge ? getComputedStyle(badge).lineHeight : null,
              scrollWidth: document.documentElement.scrollWidth,
              width: innerWidth,
            };
          });
          await writeFile(
            join(output, `${width}-${colorScheme}-${count}.json`),
            JSON.stringify(geometry, null, 2),
          );
          await page.screenshot({ path: join(output, `${width}-${colorScheme}-${count}.png`) });
          assert(geometry.tab.height >= 44, "whole navigation target remains touch sized");
          assert.equal(geometry.scrollWidth, geometry.width);
          if (count) {
            assert(geometry.badge);
            assert(geometry.badge.left >= geometry.icon.right, "badge must not cover the icon");
            assert(
              geometry.badge.right <= geometry.tab.right,
              "badge must fit its navigation target",
            );
            assert(geometry.badge.top >= geometry.tab.top, "badge must remain inside the tab bar");
            assert.equal(geometry.text, count > 99 ? "99+" : `${count}${more ? "+" : ""}`);
          } else assert.equal(geometry.badge, null);
          for (const target of ["svg", ...(count ? ['[data-badge="pending"]'] : [])]) {
            await tab.locator(target).click();
            await page.waitForURL(origin + "/inbox");
            await page.getByRole("heading", { name: "收件箱", exact: true }).waitFor();
            assert.equal(await tab.getAttribute("aria-current"), "page");
            assert.equal(await tab.getAttribute("aria-description"), description);
            await navigate(page, "/issues");
          }
          await tab.click();
          await page.waitForURL(origin + "/inbox");
          let scrollProof: { before: number; after: number } | null = null;
          if (count >= 99) {
            await page
              .getByText("待处理示例", { exact: true })
              .filter({ visible: true })
              .first()
              .waitFor();
            const beforeScroll = await page.screenshot({
              path: join(output, `${width}-${colorScheme}-${count}-before-scroll.png`),
            });
            const scroll = await page.locator("*").evaluateAll((elements) => {
              const target = elements.find(
                (e) =>
                  e.clientHeight > 0 &&
                  e.getClientRects().length > 0 &&
                  e.scrollHeight > e.clientHeight + 50,
              );
              if (!target) return null;
              const before = target.scrollTop;
              target.scrollTop = target.scrollHeight;
              return { before, after: target.scrollTop };
            });
            assert(scroll && scroll.after > scroll.before, "exercise real content scroll");
            scrollProof = scroll;
            const afterScroll = await page.screenshot({
              path: join(output, `${width}-${colorScheme}-${count}-scrolled.png`),
            });
            assert(!beforeScroll.equals(afterScroll), "scroll changes visible content");
            await tab.click();
          }
          assert.deepEqual(errors, []);
          results.push({ width, colorScheme, count, more, description, geometry, scrollProof });
        } finally {
          await context.close();
        }
      }
    }
  }
  await writeFile(join(output, "measurements.json"), JSON.stringify(results, null, 2));
  console.log("PASS inbox badge geometry, counts, themes, scroll and navigation");
} finally {
  await browser.close();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
