/// <reference lib="dom" />
import { workItemPage } from "./work-items-fixture.ts";
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
  title:
    ["启动设计稿 Web 预览", "调整附件入口图标", "调整仓库归档文案与排序"][i] ??
    `Example completed task ${i + 1}`,
  primary_work_item_id: "wi_01M5M3ME31875GDA8F7P5XTD74",
  primary_work_item_title: "移动端工作项页重新设计",
  completed_at: Date.now() - (i + 1) * 5 * 60_000,
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
  if (path === "/api/v1/repos") return json({ repos: [] });
  if (path === "/api/v1/work-items") {
    const fixture = workItemPage(url.searchParams.get("project") || "sample");
    const original = fixture.items![0]!;
    return json({
      items: Array.from({ length: 6 }, (_, i) => ({
        ...original,
        work_item_id: original.work_item_id.slice(0, -1) + i,
        title:
          ["移动端工作项页重新设计", "后续任务自动关联已有交付文件", "附件入口图标调整"][i] ??
          `示例工作项 ${i + 1}`,
        execution: {
          queued: 0,
          running: i === 0 ? 1 : 0,
          accepting: 0,
          waiting_owner: 0,
          paused: 0,
          unknown: 0,
          completed: 2,
          cancelled: 0,
          idle: 0,
        },
      })),
    });
  }
  if (path === "/api/v1/snapshot") return json(snapshot);
  if (path === "/api/v1/conversation/settings") return json({ enabled: false });
  if (path === "/api/v1/conversation/handoff") return json(handoff);
  if (path === "/api/v1/conversation") return json({ items: [], more: false });
  if (path === "/api/v1/tasks")
    return json({ tasks: url.searchParams.get("lifecycle") === "completed" ? tasks : [] });
  if (path.startsWith("/api/v1/tasks/")) {
    const task = tasks.find((task) => path.endsWith(task.task_id));
    if (task)
      return json({
        ...task,
        spec: { goal: task.title, acceptance: [] },
        actions: [],
        runs: [],
        artifacts: [],
        acceptances: [],
        decisions: [],
      });
  }
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
    { width: 820, height: 900 },
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
    if (!mobile) {
      const sidebar = page.locator("aside").first();
      const overview = sidebar.getByRole("region", { name: "工作项概览" });
      await overview.getByRole("button", { name: "查看全部 6 个工作项 ›" }).waitFor();
      assert.equal(await overview.getByRole("button").count(), 4);
      assert.equal(await sidebar.getByText("执行任务与记录", { exact: true }).count(), 0);
      const results = sidebar.getByRole("region", { name: "最近完成的任务" });
      await results.getByRole("button").nth(2).waitFor();
      assert.equal(await results.getByRole("button").count(), 3);
      assert.equal(
        await results.getByText("所属工作项：移动端工作项页重新设计", { exact: true }).count(),
        3,
      );
      assert.equal(await results.locator("time").count(), 3);
      assert(reads.some((path) => path.includes("sort=completed_at")));
      await page.screenshot({ path: join(output, `sidebar-${viewport.width}.png`) });
      await results.getByRole("button").first().click();
      const details = page.getByRole("region", { name: "任务详情", exact: true });
      await details.getByText("启动设计稿 Web 预览", { exact: true }).first().waitFor();
      await details.getByRole("button", { name: "关闭", exact: true }).click();
      const records = sidebar.getByRole("button", { name: "全部执行记录 ›", exact: true });
      await records.scrollIntoViewIfNeeded();
      await unobscured(records);
      await records.focus();
      await page.keyboard.press("Enter");
      await page.getByRole("heading", { name: "执行记录", exact: true }).waitFor();
      await page.getByLabel("按项目筛选", { exact: true }).fill("filtered-project");
      await records.click();
      assert.equal(await page.getByLabel("按项目筛选", { exact: true }).inputValue(), "");
      await overview.getByRole("button", { name: "查看全部 6 个工作项 ›" }).click();
      await page.getByRole("heading", { name: "工作项", exact: true }).waitFor();
    }
    if (mobile) await tabs.getByRole("button", { name: "工作项", exact: true }).click();
    else await page.getByRole("button", { name: "工作项 · 白板 ›" }).click();
    await page.getByRole("heading", { name: "工作项" }).waitFor();
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
    if (!mobile) await rows.nth(1).scrollIntoViewIfNeeded();
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
    const board = page.getByRole("region", { name: "工作项白板" });
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
    await page.getByLabel("按项目筛选工作项").fill("unrelated");
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
