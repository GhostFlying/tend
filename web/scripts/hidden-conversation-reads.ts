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
let ledgerSeq = 1;
let issueTitle = "Initial board state";
const missedEvent = () =>
  `id: ${ledgerSeq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: ledgerSeq, object_type: "issue", object_id: "reconnect", kind: "issue.updated", created_at: "2026-01-01T00:00:00Z" })}\n\n`;
let holdUsage = false;
const createdAt = "2026-01-01T00:00:00Z";
const original = {
  seq: 1,
  message_id: "original",
  author: "owner",
  kind: "message",
  body: "Original input outside the newest Chat page",
  created_at: createdAt,
};
const visibleHistory = [
  original,
  ...Array.from({ length: 60 }, (_, index) => ({
    seq: index + 2,
    message_id: "visible-" + (index + 1),
    author: "main_agent",
    kind: "message",
    body: "Visible reply " + (index + 1),
    created_at: createdAt,
    reply_to: original.message_id,
  })),
];
let active = true;
const writes: {
  operation: string;
  target: { id: string };
  payload: { message_id: string };
  request_id: string;
}[] = [];
let usageAborted = 0;
let heldUsage: (() => void) | undefined;
const viewOnly = (path: string) =>
  path.startsWith("/api/v1/conversation/queue?") ||
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
  if (path === "/api/v1/ops" && req.method === "POST") {
    let body = "";
    for await (const chunk of req) body += String(chunk);
    const envelope = JSON.parse(body);
    writes.push(envelope);
    assert.equal(envelope.operation, "conversation.interrupt_reply");
    active = false;
    return json({ operation: { request_id: envelope.request_id, status: "accepted" } });
  }
  if (path === "/api/v1/messages/original")
    return json({ message_id: "original", state: active ? "processing" : "interrupted" });
  if (req.method !== "GET") return res.writeHead(405).end();
  if (path === "/api/v1/session")
    return json({ login: "Fixture owner", scope: "owner", deployment_id: "visibility" });
  if (path === "/api/v1/events") {
    assert.equal(url.searchParams.get("projection"), "chat");
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    const after = Number(url.searchParams.get("after") ?? ledgerSeq);
    res.write(`event: start\ndata: {"event_seq":${after}}\n\n`);
    if (after < ledgerSeq) res.write(missedEvent());
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path === "/api/v1/issues" && url.searchParams.has("board_status")) {
    const items =
      url.searchParams.get("board_status") === "backlog"
        ? [
            {
              issue_id: "reconnect",
              title: issueTitle,
              project: "fixture",
              state: "active",
              assignee: { kind: "owner", ref: "owner" },
              assignment_epoch: 1,
              assignment_state: "active",
              revision: ledgerSeq,
            },
          ]
        : [];
    return json({ items, facts_seq: ledgerSeq, more: false });
  }
  if (path === "/api/v1/snapshot")
    return json({
      event_seq: ledgerSeq,
      tasks: [],
      projects: [],
      hosts: [],
      queue: [],
      decisions: [],
    });
  if (path === "/api/v1/inbox")
    return json({
      items: [{ attention_id: "attention", kind: "action", status: "open", title: "Fixture" }],
    });
  if (path === "/api/v1/conversation/settings") return json({ enabled: false, revision: 1 });
  if (path === "/api/v1/conversation/queue")
    return json({
      enabled: true,
      event_seq: ledgerSeq,
      items: [],
      total: 0,
      ...(active && url.searchParams.get("category") === "owner"
        ? { interruptible_message_id: "original", processing_started_at: Date.parse(createdAt) }
        : {}),
    });
  if (path === "/api/v1/conversation") {
    assert.equal(url.searchParams.get("projection"), "chat");
    const before = Number(url.searchParams.get("before") ?? Infinity);
    const items = visibleHistory.filter((item) => item.seq < before);
    return json({ items: items.slice(-50), more: items.length > 50, event_seq: ledgerSeq });
  }
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
  const consoleErrors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error" || message.type() === "warning")
      consoleErrors.push(message.text());
  });
  await page.clock.install();
  await page.goto(origin + "/issues");
  await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
  await page.clock.runFor(6000);
  assert.deepEqual(calls.filter(viewOnly), [], "hidden main view must not start its reads");
  assert(count("/api/v1/events") > 0, "global ledger stream remains active");
  assert(count("/api/v1/snapshot") > 0, "global host/project summary remains active");
  assert(count("/api/v1/inbox") > 0 && count("/api/v1/next-actions") > 0);

  const board = page.locator('[data-issue-id="reconnect"]');
  await board.getByRole("button", { name: issueTitle, exact: true }).waitFor();
  const boardPath = "/api/v1/issues?board=true";
  const historyPath = "/api/v1/conversation?projection=chat&limit=";
  const quietReads = [count(boardPath), count(historyPath)] as const;
  assert(
    quietReads.every((n) => n > 0),
    "fresh connection reads board and conversation",
  );
  const disconnect = async (change?: string) => {
    const requestedAfter = ledgerSeq;
    const resumed = page.waitForResponse(
      origin + `/api/v1/events?projection=chat&after=${requestedAfter}`,
    );
    const snapshot = page.waitForResponse(origin + "/api/v1/snapshot");
    assert.equal(streams.size, 1);
    for (const stream of streams) stream.end();
    if (change) {
      ledgerSeq++;
      issueTitle = change;
    }
    await resumed;
    await snapshot;
  };
  await disconnect();
  assert.deepEqual(
    [count(boardPath), count(historyPath)],
    quietReads,
    "quiet resume reuses ledger views",
  );

  await disconnect("Issue updated during disconnection");
  await board.getByRole("button", { name: issueTitle, exact: true }).waitFor();
  assert.equal(count(historyPath), quietReads[1], "issue replay leaves unrelated history alone");

  // Expired coalesced notifications require a new, cursor-free baseline.
  ledgerSeq++;
  issueTitle = "Issue rebuilt after reset";
  const fresh = page.waitForResponse(origin + "/api/v1/events?projection=chat");
  const history = page.waitForResponse((response) =>
    response.url().startsWith(origin + historyPath),
  );
  for (const stream of streams) stream.end(`event: reset\ndata: {"event_seq":${ledgerSeq}}\n\n`);
  const [, rebuiltHistory] = await Promise.all([fresh, history]);
  await board.getByRole("button", { name: issueTitle, exact: true }).waitFor();
  assert.equal(rebuiltHistory.status(), 200);
  assert(count(historyPath) > quietReads[1], "reset rebuilds the conversation from its source");

  await navigate(page, "/diagnostics/main");
  const input = page.getByRole("textbox", { name: "消息", exact: true });
  await input.waitFor();
  assert.equal(new URL(page.url()).pathname, "/diagnostics/main");
  assert.match(await page.title(), /tend/i);
  const transcript = page.getByRole("region", { name: "对话记录" });
  await transcript.getByText("Visible reply 60", { exact: true }).waitFor();
  assert.equal(await transcript.getByText(original.body, { exact: true }).count(), 0);
  assert.equal(count("/api/v1/conversation?projection=chat&before="), 0);
  assert.equal(await page.locator("vite-error-overlay").count(), 0);
  const stop = page.getByRole("button", { name: "停止回复", exact: true });
  await stop.waitFor();
  await page.screenshot({ path: join(output, "original-stop-target.png") });
  await stop.click();
  await stop.waitFor({ state: "hidden" });
  assert.equal(writes.length, 1);
  assert.equal(writes[0]?.target.id, original.message_id);
  assert.equal(writes[0]?.payload.message_id, original.message_id);
  for (const [name, width, height] of [
    ["desktop", 1280, 720],
    ["mobile", 390, 844],
  ] as const) {
    await page.setViewportSize({ width, height });
    assert.equal(await transcript.getByRole("button", { name: /查看过程/ }).count(), 0);
    await page.screenshot({ path: join(output, "visible-first-page-" + name + ".png") });
  }
  const chatReads = () =>
    calls.filter(
      (path) =>
        path.startsWith("/api/v1/conversation?") || path.startsWith("/api/v1/conversation/queue?"),
    ).length;
  const beforeTool = chatReads();
  ledgerSeq++;
  for (const stream of streams)
    stream.write("event: cursor\ndata: " + JSON.stringify({ event_seq: ledgerSeq }) + "\n\n");
  await page.clock.runFor(500);
  assert.equal(chatReads(), beforeTool, "skipped events do not reread Chat");
  await transcript.evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll", { bubbles: true }));
  });
  await transcript.getByText(original.body, { exact: true }).waitFor({ state: "attached" });
  assert.equal(count("/api/v1/conversation?projection=chat&before="), 1);
  assert.equal(count("/api/v1/conversation/transcript"), 0);
  assert.equal(count("/api/v1/conversation/blocks/"), 0);
  assert.equal(count("/api/v1/agent-conversations/main/rollout"), 0);
  await page.screenshot({ path: join(output, "earlier-visible-page.png") });
  await input.fill("Retained fixture draft");
  await input.evaluate((element) => element.setAttribute("data-retained-fixture", "main"));
  await page.getByRole("button", { name: "上下文用量 42%", exact: true }).waitFor();
  for (const category of ["owner", "withdrawn"])
    assert(count(`/api/v1/conversation/queue?category=${category}`) > 0);
  assert(count("/api/v1/conversation/settings?") > 0);
  assert.equal(count("/api/v1/conversation/transcript?"), 0);
  assert.equal(count("/api/v1/agent-conversations/main/compactions"), 0);

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
  // This fixture deliberately blocks service workers in the browser context.
  const appConsoleErrors = consoleErrors.filter(
    (message) => message !== "Service Worker registration blocked by Playwright",
  );
  assert.deepEqual(appConsoleErrors, []);
  await writeFile(
    join(output, "result.json"),
    JSON.stringify({ calls, writes, usageAborted, errors, consoleErrors, appConsoleErrors }),
  );
  console.log("PASS hidden conversation reads, abort, retained draft and global summaries");
  await context.close();
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
