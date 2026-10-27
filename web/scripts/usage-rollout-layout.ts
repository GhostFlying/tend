/// <reference lib="dom" />
// Render the built product against isolated API fixtures; reject every write.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { resolve, join } from "node:path";
import { tmpdir } from "node:os";
import { chromium } from "playwright";
import type { TokenUsageTotals, TokenUsageView, RunLogView } from "../src/api/generated/api.ts";
const root = resolve(fileURLToPath(new URL("../../internal/webui/dist/app/", import.meta.url)));
const output = process.env.TEND_USAGE_OUTPUT ?? join(tmpdir(), "tend-usage-rollout-layout");
await mkdir(output, { recursive: true });
const totals: TokenUsageTotals = {
  requests: 2,
  input: 12000,
  input_known: 2,
  cache_read: 6000,
  cache_read_known: 2,
  cache_write: 500,
  cache_write_known: 1,
  output: 2000,
  output_known: 2,
  total: 14000,
  total_known: 2,
  final: 1,
};
let unknown = false;
let correction = false;
let runsAvailable = true;
let eventSeq = 1;
const taskID = "task_01M5YH00000000000000000000";
const runID = "run_fixture";
const task = {
  task_id: taskID,
  title: "隔离执行过程",
  project: "fixture",
  lifecycle: "running",
  control_mode: "managed",
  control_revision: 1,
  spec_version: 1,
  rating: "p2",
  block_reason: "none",
  workspace: "fixture",
  next_implement_generation: 2,
  execution: { state: "running", run_id: runID, role: "implement" },
  spec: { goal: "fixture", acceptance: [] },
  runs: [
    {
      run_id: runID,
      role: "implement",
      generation: 1,
      spec_version: 1,
      host: "fixture",
      observed: "running",
    },
  ],
  actions: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
};
const logs: RunLogView[] = Array.from({ length: 150 }, (_, i) => ({
  seq: i + 1,
  native_id: `item_${i + 1}`,
  kind: "item.completed",
  body: { type: "agentMessage", text: `隔离记录 ${i + 1}` },
  created_at: "2026-10-27T03:00:00Z",
}));
const streams = new Set<ServerResponse>();
const writes: string[] = [];
const reads: string[] = [];
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const path = url.pathname;
  res.setHeader("Cache-Control", "no-store");
  const json = (data: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(data));
  };
  if (req.method !== "GET" && !path.endsWith("/session")) {
    writes.push(path);
    res.statusCode = 405;
    return json({ error: { code: "forbidden", message: "Fixture rejects writes" } });
  }
  if (path === "/fixture-font" && process.env.TEND_LAYOUT_FONT) {
    res.setHeader("Content-Type", "font/ttf");
    res.end(await readFile(process.env.TEND_LAYOUT_FONT));
    return;
  }
  if (path.startsWith("/api/")) reads.push(path + url.search);
  if (path === "/api/v1/session")
    return json({
      token: "fixture",
      login: "Example owner",
      scope: "owner",
      deployment_id: "usage-layout-fixture",
    });
  if (path === "/api/v1/snapshot")
    return json({
      event_seq: eventSeq,
      tasks: [task],
      projects: ["fixture"],
      hosts: [],
      queue: [],
      decisions: [],
    });
  if (path === "/api/v1/work-items") return json({ items: [], more: false });
  if (path === "/api/v1/agent-conversations") return json({ enabled: false, items: [] });
  if (path === "/api/v1/tasks") return json({ tasks: [task] });
  if (path === `/api/v1/tasks/${taskID}`)
    return json({ ...task, runs: runsAvailable ? task.runs : [] });
  if (path === "/api/v1/repos") return json({ repos: [] });
  if (path === "/api/v1/conversation") return json({ items: [], more: false });
  if (path === "/api/v1/conversation/queue") return json({ items: [] });
  if (path === "/api/v1/conversation/settings" || path === "/api/v1/conversation/handoff")
    return json({ enabled: false });
  if (path === "/api/v1/token-usage") {
    const t = unknown
      ? {
          ...totals,
          requests: 1,
          input_known: 0,
          output_known: 0,
          total_known: 0,
          cache_read_known: 0,
          cache_write_known: 0,
        }
      : { ...totals, total: correction ? 13000 : 14000, output: correction ? 1000 : 2000 };
    const view: TokenUsageView = {
      trend: [],
      revision: correction ? 2 : 1,
      as_of: 1793066400000,
      timezone: "Asia/Shanghai",
      window: url.searchParams.get("window") ?? "7d",
      start: null,
      end: 1793066400000,
      totals: t,
      unattributed: { ...totals, requests: 0 },
      models: [{ name: "model-fixture", ...t }],
      roles: [{ name: "implement", ...t }],
      records: [],
      next: 0,
      more: false,
      conflicts: 0,
      coverage: "隔离虚构请求；不表示真实消费。",
    };
    return json(view);
  }
  if (path === `/api/v1/runs/${runID}/logs`)
    return json({ run_id: runID, observed: "running", logs: logs.slice(0, 100), next: 100 });
  if (path === `/api/v1/runs/${runID}/rollout`) {
    const before = Number(url.searchParams.get("before"));
    const after = Number(url.searchParams.get("after"));
    const selected = before
      ? logs.filter((r) => r.seq < before).slice(-100)
      : after
        ? logs.filter((r) => r.seq > after).slice(0, 100)
        : logs.slice(-100);
    const first = selected[0]?.seq ?? before;
    const last = selected.at(-1)?.seq ?? after;
    return json({
      run_id: runID,
      observed: "running",
      logs: selected,
      before: first,
      after: last,
      watermark: logs.at(-1)!.seq,
      more_before: first > 1,
      more_after: last < logs.at(-1)!.seq,
    });
  }
  if (path === "/api/v1/events") {
    res.setHeader("Content-Type", "text/event-stream");
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path.startsWith("/api/")) {
    res.statusCode = 404;
    return json({ error: { code: "not_found", message: "Fixture route absent" } });
  }
  try {
    const file = resolve(root, `.${path}${path.endsWith("/") ? "index.html" : ""}`);
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "Content-Type",
      path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css" : "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.statusCode = 404;
    res.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch();
try {
  for (const viewport of [
    { width: 320, height: 720 },
    { width: 390, height: 568 },
    { width: 1440, height: 900 },
  ]) {
    correction = false;
    unknown = false;
    const context = await browser.newContext({ viewport, serviceWorkers: "block" });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(`http://127.0.0.1:${address.port}/`);
    if (process.env.TEND_LAYOUT_FONT)
      await page.addStyleTag({
        content:
          "@font-face{font-family:FixtureCJK;src:url(/fixture-font)}:root{--font:FixtureCJK,sans-serif}",
      });
    if (viewport.width <= 760) {
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("button", { name: "工作项" })
        .click();
      await page.getByRole("button", { name: /资源与记录/ }).click();
    }
    await page.getByRole("button", { name: "Token 用量 ›", exact: true }).click();
    const panel = page.getByRole("region", { name: "Token 用量", exact: true });
    await panel.getByText("14,000", { exact: true }).waitFor();
    const buttons = await panel
      .getByRole("navigation", { name: "用量时间范围" })
      .getByRole("button")
      .allTextContents();
    assert.equal(buttons.at(-1), "总共");
    const filterBounds = await panel
      .getByRole("navigation", { name: "用量时间范围" })
      .boundingBox();
    const totalBounds = await panel
      .getByRole("button", { name: "总共", exact: true })
      .boundingBox();
    assert(filterBounds && totalBounds);
    assert(
      Math.abs(totalBounds.x + totalBounds.width - filterBounds.x - filterBounds.width) < 2,
      "total filter is not at the far right",
    );

    const line = await panel.locator('[class*="line"]').first().innerText();
    assert(line.indexOf("输入 token") < line.indexOf("缓存读取"));
    assert(line.indexOf("缓存写入") < line.indexOf("输出 token"));
    await panel.getByRole("button", { name: "总共", exact: true }).click();
    await panel.getByText("14,000", { exact: true }).waitFor();
    correction = true;
    await panel.getByRole("button", { name: "刷新", exact: true }).click();
    await panel.getByText("13,000", { exact: true }).waitFor();
    await page.screenshot({
      path: join(output, `usage-known-${viewport.width}.png`),
      fullPage: true,
    });
    unknown = true;
    await panel.getByRole("button", { name: "刷新", exact: true }).click();
    await panel.locator('[class*="hero"]').getByText("未提供", { exact: true }).waitFor();
    assert(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `usage-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "关闭详情", exact: true }).click();
    runsAvailable = false;
    await page.goto(`http://127.0.0.1:${address.port}/?task=${taskID}`);
    await page.getByText("执行过程与用量", { exact: true }).first().click();
    const rollout = page.getByRole("region", { name: "执行过程与用量" });
    await rollout.getByText("尚未开始执行。", { exact: true }).waitFor();
    runsAvailable = true;
    eventSeq++;
    for (const stream of streams)
      stream.write(
        `event: ledger\ndata: ${JSON.stringify({ event_seq: eventSeq, object_type: "task", object_id: taskID, kind: "task.changed", created_at: "2026-10-27T03:00:00Z" })}\n\n`,
      );

    await rollout.getByText("隔离记录 150", { exact: true }).waitFor();
    await page.waitForTimeout(2200);
    assert(
      await rollout.getByRole("button", { name: "更早记录", exact: true }).isEnabled(),
      "empty live poll lost the older-page cursor",
    );
    const beforePause = await rollout.locator("article").allTextContents();
    await rollout.getByRole("button", { name: "暂停跟随", exact: true }).click();
    await page.waitForTimeout(100);
    assert.deepEqual(
      await rollout.locator("article").allTextContents(),
      beforePause,
      "pausing replaced the reading window",
    );
    await rollout.getByRole("button", { name: "更早记录", exact: true }).click();
    await rollout.getByText("隔离记录 1", { exact: true }).waitFor();
    await rollout.getByRole("button", { name: "较新记录", exact: true }).click();
    await rollout.getByText("隔离记录 150", { exact: true }).waitFor();
    assert.equal(await rollout.getByText("隔离记录 150", { exact: true }).count(), 1);
    assert(await rollout.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `rollout-${viewport.width}.png`), fullPage: true });
    assert.deepEqual(errors, []);
    await context.close();
    console.log(`PASS ${viewport.width}x${viewport.height}`);
  }

  // A complete short history must not acquire a false "older" boundary when
  // the next live row arrives; the merge already contains that earlier page.
  logs.splice(10);
  const boundaryContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
    serviceWorkers: "block",
  });
  const boundary = await boundaryContext.newPage();
  await boundary.goto(`http://127.0.0.1:${address.port}/?task=${taskID}`);
  await boundary.getByText("执行过程与用量", { exact: true }).first().click();
  const short = boundary.getByRole("region", { name: "执行过程与用量" });
  await short.getByText("隔离记录 10", { exact: true }).waitFor();
  const earlier = short.getByRole("button", { name: "更早记录", exact: true });
  assert(await earlier.isDisabled());
  logs.push({
    seq: 11,
    native_id: "item_11",
    kind: "item.completed",
    body: { type: "agentMessage", text: "隔离记录 11" },
    created_at: "2026-10-27T03:00:00Z",
  });
  await short.getByText("隔离记录 11", { exact: true }).waitFor();
  assert(await earlier.isDisabled(), "live append fabricated an older history page");
  assert.equal(await short.locator("article").count(), 11);
  await boundaryContext.close();
  assert.equal(writes.length, 0);
  assert(reads.some((path) => path.includes("/rollout?before=")));
  assert(reads.some((path) => path.includes("window=total")));
  console.log(`Browser ${browser.version()}; screenshots ${output}`);
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
