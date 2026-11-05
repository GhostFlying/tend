/// <reference lib="dom" />
// Real retained React views against loopback-only read fixtures.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { extname, join, resolve } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot, navigate } from "./navigation.ts";

const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT;
assert(output, "Set an isolated fixture output directory");
await mkdir(output, { recursive: true });
const calls: string[] = [];
const streams = new Set<ServerResponse>();
let holdUsage = false;
let usageAborted = 0;
let heldUsage: (() => void) | undefined;
const viewOnly = (path: string) =>
  path.startsWith("/api/v1/conversation/queue?") ||
  path.startsWith("/api/v1/conversation/transcript?") ||
  path.startsWith("/api/v1/conversation/settings?") ||
  path === "/api/v1/agent-conversations/main/usage" ||
  path === "/api/v1/agent-conversations/main/compactions";
const count = (path: string) => calls.filter((call) => call.startsWith(path)).length;
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://fixture.invalid");
  const path = url.pathname;
  if (path.startsWith("/api/")) calls.push(url.pathname + url.search);
  const json = (value: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(value));
  };
  if (req.method !== "GET") return res.writeHead(405).end();
  if (path === "/api/v1/session")
    return json({ login: "Fixture owner", scope: "owner", deployment_id: "visibility" });
  if (path === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], projects: [], hosts: [], queue: [], decisions: [] });
  if (path === "/api/v1/inbox")
    return json({
      items: [{ attention_id: "attention", kind: "action", status: "open", title: "Fixture" }],
    });
  if (path === "/api/v1/conversation/settings") return json({ enabled: false, revision: 1 });
  if (path === "/api/v1/conversation/queue")
    return json({ category: url.searchParams.get("category"), items: [], total: 0, more: false });
  if (path === "/api/v1/conversation/transcript") return json({ blocks: [], more: false });
  if (path === "/api/v1/agent-conversations/main/usage") {
    if (holdUsage) {
      res.on("close", () => {
        if (!res.writableEnded) usageAborted++;
      });
      heldUsage?.();
      return;
    }
    return json({ available: true, used_percentage: 42, revision: 1 });
  }
  if (path.startsWith("/api/")) return json({ items: [], more: false, repos: [], revision: 1 });
  try {
    const file = resolve(root, extname(path) ? "." + path : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "Content-Type",
      ({ ".js": "text/javascript", ".css": "text/css" } as Record<string, string>)[extname(file)] ??
        "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
const browser = await chromium.launch({ headless: true });
try {
  const context = await browser.newContext({ serviceWorkers: "block" });
  await context.addInitScript(() => {
    localStorage.setItem("tend.token", "fixture");
    localStorage.setItem("tend.deployment_id", "visibility");
  });
  await context.route("**/*", (route) =>
    new URL(route.request().url()).origin === origin ? route.continue() : route.abort(),
  );
  const page = await context.newPage();
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.clock.install();
  await page.goto(origin + "/issues");
  await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
  await page.clock.runFor(6000);
  assert.deepEqual(calls.filter(viewOnly), [], "hidden main view must not start its reads");
  assert(count("/api/v1/events") > 0, "global ledger stream remains active");
  assert(count("/api/v1/snapshot") > 0, "global host/project summary remains active");
  assert(count("/api/v1/inbox") > 0 && count("/api/v1/next-actions") > 0);

  await navigate(page, "/diagnostics/main");
  const input = page.getByRole("textbox", { name: "消息", exact: true });
  await input.fill("Retained fixture draft");
  await input.evaluate((element) => element.setAttribute("data-retained-fixture", "main"));
  await page.getByRole("button", { name: "上下文用量 42%", exact: true }).waitFor();
  for (const category of ["owner", "background", "withdrawn"])
    assert(count(`/api/v1/conversation/queue?category=${category}`) > 0);
  assert(count("/api/v1/conversation/settings?") > 0);
  assert(count("/api/v1/conversation/transcript?") > 0);
  assert(count("/api/v1/agent-conversations/main/compactions") > 0);

  holdUsage = true;
  const inflight = new Promise<void>((done) => (heldUsage = done));
  await page.clock.runFor(5100);
  await Promise.race([
    inflight,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error("no usage poll")), 10000)),
  ]);
  await navigate(page, "/issues");
  await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
  const hiddenCalls = calls.filter(viewOnly).length;
  await page.clock.runFor(11000);
  assert.equal(calls.filter(viewOnly).length, hiddenCalls, "hidden reads must remain stopped");
  assert(usageAborted > 0, "hiding must abort the in-flight usage request");
  assert(await page.getByRole("link", { name: "收件箱", exact: true }).first().isVisible());

  holdUsage = false;
  await navigate(page, "/diagnostics/main");
  assert.equal(await input.inputValue(), "Retained fixture draft");
  assert.equal(await input.getAttribute("data-retained-fixture"), "main");
  assert(await input.evaluate((element) => element !== document.activeElement));
  await page.getByRole("button", { name: "上下文用量 42%", exact: true }).waitFor();
  await page.clock.runFor(5100);
  assert(calls.filter(viewOnly).length > hiddenCalls, "showing resumes view reads");
  assert.deepEqual(errors, []);
  await writeFile(join(output, "result.json"), JSON.stringify({ calls, usageAborted, errors }));
  console.log("PASS hidden conversation reads, abort, retained draft and global summaries");
  await context.close();
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
