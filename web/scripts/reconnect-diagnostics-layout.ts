/// <reference lib="dom" />
// Real client and input against a loopback SSE fixture. Browser plugin not available.
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { createServer, type ServerResponse } from "node:http";
import { extname, join, resolve } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot } from "./navigation.ts";

const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT;
assert(output, "Set an isolated evidence directory");
await mkdir(output, { recursive: true });

let stream: ServerResponse | undefined;
let traceSeq = 0;
let refusalTrace: string | undefined;
let allowStart = true;
let cursorRefusal: Promise<void> | undefined;
const cursorError = "refresh cursor expired; reload authoritative views and connect without after";
const cursors: (string | null)[] = [];
function start(response: ServerResponse) {
  response.write('event: start\ndata: {"event_seq":1}\n\n');
}
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const path = url.pathname;
  const json = (value: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(value));
  };
  if (path === "/api/v1/session") {
    return json({ token: "fixture", login: "Example owner", deployment_id: "reconnect-fixture" });
  }
  if (path === "/api/v1/events" || path === "/api/v1/messaging/events") {
    if (path === "/api/v1/messaging/events") {
      res.setHeader("Content-Type", "text/event-stream");
      start(res);
      return;
    }
    const after = url.searchParams.get("after");
    cursors.push(after);
    const trace = `trc_${String(++traceSeq).padStart(26, "0")}`;
    res.setHeader("X-Tend-Trace-ID", trace);
    if (cursorRefusal && after !== null) {
      await cursorRefusal;
      refusalTrace = trace;
      return json({ error: { code: "stale_revision", message: cursorError } }, 409);
    }
    res.setHeader("Content-Type", "text/event-stream");
    stream = res;
    res.on("close", () => {
      if (stream === res) stream = undefined;
    });
    if (allowStart) start(res);
    else res.write(": still opening\n\n");
    return;
  }
  if (path === "/api/v1/snapshot") {
    return json({ event_seq: 1, tasks: [], projects: [], hosts: [], queue: [], decisions: [] });
  }
  if (path.startsWith("/api/")) {
    return json({ items: [], more: false, repos: [], enabled: false, revision: 0 });
  }
  try {
    const file = resolve(root, extname(path) ? "." + path : "index.html");
    assert(file.startsWith(root + "/"));
    const types: Record<string, string> = {
      ".js": "text/javascript",
      ".css": "text/css",
      ".svg": "image/svg+xml",
    };
    res.setHeader("Content-Type", types[extname(file)] ?? "text/html");
    res.end(await readFile(file));
  } catch {
    res.writeHead(404).end();
  }
});

function interrupt(reset = false) {
  assert(stream, "the browser must have an established main event stream");
  allowStart = false;
  const previous = stream;
  stream = undefined;
  previous.end(reset ? 'event: reset\ndata: {"event_seq":2}\n\n' : undefined);
  return String(previous.getHeader("X-Tend-Trace-ID"));
}

function recover() {
  allowStart = true;
  if (stream) start(stream);
}

const cases = [
  { name: "desktop", width: 1440, path: "/", touch: false, compact: false },
  { name: "rail", width: 900, path: "/", touch: false, compact: true },
  { name: "mobile-im", width: 320, path: "/", touch: true, compact: true },
  { name: "mobile-chat", width: 390, path: "/diagnostics/main", touch: true, compact: true },
];

await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const origin = `http://127.0.0.1:${address.port}`;

try {
  const browser = await chromium.launch();
  try {
    console.log(`Chromium ${browser.version()} at ${origin}. Browser plugin not available.`);
    for (const { name, width, path, touch, compact } of cases) {
      const context = await browser.newContext({
        viewport: { width, height: 844 },
        isMobile: touch,
        hasTouch: touch,
        serviceWorkers: "block",
      });
      await context.tracing.start({ screenshots: true, snapshots: true });
      cursors.length = 0;
      try {
        await context.route("**/*", (route) =>
          new URL(route.request().url()).origin === origin ? route.continue() : route.abort(),
        );
        const page = await context.newPage();
        page.setDefaultTimeout(10_000);
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        page.on("console", (message) => {
          if (
            message.type() === "warning" &&
            message.text() === "Service Worker registration blocked by Playwright"
          )
            return;
          if (
            cursorRefusal &&
            message.type() === "error" &&
            message.location().url === origin + "/api/v1/events?projection=chat&after=1" &&
            message.text().includes("409")
          )
            return;
          if (["error", "warning"].includes(message.type())) errors.push(message.text());
        });
        await page.goto(origin + path);
        assert.equal(page.url(), origin + path);
        assert.equal(await page.title(), "tend");
        await page.getByRole("main").getByRole("heading", { level: 1 }).waitFor();
        assert.equal(await page.locator("vite-error-overlay").count(), 0);
        const online = page.getByRole("status", { name: "已连接", exact: true });
        const button = page.getByRole("button", { name: "正在重连", exact: true });
        await online.waitFor();
        const initialStatus = await online.elementHandle();

        const reconnectResponse = page.waitForResponse(
          (res) => new URL(res.url()).pathname === "/api/v1/events",
        );
        void reconnectResponse.catch(() => {});
        let expectedTrace = interrupt();
        await button.waitFor();
        assert.equal((await button.innerText()).trim(), compact ? "" : "正在重连");
        const offline = page.getByRole("status", { name: "正在重连", exact: true });
        assert(await offline.evaluate((node, original) => node === original, initialStatus));
        assert.equal(await offline.textContent(), "正在重连");
        const panel = button.locator("..").locator("[id]");
        const show = async (text = "浏览器观察：事件流已结束") => {
          await panel.getByText(text, { exact: true }).waitFor();
          await panel.getByText(`服务端诊断 ID：${expectedTrace}`, { exact: true }).waitFor();
          assert.equal(await panel.getAttribute("id"), await button.getAttribute("aria-controls"));
          assert.equal(await button.getAttribute("aria-expanded"), "true");
          const box = await panel.boundingBox();
          assert(box && box.x >= 0 && box.x + box.width <= width && box.y + box.height < 844);
        };
        const hidden = async () => {
          await panel.waitFor({ state: "hidden" });
          assert.equal(await button.getAttribute("aria-expanded"), "false");
        };
        await hidden();
        if (touch) {
          const target = await button.boundingBox();
          assert(target && target.width >= 44 && target.height >= 44);
          await button.tap();
          await show();
          await page.screenshot({ path: join(output, `${name}.png`) });
          await button.tap();
          await hidden();
          await button.tap();
          await show();
          await page.touchscreen.tap(8, 210);
          await hidden();
        } else {
          await button.hover();
          await show();
          await panel.hover();
          await show();
          await page.screenshot({ path: join(output, `${name}.png`) });
          await page.keyboard.press("Escape");
          await hidden();
          await page.mouse.move(width - 10, 210);
          await button.hover();
          await show();
          await page.mouse.move(width - 10, 210);
          await hidden();
          for (let i = 0; i < 20; i++) {
            await page.keyboard.press("Tab");
            if (await button.evaluate((el) => el === document.activeElement)) break;
          }
          assert(await button.evaluate((el) => el === document.activeElement));
          await show();
          await page.keyboard.press("Space");
          await hidden();
          assert(await button.evaluate((el) => el === document.activeElement));
          await page.keyboard.press("Space");
          await show();
          await page.keyboard.press("Tab");
          await hidden();
          await page.keyboard.press("Shift+Tab");
          await show();
          await page.keyboard.press("Tab");
          await hidden();
        }

        // The browser got HTTP headers and an SSE heartbeat from the retry.
        // Only a start event brings the connector back online.
        await reconnectResponse;
        if (touch) await button.tap();
        else await button.hover();
        await show();
        assert.deepEqual(cursors, [null, "1"]);

        recover();
        await online.waitFor();
        assert(await online.evaluate((node, original) => node === original, initialStatus));
        assert.equal(await online.textContent(), "已连接");
        await button.waitFor({ state: "hidden" });
        await panel.waitFor({ state: "hidden" });

        if (name === "desktop") {
          let releaseRefusal!: () => void;
          cursorRefusal = new Promise<void>((resolve) => {
            releaseRefusal = resolve;
          });
          const fresh = page.waitForResponse((response) => {
            const url = new URL(response.url());
            return url.pathname === "/api/v1/events" && !url.searchParams.has("after");
          });
          void fresh.catch(() => {});
          try {
            expectedTrace = interrupt();
            await button.hover();
            await show();
          } finally {
            releaseRefusal();
          }
          await fresh;
          assert(refusalTrace);
          expectedTrace = refusalTrace;
          await show(`浏览器观察：${cursorError}`);
          await page.screenshot({ path: join(output, "desktop-cursor-rejected.png") });
          assert.deepEqual(cursors, [null, "1", "1", null]);
          recover();
          await online.waitFor();
          cursorRefusal = undefined;

          expectedTrace = interrupt(true);
          await button.waitFor();
          await page.mouse.move(width - 10, 210);
          await button.hover();
          await show("浏览器观察：服务端要求重新同步游标");
          await page.screenshot({ path: join(output, "desktop-reset.png") });
          recover();
          await online.waitFor();
          assert.equal(cursors.at(-1), null);
        }
        assert.deepEqual(errors, []);
        console.log(
          `${name} ${width}×844 ${path}: connection, diagnostic, dismissal and recovery passed`,
        );
      } finally {
        cursorRefusal = undefined;
        recover();
        await context.tracing.stop({ path: join(output, `${name}-trace.zip`) });
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }
} finally {
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
