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
let rolloutFailure = false;
let rolloutEmpty = false;
const taskID = "task_01M5YH00000000000000000000";
const runID = "run_fixture";
const task = {
  task_id: taskID,
  title: "演示：修复执行状态展示",
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
// Fictional actions exercise the production tool renderer, including start/result pairing.
logs[144] = {
  ...logs[144]!,
  kind: "native.item_started",
  native_id: "test-call",
  body: {
    thread_id: "demo-thread",
    turn_id: "demo-turn",
    item_id: "test-call",
    data: { type: "commandExecution", command: "go test ./internal/domain" },
  },
};
logs[145] = {
  ...logs[145]!,
  native_id: "test-call",
  body: {
    thread_id: "demo-thread",
    turn_id: "demo-turn",
    item_id: "test-call",
    type: "commandExecution",
    command: "go test ./internal/domain",
    status: "completed",
    durationMs: 18,
    aggregatedOutput:
      "ok   example/internal/domain  0.018s\n" +
      Array.from({ length: 80 }, (_, index) => `演示输出 ${index + 1}`).join("\n"),
    exitCode: 0,
  },
};
logs[146] = {
  ...logs[146]!,
  body: {
    type: "agentMessage",
    text: "领域测试通过。接下来核对状态变更后的页面展示。\n\n- 保留正在执行的状态\n- 历史阅读时保持当前位置",
  },
};
logs[147] = {
  ...logs[147]!,
  body: {
    type: "mcpToolCall",
    tool: "read_file",
    arguments: { path: "web/src/rollout/RunRollout.tsx" },
    status: "completed",
    result: {
      content: [{ type: "text", text: "读取执行过程组件，检查卡片与完整视图的状态共享。" }],
    },
  },
};
logs[148] = {
  ...logs[148]!,
  body: {
    type: "agentMessage",
    text: "卡片保留最近的执行步骤；打开完整过程后，可以继续阅读工具输出和回复正文。",
  },
};
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
  if (path === "/api/v1/issues") return json({ items: [], more: false });
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
    const subject = url.searchParams.get("subject");
    if (subject) assert.equal(subject, `run:${runID}`);
    const t = subject
      ? {
          ...totals,
          requests: 1,
          input: 40,
          input_known: 1,
          output: 2,
          output_known: 1,
          total: 42,
          total_known: 1,
          cache_read: 10,
          cache_read_known: 1,
          cache_write: 0,
          cache_write_known: 1,
          final: 1,
        }
      : unknown
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
  if (/^\/api\/v1\/runs\/[^/]+\/rollout$/.test(path)) {
    const requestedRun = path.split("/")[4]!;
    if (rolloutFailure) {
      res.statusCode = 503;
      return json({ error: { code: "unavailable", message: "Fixture failure" } });
    }
    if (rolloutEmpty)
      return json({
        run_id: requestedRun,
        observed: "running",
        logs: [],
        before: 0,
        after: 0,
        watermark: 0,
        more_before: false,
        more_after: false,
      });
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
      run_id: requestedRun,
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
    res.write(`event: start\ndata: ${JSON.stringify({ event_seq: eventSeq })}\n\n`);
    streams.add(res);
    const heartbeat = setInterval(() => res.write(": heartbeat\n\n"), 1000);
    res.on("close", () => {
      clearInterval(heartbeat);
      streams.delete(res);
    });
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
        .getByRole("button", { name: "Issue" })
        .click();
      await page.getByRole("button", { name: "任务执行记录", exact: true }).click();
      await page
        .locator("main details > summary")
        .filter({ hasText: /^资源与记录$/ })
        .click();
    }
    await page.getByRole("button", { name: "Token 用量", exact: true }).click();
    const panel = page.getByRole("region", { name: "Token 用量", exact: true });
    await panel.getByText("14,000", { exact: true }).waitFor();
    assert.equal(
      await panel.getByRole("combobox", { name: "范围", exact: true }).inputValue(),
      "total",
    );

    const line = await panel.locator('[class*="line"]').first().innerText();
    assert(line.indexOf("输入 token") < line.indexOf("缓存读取"));
    assert(line.indexOf("缓存写入") < line.indexOf("输出 token"));
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
    await panel.locator('[class*="hero"]').getByText("—", { exact: true }).waitFor();
    assert(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `usage-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "关闭 Token 用量", exact: true }).click();
    runsAvailable = false;
    await page.goto(`http://127.0.0.1:${address.port}/?task=${taskID}`);
    const card = page.getByRole("region", { name: "执行过程与用量" });
    await card.getByText("尚未开始执行。", { exact: true }).waitFor();
    runsAvailable = true;
    eventSeq++;
    for (const stream of streams)
      stream.write(
        `event: ledger\ndata: ${JSON.stringify({ event_seq: eventSeq, object_type: "task", object_id: taskID, kind: "task.changed", created_at: "2026-10-27T03:00:00Z" })}\n\n`,
      );
    await card.getByText("隔离记录 150", { exact: true }).waitFor();
    assert.equal(await card.locator("article").count(), 0);
    await card.screenshot({ path: join(output, `rollout-card-${viewport.width}.png`) });
    await card.getByRole("button", { name: "展开执行步骤" }).click();
    assert.equal(await card.locator("article").count(), 6);
    await card.getByRole("button", { name: "本次用量", exact: true }).click();
    const runUsage = card.getByRole("region", { name: "本次执行 token 用量" });
    await runUsage.locator('[class*="hero"]').getByText("42", { exact: true }).waitFor();
    assert.equal(await runUsage.getByRole("combobox", { name: "范围", exact: true }).count(), 0);
    assert(
      reads.some(
        (path) =>
          path.includes("token-usage?") &&
          new URL(path, "http://localhost").searchParams.get("subject") === `run:${runID}`,
      ),
    );
    assert(!/14,000/.test(await runUsage.innerText()));
    await runUsage.screenshot({ path: join(output, `single-run-usage-${viewport.width}.png`) });
    await card.getByRole("button", { name: "本次用量", exact: true }).click();
    await card.locator("article").last().locator("summary").first().click();
    await card.screenshot({ path: join(output, `rollout-expanded-${viewport.width}.png`) });
    const position = await card.evaluate(
      (el) => el.closest("dialog")?.querySelector('[class*="pane"]')?.scrollTop ?? 0,
    );
    await card
      .getByRole("button", { name: /查看完整过程/ })
      .evaluate((el) => (el as HTMLButtonElement).click());
    const rollout = page.getByRole("dialog", { name: "完整执行过程", exact: true });
    const reader = rollout.getByLabel("执行过程正文", { exact: true });
    await rollout.getByText("实时跟随", { exact: true }).waitFor();
    await rollout.getByRole("button", { name: "本次用量", exact: true }).click();
    const readerUsage = rollout.getByRole("region", { name: "本次执行 token 用量" });
    await readerUsage.locator('[class*="hero"]').getByText("42", { exact: true }).waitFor();
    await readerUsage.screenshot({ path: join(output, `reader-usage-${viewport.width}.png`) });
    await rollout.getByRole("button", { name: "本次用量", exact: true }).click();
    // Capturing the panel scrolls into history; explicitly resume the reader afterward.
    if (await rollout.getByRole("button", { name: "恢复跟随", exact: true }).count())
      await rollout.getByRole("button", { name: "恢复跟随", exact: true }).click();
    await rollout.getByText("隔离记录 150", { exact: true }).waitFor();
    await rollout.getByText("实时跟随", { exact: true }).waitFor();
    assert.equal(await rollout.locator("article").count(), 99);
    assert.equal(
      await rollout
        .locator("article > details > summary")
        .filter({ hasText: "go test ./internal/domain" })
        .count(),
      1,
      "start and result became duplicate steps",
    );
    await page.waitForTimeout(2200);
    assert(await rollout.getByRole("button", { name: "加载更早记录" }).isEnabled());
    const beforePause = await rollout.locator("article").allTextContents();
    await rollout.getByRole("button", { name: "暂停跟随" }).click();
    assert.deepEqual(await rollout.locator("article").allTextContents(), beforePause);
    await reader.evaluate((el) => {
      el.scrollTop = el.scrollHeight / 2;
    });
    const readerTop = (await reader.boundingBox())!.y;
    const anchor = await reader.locator("[data-step-key]").evaluateAll((nodes, top) => {
      const row = nodes.find((node) => node.getBoundingClientRect().bottom > top)!;
      return { key: (row as HTMLElement).dataset.stepKey, top: row.getBoundingClientRect().top };
    }, readerTop);
    // Invoke without scrolling the reader to its top pagination button.
    await rollout
      .getByRole("button", { name: "加载更早记录" })
      .evaluate((el) => (el as HTMLButtonElement).click());
    await rollout.getByText("隔离记录 1", { exact: true }).waitFor();
    const anchored = await reader.locator(`[data-step-key="${anchor.key}"]`).boundingBox();
    assert(anchored && Math.abs(anchored.y - anchor.top) < 3, "prepend moved the reading anchor");
    assert.equal(await rollout.getByText("隔离记录 150", { exact: true }).count(), 1);
    assert(await rollout.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await rollout.getByRole("button", { name: "← 返回", exact: true }).click();
    await rollout.waitFor({ state: "detached" });
    assert.equal(
      await card.getByRole("button", { name: "展开执行步骤" }).getAttribute("aria-expanded"),
      "true",
    );
    assert(
      (await card.locator("article").last().locator("details").first().getAttribute("open")) !==
        null,
    );
    assert(
      Math.abs(
        (await card.evaluate(
          (el) => el.closest("dialog")?.querySelector('[class*="pane"]')?.scrollTop ?? 0,
        )) - position,
      ) < 3,
      "return moved the card",
    );
    await card
      .getByRole("button", { name: /查看完整过程/ })
      .evaluate((el) => (el as HTMLButtonElement).click());
    await rollout.waitFor();
    await rollout.getByText("历史阅读", { exact: true }).waitFor();
    const reopenedAnchor = await reader.locator(`[data-step-key="${anchor.key}"]`).boundingBox();
    assert(
      reopenedAnchor && Math.abs(reopenedAnchor.y - anchor.top) < 3,
      "reopening lost the detailed reading position",
    );
    await rollout.getByRole("button", { name: "← 返回", exact: true }).click();
    await rollout.waitFor({ state: "detached" });
    await card
      .locator("article")
      .filter({ hasText: "go test ./internal/domain" })
      .getByRole("button", { name: /在完整过程中查看/ })
      .click();
    await rollout.waitFor();
    await rollout.getByText("历史阅读", { exact: true }).waitFor();
    await rollout
      .locator('pre[data-scroll="output"]')
      .filter({ hasText: "ok   example/internal/domain" })
      .waitFor();
    const targetBounds = await rollout.locator('[data-target="true"]').boundingBox();
    const readerBounds = await reader.boundingBox();
    assert(
      targetBounds &&
        readerBounds &&
        targetBounds.y >= readerBounds.y - 2 &&
        targetBounds.y < readerBounds.y + readerBounds.height,
    );
    const expandedTool = rollout
      .locator("article")
      .filter({ hasText: "go test ./internal/domain" });
    const toolOutput = expandedTool.locator('pre[data-scroll="output"]');
    await toolOutput.evaluate((el) => {
      el.scrollTop = 80;
    });
    await reader.evaluate((el) => {
      el.scrollTop += 150;
    });
    const expandedTop = (await expandedTool.boundingBox())!.y;
    const outputTop = await toolOutput.evaluate((el) => el.scrollTop);
    assert(outputTop > 0, "fixture did not exercise an output scroller");
    await rollout.getByRole("button", { name: "← 返回", exact: true }).click();
    await rollout.waitFor({ state: "detached" });
    await card
      .getByRole("button", { name: /查看完整过程/ })
      .evaluate((el) => (el as HTMLButtonElement).click());
    await rollout.waitFor();
    assert(
      (await expandedTool.locator("details").first().getAttribute("open")) !== null,
      "reopening collapsed the reading step",
    );
    assert(
      Math.abs((await expandedTool.boundingBox())!.y - expandedTop) < 3,
      "reopening moved the expanded reading anchor",
    );
    assert(
      Math.abs((await toolOutput.evaluate((el) => el.scrollTop)) - outputTop) < 3,
      "reopening lost the tool output scroll position",
    );
    await page.keyboard.press("Escape");
    await rollout.waitFor({ state: "detached" });
    await card.getByRole("button", { name: /查看完整过程/ }).click();
    await rollout.waitFor();
    await page.goBack();
    await rollout.waitFor({ state: "detached" });
    assert(await card.isVisible(), "browser back left the task");
    await card.getByRole("button", { name: /查看完整过程/ }).click();
    await rollout.waitFor();
    await rollout.getByRole("button", { name: /回到最新/ }).click();
    await rollout.getByText("实时跟随", { exact: true }).waitFor();
    const newSeq = logs.at(-1)!.seq + 1;
    logs.push({
      seq: newSeq,
      native_id: `item_${newSeq}`,
      kind: "item.completed",
      body: { type: "agentMessage", text: "新到达的执行记录" },
      created_at: "2026-10-27T03:01:00Z",
    });
    await rollout.getByText("新到达的执行记录", { exact: true }).waitFor();
    assert(
      await reader.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 4),
      "live did not follow the bottom",
    );
    await reader.evaluate((el) => {
      el.scrollTop = 200;
    });
    await rollout.getByText("历史阅读", { exact: true }).waitFor();
    const historyTop = await reader.evaluate((el) => el.scrollTop);
    task.runs[0]!.observed = "finished";
    eventSeq++;
    for (const stream of streams)
      stream.write(
        `event: ledger\ndata: ${JSON.stringify({ event_seq: eventSeq, object_type: "task", object_id: taskID, kind: "task.changed", created_at: "2026-10-27T03:01:00Z" })}\n\n`,
      );
    await rollout.getByText("已结束", { exact: false }).waitFor();
    assert.equal(
      await rollout.getByText("正在执行…", { exact: true }).count(),
      0,
      "paused rollout masked formal completion",
    );
    task.runs[0]!.observed = "running";
    eventSeq++;
    for (const stream of streams)
      stream.write(
        `event: ledger\ndata: ${JSON.stringify({ event_seq: eventSeq, object_type: "task", object_id: taskID, kind: "task.changed", created_at: "2026-10-27T03:01:00Z" })}\n\n`,
      );
    await rollout.getByText("执行中", { exact: false }).waitFor();

    await page.waitForTimeout(2200);
    assert(Math.abs((await reader.evaluate((el) => el.scrollTop)) - historyTop) < 3);
    await rollout.getByRole("button", { name: /回到最新/ }).click();
    await rollout.getByText("实时跟随", { exact: true }).waitFor();
    rolloutFailure = true;
    await rollout.getByText("更多", { exact: true }).click();
    await rollout.getByRole("button", { name: "重新连接", exact: true }).click();
    await rollout.getByRole("alert").waitFor();
    assert.equal(
      await rollout.getByText("新到达的执行记录", { exact: true }).count(),
      1,
      "failure discarded read content",
    );
    rolloutFailure = false;
    await rollout.getByRole("alert").getByRole("button", { name: "重新连接" }).click();
    await rollout.getByRole("alert").waitFor({ state: "detached" });
    if (await rollout.locator("nav details[open]").count())
      await rollout.getByText("更多", { exact: true }).click();
    const commandRow = rollout.locator("article").filter({ hasText: "go test ./internal/domain" });
    if ((await commandRow.locator("details").first().getAttribute("open")) === null) {
      await commandRow.locator("summary").first().click();
    }
    await commandRow
      .locator('pre[data-scroll="output"]')
      .filter({ hasText: "ok   example/internal/domain" })
      .waitFor();
    await page.screenshot({ path: join(output, `rollout-${viewport.width}.png`), fullPage: true });
    await rollout.getByRole("button", { name: "关闭完整过程" }).click();
    await rollout.waitFor({ state: "detached" });
    logs.pop();
    assert.deepEqual(errors, []);
    await context.close();
    console.log(`PASS ${viewport.width}x${viewport.height}`);
  }

  // The first nonempty poll after an empty window can be a full newest page.
  rolloutEmpty = true;
  const backlogContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
    serviceWorkers: "block",
  });
  try {
    const backlog = await backlogContext.newPage();
    const errors: string[] = [];
    backlog.on("pageerror", (error) => errors.push(error.message));
    await backlog.goto(`http://127.0.0.1:${address.port}/?task=${taskID}`);
    const card = backlog.getByRole("region", { name: "执行过程与用量" });
    await card.getByRole("button", { name: "展开执行步骤" }).click();
    await card.getByRole("button", { name: /查看完整过程/ }).click();
    const reader = backlog.getByRole("dialog", { name: "完整执行过程", exact: true });
    await reader.getByText("尚无已采集记录。", { exact: true }).waitFor();
    rolloutEmpty = false;
    await reader.getByText("隔离记录 150", { exact: true }).waitFor();
    await reader.getByRole("button", { name: "加载更早记录", exact: true }).click();
    await reader.getByText("隔离记录 1", { exact: true }).waitFor();
    assert.equal(await reader.getByText("隔离记录 150", { exact: true }).count(), 1);
    assert.deepEqual(errors, []);
    await backlog.screenshot({ path: join(output, "empty-backlog-390.png") });
    console.log("PASS initially empty rollout retains paginated backlog");
  } finally {
    rolloutEmpty = false;
    await backlogContext.close();
  }

  // Follow newer executions by default, retaining history only after selection.
  const selectionContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
    serviceWorkers: "block",
  });
  try {
    const page = await selectionContext.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/?task=${taskID}`);
    const select = page
      .getByRole("region", { name: "执行过程与用量" })
      .getByRole("combobox", { name: "执行记录" });
    await select.waitFor();
    assert.equal(await select.inputValue(), runID);
    const appendRun = (id: string, role: string, generation: number) => {
      task.runs.push({ ...task.runs[0]!, run_id: id, role, generation });
      eventSeq++;
      for (const stream of streams)
        stream.write(
          `event: ledger\ndata: ${JSON.stringify({ event_seq: eventSeq, object_type: "task", object_id: taskID, kind: "task.changed", created_at: "2026-10-27T03:01:00Z" })}\n\n`,
        );
    };
    const acceptance = page.waitForResponse(
      (r) => new URL(r.url()).pathname === "/api/v1/runs/run_accept/rollout",
    );
    appendRun("run_accept", "accept", 1);
    await select.locator('option[value="run_accept"]').waitFor({ state: "attached" });
    await acceptance;
    assert.equal(
      await select.inputValue(),
      "run_accept",
      "default reader failed to follow acceptance",
    );
    await select.selectOption(runID);
    appendRun("run_retry", "implement", 2);
    await select.locator('option[value="run_retry"]').waitFor({ state: "attached" });
    assert.equal(
      await select.inputValue(),
      runID,
      "explicit history selection changed on a newer run",
    );
    assert(
      !reads.some((p) => p.startsWith("/api/v1/runs/run_retry/rollout")),
      "historical reader fetched the unselected new run",
    );
    await select.selectOption("run_retry");
    const next = page.waitForResponse(
      (r) => new URL(r.url()).pathname === "/api/v1/runs/run_accept_next/rollout",
    );
    appendRun("run_accept_next", "accept", 2);
    await select.locator('option[value="run_accept_next"]').waitFor({ state: "attached" });
    await next;
    assert.equal(
      await select.inputValue(),
      "run_accept_next",
      "selecting latest did not restore following",
    );
    console.log("PASS reader follows new runs, pins selected history, and resumes following");
  } finally {
    task.runs.splice(1);
    await selectionContext.close();
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
  const shortCard = boundary.getByRole("region", { name: "执行过程与用量" });
  await shortCard.getByText("隔离记录 10", { exact: true }).waitFor();
  await shortCard.getByRole("button", { name: "展开执行步骤" }).click();
  await shortCard.getByRole("button", { name: /查看完整过程/ }).click();
  const short = boundary.getByRole("dialog", { name: "完整执行过程", exact: true });
  const earlier = short.getByRole("button", { name: "加载更早记录" });
  assert.equal(await earlier.count(), 0);
  logs.push({
    seq: 11,
    native_id: "item_11",
    kind: "item.completed",
    body: { type: "agentMessage", text: "隔离记录 11" },
    created_at: "2026-10-27T03:00:00Z",
  });
  await short.getByText("隔离记录 11", { exact: true }).waitFor();
  assert.equal(await earlier.count(), 0, "live append fabricated an older history page");
  assert.equal(await short.locator("article").count(), 11);
  await boundaryContext.close();
  assert.equal(writes.length, 0);
  assert(
    !reads.some((path) => /\/runs\/[^/]+\/logs/.test(path)),
    "retired legacy log reader still fetched alongside rollout",
  );
  assert(reads.some((path) => path.includes("/rollout?before=")));
  assert(reads.some((path) => path.includes("window=total")));
  console.log(`Browser ${browser.version()}; screenshots ${output}`);
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
