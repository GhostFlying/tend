/// <reference lib="dom" />
// Exercise the built app with an isolated owner API, never a deployed server.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, type Locator } from "playwright";
import type { Envelope, HostView, Snapshot } from "../src/api/generated/api.ts";

const root = fileURLToPath(new URL("../../internal/webui/dist/app/", import.meta.url));
const output = process.env.TEND_LAYOUT_OUTPUT ?? join(tmpdir(), "tend-mobile-layout");
await mkdir(output, { recursive: true });
const host = (id: string, online = true, maintenance = false): HostView => ({
  host_id: id,
  online,
  labels: [],
  slots: 4,
  open_runs: online ? 1 : 0,
  os: "linux",
  arch: "amd64",
  worker: "orca",
  build_version: "example-build",
  protocol_version: 1,
  last_heartbeat_at: "2026-10-24T06:00:00Z",
  ...(maintenance
    ? { maintenance: { mode: "maintenance", phase: "draining", reason: "Example maintenance" } }
    : {}),
});
const initialHosts = () => [host("Example Alpha"), host("Example Beta", false, true)];
let snapshot: Snapshot = {
  event_seq: 1,
  tasks: [],
  projects: [],
  hosts: initialHosts(),
  queue: [],
  decisions: [],
};
const tasks = Array.from({ length: 5 }, (_, i) => ({
  task_id: `task_example${i}`,
  title: `Example completed task ${i + 1}`,
  project: "sample",
  lifecycle: "completed",
  control_mode: "managed",
  control_revision: 1,
  spec_version: 1,
  rating: "p2",
  block_reason: "none",
  workspace: "example",
  next_implement_generation: 1,
}));
let handoff = { enabled: true, revision: 1, idle_minutes: 50 };
const writes: Envelope[] = [];
const streams = new Set<ServerResponse>();
const reads: string[] = [];
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const path = url.pathname;
  res.setHeader("Cache-Control", "no-store");
  const json = (data: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(data));
  };
  if (path.startsWith("/api/")) reads.push(url.pathname + url.search);
  if (path === "/api/v1/session")
    return json({ token: "fixture-token", login: "Example owner", scope: "owner" });
  if (path === "/api/v1/snapshot") return json(snapshot);
  if (path === "/api/v1/conversation/settings") return json({ enabled: false });
  if (path === "/api/v1/conversation/handoff") return json(handoff);
  if (path === "/api/v1/conversation") return json({ items: [], more: false });
  if (path === "/api/v1/tasks")
    return json({ tasks: url.searchParams.get("lifecycle") === "completed" ? tasks : [] });
  if (path === "/api/v1/events") {
    res.setHeader("Content-Type", "text/event-stream");
    res.write(`event: start\ndata: ${JSON.stringify({ event_seq: snapshot.event_seq })}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path === "/api/v1/ops" && req.method === "POST") {
    let body = "";
    for await (const chunk of req) body += String(chunk);
    const envelope = JSON.parse(body) as Envelope;
    writes.push(envelope);
    assert.equal(envelope.operation, "conversation.set_handoff_settings");
    assert.equal(envelope.expected_control_revision, handoff.revision);
    handoff = {
      ...handoff,
      revision: handoff.revision + 1,
      idle_minutes: (envelope.payload as { idle_minutes: number }).idle_minutes,
    };
    return json({ operation: { status: "succeeded", result: {} } });
  }
  if (path.startsWith("/api/")) {
    res.statusCode = 404;
    return json({ error: { code: "not_found", message: "Not in test data" } });
  }
  try {
    const file = resolve(root, `.${path}${path.endsWith("/") ? "index.html" : ""}`);
    assert(file.startsWith(root));
    const type = path.endsWith(".js")
      ? "text/javascript"
      : path.endsWith(".css")
        ? "text/css"
        : "text/html";
    res.setHeader("Content-Type", type);
    res.end(await readFile(file));
  } catch {
    res.statusCode = 404;
    res.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch({
  ...(process.env.TEND_LAYOUT_BROWSER ? { executablePath: process.env.TEND_LAYOUT_BROWSER } : {}),
});

function publish(hosts: HostView[]) {
  snapshot = { ...snapshot, hosts, event_seq: snapshot.event_seq + 1 };
  for (const res of streams)
    res.write(
      `event: ledger\ndata: ${JSON.stringify({ event_seq: snapshot.event_seq, object_type: "host", object_id: "example", kind: "host.runtime", created_at: "2026-10-24T06:00:00Z" })}\n\n`,
    );
}

// Unlike isVisible(), this detects clipping by a scroll region or another panel.
async function unobscured(locator: Locator) {
  assert(
    await locator.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return r.top >= 0 && r.bottom <= innerHeight && hit !== null && el.contains(hit);
    }),
    `clipped or covered: ${await locator.textContent()}`,
  );
}

try {
  for (const viewport of [
    { width: 390, height: 760 },
    { width: 430, height: 832 },
    { width: 1440, height: 900 },
    { width: 390, height: 568 },
  ]) {
    snapshot = { ...snapshot, hosts: initialHosts() };
    handoff = { enabled: true, revision: 1, idle_minutes: 50 };
    const context = await browser.newContext({ viewport, serviceWorkers: "block" });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(`http://127.0.0.1:${address.port}/`);
    const mobile = viewport.width <= 760;
    const tabs = page.getByRole("navigation", { name: "视图" });
    assert.equal(
      await page.locator("header").getByRole("button", { name: "设置", exact: true }).count(),
      0,
    );
    if (mobile) await tabs.getByRole("button", { name: "任务", exact: true }).click();
    else await page.getByRole("button", { name: "全部任务 · 阶段看板 ›" }).click();
    await page.getByRole("heading", { name: "任务看板" }).waitFor();
    assert.equal(
      await page.locator("header").getByRole("button", { name: "设置", exact: true }).count(),
      0,
    );
    const summary = page.locator("summary").filter({ hasText: "执行主机" });
    const recent = page.locator("summary").filter({ hasText: "最近完成" });
    if (mobile) {
      await recent.click();
      await page
        .getByRole("region", { name: "最近完成" })
        .getByText("Example completed task 5")
        .waitFor({ state: "attached" });
      // The old layout fails here: five recent tasks hid the hosts entry below 180px.
      await unobscured(summary);
      assert.match((await summary.textContent())!, /2 台/);
      await recent.click();
      await page.screenshot({
        path: join(output, `board-${viewport.width}x${viewport.height}.png`),
      });
      await summary.click();
    }
    const list = page.getByRole("region", { name: "执行主机占用" }).filter({ visible: true });
    const rows = list.getByRole("button", { name: /^主机详情：/ });
    await rows.nth(1).waitFor();
    assert.equal(await rows.count(), 2);
    if (viewport.height >= 760) {
      await unobscured(rows.nth(0));
      await unobscured(rows.nth(1));
    }
    assert.match((await rows.nth(1).textContent())!, /离线/);
    assert.match((await rows.nth(1).textContent())!, /维护/);
    for (const id of ["Example Alpha", "Example Beta"]) {
      const row = list.getByRole("button", { name: `主机详情：${id}`, exact: true });
      await row.scrollIntoViewIfNeeded();
      await unobscured(row);
      await row.click();
      const dialog = page.getByRole("dialog", { name: "主机详情" });
      assert.equal(await dialog.getByRole("heading", { level: 3 }).textContent(), id);
      await page.screenshot({
        path: join(output, `detail-${id.split(" ")[1]}-${viewport.width}x${viewport.height}.png`),
      });
      await dialog.getByRole("button", { name: "关闭主机详情" }).click();
    }
    await page.screenshot({ path: join(output, `hosts-${viewport.width}x${viewport.height}.png`) });
    if (mobile) await recent.click();
    publish(Array.from({ length: 24 }, (_, i) => host(`Example Host ${i + 1}`)));
    await rows.nth(23).waitFor({ state: "attached" });
    await rows.nth(23).scrollIntoViewIfNeeded();
    await unobscured(rows.nth(23));
    await rows.nth(23).click();
    assert.equal(
      await page
        .getByRole("dialog", { name: "主机详情" })
        .getByRole("heading", { level: 3 })
        .textContent(),
      "Example Host 24",
    );
    await page.getByRole("button", { name: "关闭主机详情" }).click();
    const board = page.getByRole("region", { name: "分阶段任务看板" });
    assert((await board.boundingBox())!.height > 80);
    assert(await list.evaluate((el) => el.scrollWidth <= el.clientWidth));
    if (mobile)
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    publish([]);
    await list.getByText("暂无已登记的执行主机。").waitFor();
    assert.equal(await rows.count(), 0);
    publish([host("Example Solo", false)]);
    await list.getByRole("button", { name: "主机详情：Example Solo", exact: true }).waitFor();
    assert.equal(await rows.count(), 1);
    await rows.first().click();
    publish([host("Example New"), host("Example Solo", true, true)]);
    const detail = page.getByRole("dialog", { name: "主机详情" });
    await detail.getByText("在线", { exact: true }).waitFor();
    assert.equal(await detail.getByRole("heading", { level: 3 }).textContent(), "Example Solo");
    assert.match((await detail.textContent())!, /维护/);
    await detail.getByRole("button", { name: "关闭主机详情" }).click();
    const filtered = page.waitForResponse((response) =>
      response.url().includes("project=unrelated"),
    );
    await page.getByLabel("按项目筛选").fill("unrelated");
    await filtered;
    assert.equal(await rows.count(), 2);
    assert(reads.some((path) => path.includes("project=unrelated")));
    assert(!reads.some((path) => path.startsWith("/api/v1/snapshot?")));
    if (mobile) await tabs.getByRole("button", { name: "待处理", exact: true }).click();
    const settings = page.getByRole("button", { name: "设置", exact: true });
    await unobscured(settings);
    await settings.click();
    const panel = page.getByRole("dialog", { name: "设置", exact: true });
    await panel.getByLabel("空闲分钟").fill("60");
    await panel.getByRole("button", { name: "保存", exact: true }).click();
    await panel.getByText("空闲 60 分钟后自动准备。", { exact: false }).waitFor();
    assert.equal(writes.at(-1)?.operation, "conversation.set_handoff_settings");
    await page.screenshot({
      path: join(output, `settings-${viewport.width}x${viewport.height}.png`),
    });
    await panel.getByRole("button", { name: "关闭设置" }).click();
    assert(await settings.evaluate((el) => el === document.activeElement));
    if (mobile) await tabs.getByRole("button", { name: "对话", exact: true }).click();
    else await page.getByRole("button", { name: "返回聊天" }).click();
    await page.getByRole("heading", { name: "主 Agent" }).waitFor();
    assert.deepEqual(errors, []);
    await context.close();
    console.log(
      `PASS ${viewport.width}x${viewport.height}: discovery, scrolling, live identity, filters, settings`,
    );
  }
  console.log(`Browser ${browser.version()}; generic fixture screenshots: ${output}`);
} finally {
  await browser.close();
  for (const res of streams) res.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
