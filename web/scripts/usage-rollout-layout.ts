import { navigate } from "./navigation.ts";
import { fixtureRoot } from "./navigation.ts";
/// <reference lib="dom" />
// Render the built product against isolated API fixtures; reject every write.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { resolve, join } from "node:path";
import { tmpdir } from "node:os";
import { chromium } from "playwright";
import type { TokenUsageTotals, TokenUsageView, RunLogView } from "../src/api/generated/api.ts";
const root = fixtureRoot();
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
let usageFailure = false;
let usageEmpty = false;
let usageMany = false;
let runsAvailable = true;
let eventSeq = 1;
let rolloutFailure = false;
let rolloutEmpty = false;
let historyGate: Promise<void> | undefined;
let releaseHistory: (() => void) | undefined;
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
    if (usageFailure) {
      res.statusCode = 503;
      return json({ error: { code: "unavailable", message: "Usage fixture failure" } });
    }
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
    const priced = { ...t, estimated_usd: "0.012", estimated_requests: 1 };
    const models = unknown ? ["model-fixture", "实际模型未提供"] : ["model-fixture"];
    const path = [
      { id: "wi_parent", title: "移动端体验改进" },
      { id: "wi_child", title: task.title },
    ];
    const attribution = {
      issue_path: path,
      task_id: taskID,
      task_title: task.title,
      run_id: runID,
      agent: "小林",
      agent_id: "fixture/planner",
      categories: ["software-engineer"],
      role: "implement",
    };
    const session = {
      id: "018d0000-0000-7000-8000-000000000001",
      parent_session_id: "",
      models,
      first_at: 1793066400000,
      last_at: 1793066400000,
      totals: priced,
      attributions: [attribution],
      executions: [
        {
          attribution,
          totals: priced,
          measurements: [
            { model: "model-fixture", cli: "codex", cli_version: "0.163.0", totals: priced },
          ],
        },
      ],
    };
    const view: TokenUsageView = {
      trend: [],
      revision: correction ? 2 : 1,
      as_of: 1793066400000,
      timezone: "Asia/Shanghai",
      window: url.searchParams.get("window") ?? "7d",
      start: null,
      end: 1793066400000,
      totals: priced,
      pricing: { source: "OpenRouter", currency: "USD", quoted_at: 1793066400000 },
      collection: { sources: 2, pending: 1, legacy_requests: 0, last_synced_at: 1793066400000 },
      sessions: [session],
      issues: [
        {
          id: "wi_parent",
          title: "移动端体验改进",
          parent_id: "",
          models,
          path: path.slice(0, 1),
          direct: { ...totals, requests: 0 },
          totals: priced,
          sessions: [],
        },
        {
          id: "wi_child",
          title: task.title,
          parent_id: "wi_parent",
          models,
          path,
          totals: priced,
          direct: priced,
          sessions: [session],
        },
      ],
      unattributed: { ...totals, requests: 0 },
      models: [{ name: "model-fixture", ...t }],
      roles: [{ name: "implement", ...t }],
      records: [],
      next: 0,
      more: false,
      conflicts: 0,
      coverage: "隔离虚构请求；不表示真实消费。",
    };
    if (usageEmpty) {
      view.sessions = [];
      view.issues = [];
      view.totals = { ...totals, requests: 0, total: 0, total_known: 0 };
    }
    if (usageMany) {
      view.sessions = Array.from({ length: 51 }, (_, i) => ({
        ...session,
        id: `session-page-${i}`,
        executions: [0, 1].map((part) => {
          const partial = {
            ...priced,
            requests: 1,
            input: 6000,
            input_known: 1,
            cache_read: 3000,
            cache_read_known: 1,
            cache_write: part === 0 ? 500 : 0,
            cache_write_known: part === 0 ? 1 : 0,
            output: 500,
            output_known: 1,
            total: 6500,
            total_known: 1,
            final: part === 0 ? 1 : 0,
            estimated_requests: part === 0 ? 1 : 0,
            estimated_usd: part === 0 ? "0.012" : "",
          };
          return {
            ...session.executions[0]!,
            attribution: { ...attribution, run_id: `${runID}-${part}` },
            totals: partial,
            measurements: [{ ...session.executions[0]!.measurements[0]!, totals: partial }],
          };
        }),
      }));
    }
    const selectedSession = url.searchParams.get("session");
    if (selectedSession)
      view.sessions = (view.sessions ?? []).filter((s) => s.id === selectedSession);
    return json(view);
  }
  if (path === `/api/v1/runs/${runID}/logs`)
    return json({ run_id: runID, observed: "running", logs: logs.slice(0, 100), next: 100 });
  if (/^\/api\/v1\/runs\/[^/]+\/(rollout|preview)$/.test(path)) {
    const native = path.endsWith("/preview");
    const requestedRun = path.split("/")[4]!;
    if (rolloutFailure) {
      if (native) return json({ state: "runner_unreachable", origin: "runner" });
      res.statusCode = 503;
      return json({ error: { code: "unavailable", message: "Fixture failure" } });
    }
    const reply = (page: unknown) =>
      json(
        native
          ? {
              state: rolloutEmpty ? "empty" : "ready",
              origin: "runner",
              generation: "fixture-generation",
              page,
            }
          : page,
      );
    if (native && url.searchParams.has("record")) {
      const row = logs.find((r) => r.seq === Number(url.searchParams.get("record")));
      const body = row?.body as Record<string, unknown> | undefined;
      const field = url.searchParams.get("field");
      return json({
        state: "ready",
        origin: "runner",
        generation: "fixture-generation",
        content: {
          text:
            field === "raw"
              ? JSON.stringify(row)
              : field === "output"
                ? (body?.aggregatedOutput ?? "")
                : field === "input"
                  ? (body?.command ?? "")
                  : (body?.text ?? ""),
          next_offset: 0,
          more: false,
        },
      });
    }
    if (rolloutEmpty)
      return reply({
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
    if (before) await historyGate;
    const selected = before
      ? logs.filter((r) => r.seq < before).slice(-100)
      : after
        ? [
            ...logs.filter((r) => r.seq > after),
            ...(typeof logs[99]?.body === "object" &&
            JSON.stringify(logs[99]?.body).includes("较早执行回复")
              ? [logs[99]!]
              : []),
          ].slice(0, 100)
        : logs.slice(-100);
    const first = selected[0]?.seq ?? before;
    const last = selected.at(-1)?.seq ?? after;
    return reply({
      run_id: requestedRun,
      observed: task.runs.find((run) => run.run_id === requestedRun)?.observed ?? "running",
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
    const file = resolve(root, path.split("/").at(-1)?.includes(".") ? `.${path}` : "index.html");
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
    await page.goto(`http://127.0.0.1:${address.port}/diagnostics/main`);
    if (process.env.TEND_LAYOUT_FONT)
      await page.addStyleTag({
        content:
          "@font-face{font-family:FixtureCJK;src:url(/fixture-font)}:root{--font:FixtureCJK,sans-serif}",
      });
    if (viewport.width <= 760) {
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "Issues", exact: true })
        .click();
      await navigate(page, "/tasks");
      await page
        .locator("main details > summary")
        .filter({ hasText: /^资源与记录$/ })
        .click();
    }
    if (viewport.width <= 760)
      await page.getByRole("button", { name: "Token 用量", exact: true }).click();
    else await page.getByRole("link", { name: "Token 用量", exact: true }).click();
    const panel = page.getByRole("region", { name: "Token 用量", exact: true });
    await panel.getByText("14,000", { exact: true }).waitFor();
    assert.equal(await page.getByRole("heading", { name: "Token 用量", exact: true }).count(), 1);
    assert.equal(
      await panel.getByRole("combobox", { name: "用量时间范围", exact: true }).inputValue(),
      "total",
    );

    assert.equal(
      await panel.getByRole("tab", { name: "按 Issue", exact: true }).getAttribute("aria-selected"),
      "true",
    );
    await page.screenshot({
      path: join(output, `usage-default-${viewport.width}.png`),
      fullPage: true,
    });
    const line = await panel.locator('[class*="tokens"]').first().innerText();
    assert(line.indexOf("输入") < line.indexOf("缓存"));
    assert(line.indexOf("缓存写入") < line.indexOf("输出"));
    assert(!/≈|约等于/.test(await panel.innerText()));
    await panel.getByRole("button", { name: "展开移动端体验改进" }).click();
    await panel.getByRole("button", { name: task.title, exact: false }).click();
    const usageDetail = page.getByRole("dialog", { name: task.title, exact: true });
    await usageDetail.getByText("Codex CLI · 0.163.0", { exact: true }).waitFor();
    assert.equal(
      await usageDetail.getByRole("button", { name: "小林", exact: true }).getAttribute("title"),
      "Category: software-engineer",
    );
    assert(!/thread |turn |原生请求/.test(await usageDetail.innerText()));
    await usageDetail.screenshot({ path: join(output, `usage-detail-${viewport.width}.png`) });
    await usageDetail.getByRole("button", { name: `关闭${task.title}`, exact: true }).click();
    await panel.getByRole("tab", { name: "按 Session", exact: true }).click();
    assert(new URL(page.url()).searchParams.get("group_by") === "session");
    await panel.getByText("14,000", { exact: true }).waitFor();
    await page.screenshot({
      path: join(output, `usage-session-${viewport.width}.png`),
      fullPage: true,
    });
    await panel.getByRole("tab", { name: "按 Issue", exact: true }).click();
    await panel.getByRole("button", { name: "收起移动端体验改进" }).click();
    await panel.getByRole("button", { name: "筛选", exact: true }).click();
    correction = true;
    await panel.getByRole("button", { name: "刷新", exact: true }).click();
    await panel.getByText("13,000", { exact: true }).waitFor();
    if (viewport.width === 390 || viewport.width === 1440) {
      await page.setViewportSize({
        width: viewport.width,
        height: viewport.width === 1440 ? 900 : 844,
      });
      await page.screenshot({ path: join(output, `s2-usage-${viewport.width}.png`) });
      await page.setViewportSize(viewport);
    }
    await page.screenshot({
      path: join(output, `usage-known-${viewport.width}.png`),
      fullPage: true,
    });
    if (viewport.width === 390) {
      usageFailure = true;
      await panel.getByRole("button", { name: "刷新", exact: true }).click();
      await panel.getByRole("alert").filter({ hasText: "更新失败" }).waitFor();
      await panel.getByText("13,000", { exact: true }).waitFor();
      await panel.getByLabel("Issue ID", { exact: true }).fill("unmatched-issue");
      await panel.getByRole("alert").filter({ hasText: "当前筛选暂无可显示的数据" }).waitFor();
      assert.equal(await panel.getByText("13,000", { exact: true }).count(), 0);
      assert.equal(new URL(page.url()).pathname, "/usage");
      assert.equal(new URL(page.url()).searchParams.get("issue"), "unmatched-issue");
      usageFailure = false;
      usageEmpty = true;
      await panel.getByRole("button", { name: "重试", exact: true }).click();
      await panel.getByText("当前筛选下暂无已记录用量。", { exact: true }).waitFor();
      usageEmpty = false;
      usageMany = true;
      await panel.getByRole("button", { name: "清除筛选", exact: true }).click();
      await panel.getByRole("tab", { name: "按 Session", exact: true }).click();
      await panel.getByRole("button", { name: "加载更多", exact: true }).waitFor();
      assert.equal(await panel.getByRole("row").count(), 50);
      await panel.getByRole("button", { name: "加载更多", exact: true }).click();
      assert.equal(await panel.getByRole("row").count(), 51);
      assert.equal(await panel.getByRole("button", { name: "加载更多", exact: true }).count(), 0);
      await panel.getByRole("button", { name: "session-page-0", exact: false }).focus();
      await page.keyboard.press("Enter");
      const keyboardDetail = page.getByRole("dialog", { name: "session-page-0", exact: true });
      await keyboardDetail.getByText("Codex CLI · 0.163.0", { exact: true }).first().waitFor();
      assert.equal(await keyboardDetail.getByText(/6,500 Token/).count(), 2);
      assert.equal(
        await keyboardDetail.getByText("6,500", { exact: true }).filter({ visible: true }).count(),
        0,
      );
      await page.keyboard.press("Escape");
      await keyboardDetail.waitFor({ state: "hidden" });
      usageMany = false;
      await panel.getByRole("tab", { name: "按 Issue", exact: true }).click();
      await panel.getByText("13,000", { exact: true }).waitFor();
      console.log("PASS usage filter isolation, failure recovery, pagination and keyboard detail");
    }
    unknown = true;
    await panel.getByRole("button", { name: "刷新", exact: true }).click();
    await panel
      .locator('[class*="tokens"]')
      .first()
      .getByText("未提供", { exact: true })
      .first()
      .waitFor();
    await panel
      .getByRole("button", { name: "1 个已知模型 · 含未记录", exact: true })
      .first()
      .waitFor();
    assert(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `usage-${viewport.width}.png`), fullPage: true });
    await page.goBack();
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
    await card
      .getByText(/\d+ 个步骤/)
      .first()
      .waitFor();
    assert.equal(await card.locator("article").count(), 0);
    await card.screenshot({ path: join(output, `rollout-card-${viewport.width}.png`) });
    await card.getByRole("button", { name: "展开执行步骤" }).click();
    await card.getByText("隔离记录 150", { exact: true }).first().waitFor();
    assert.equal(await card.locator("article").count(), 6);
    await card.getByRole("button", { name: "本次用量", exact: true }).click();
    const runUsage = card.getByRole("region", { name: "本次执行 Token 用量" });
    await runUsage.getByText("42", { exact: true }).waitFor();
    assert.equal(
      await runUsage.getByRole("combobox", { name: "用量时间范围", exact: true }).inputValue(),
      "total",
    );
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
    await card.getByRole("button", { name: /查看完整过程/ }).click();
    const rollout = page.getByRole("region", { name: "执行过程监控", exact: true });
    const reader = rollout.locator("[data-trace-scroll]");
    const rows = rollout
      .getByRole("table", { name: "执行记录", exact: true })
      .locator("[data-seq]");
    await rollout.getByText("跟随最新", { exact: true }).waitFor();
    assert(page.url().includes(`/tasks/${taskID}/runs/${runID}`));
    assert.equal(await page.getByRole("dialog", { name: "完整执行过程" }).count(), 0);
    assert.match(
      await rollout.getByRole("button", { name: "记录来源", exact: true }).innerText(),
      /runner 原生记录/,
    );
    await rows.filter({ hasText: "隔离记录 150" }).waitFor();
    assert.equal(await rows.count(), 99);
    const command = rows.filter({ hasText: "go test ./internal/domain" });
    assert.equal(await command.count(), 1, "start and result became duplicate steps");
    await command.click();
    await rollout.getByRole("tab", { name: "输出", exact: true }).click();
    await rollout
      .getByRole("tabpanel")
      .getByText(/ok {3}example\/internal\/domain/)
      .waitFor();
    await rollout.getByRole("tab", { name: "原始记录", exact: true }).click();
    await rollout
      .getByRole("tabpanel")
      .getByText(/aggregatedOutput/)
      .waitFor();
    await page.screenshot({ path: join(output, `rollout-details-${viewport.width}.png`) });
    await rollout.getByRole("button", { name: "关闭记录详情", exact: true }).click();
    // A real wheel gesture enables boundary loading; prepending retains the visible row.
    historyGate = new Promise<void>((resolve) => {
      releaseHistory = resolve;
    });
    await reader.hover();
    await page.mouse.wheel(0, -1);
    await reader.evaluate((el) => {
      el.scrollTop = 32;
    });
    await page.waitForTimeout(50);
    const anchor = await rows.evaluateAll((nodes) => {
      const top = document.querySelector("[data-trace-scroll]")!.getBoundingClientRect().top + 36;
      const row = nodes.find((node) => node.getBoundingClientRect().top > top)!;
      return { seq: (row as HTMLElement).dataset.seq, top: row.getBoundingClientRect().top };
    });
    releaseHistory!();
    historyGate = undefined;
    await reader.locator('[data-seq="1"]').waitFor({ state: "attached" });
    const anchorRow = reader.locator(`[data-seq="${anchor.seq}"]`);
    assert(
      Math.abs((await anchorRow.boundingBox())!.y - anchor.top) < 3,
      "prepend moved the reading anchor",
    );
    assert.equal(await rows.count(), 149);
    assert.equal(await rows.filter({ hasText: "隔离记录 150" }).count(), 1);
    assert.equal(await rollout.getByRole("button", { name: "加载更早记录" }).count(), 0);
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    const historyTop = await reader.evaluate((el) => el.scrollTop);
    await page.waitForTimeout(2200);
    assert(Math.abs((await reader.evaluate((el) => el.scrollTop)) - historyTop) < 3);
    await rollout.getByRole("button", { name: "返回任务", exact: true }).click();
    await rollout.waitFor({ state: "detached" });
    await page.goForward();
    await anchorRow.waitFor();
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    assert(
      Math.abs((await anchorRow.boundingBox())!.y - anchor.top) < 3,
      `browser forward lost the historical reading position: ${JSON.stringify({ anchor, restored: await anchorRow.boundingBox(), scroll: await reader.evaluate((el) => el.scrollTop), reads: reads.filter((path) => path.includes("/preview?")).slice(-4) })}`,
    );
    assert.equal(
      await rows.filter({ hasText: "隔离记录 150" }).count(),
      0,
      "history restoration unexpectedly fetched the newest page",
    );
    await rollout.getByRole("button", { name: "回到最新", exact: true }).click();
    await rollout.getByText("跟随最新", { exact: true }).waitFor();
    for (const size of [{ width: 390, height: 568 }, { width: 1440, height: 900 }, viewport]) {
      await page.setViewportSize(size);
      await page.waitForFunction(() => {
        const el = document.querySelector<HTMLElement>("[data-trace-scroll]")!;
        return el.scrollHeight - el.scrollTop - el.clientHeight < 4;
      });
      await rollout.getByText("跟随最新", { exact: true }).waitFor();
    }
    const newSeq = logs.at(-1)!.seq + 1;
    logs.push({
      seq: newSeq,
      native_id: `item_${newSeq}`,
      kind: "item.completed",
      body: { type: "agentMessage", text: "新到达的执行记录" },
      created_at: "2026-10-27T03:01:00Z",
    });
    await rows.filter({ hasText: "新到达的执行记录" }).waitFor();
    assert(
      await reader.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 4),
      "live did not follow the bottom",
    );
    await reader.hover();
    await page.mouse.wheel(0, -100);
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    task.runs[0]!.observed = "finished";
    eventSeq++;
    for (const stream of streams)
      stream.write(
        `event: ledger\ndata: ${JSON.stringify({ event_seq: eventSeq, object_type: "task", object_id: taskID, kind: "task.changed" })}\n\n`,
      );
    await rollout.locator("header").getByText("已结束", { exact: true }).waitFor();
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    await rollout.getByRole("button", { name: "回到最新", exact: true }).click();
    await page.waitForFunction(() => {
      const el = document.querySelector<HTMLElement>("[data-trace-scroll]")!;
      return el.scrollHeight - el.scrollTop - el.clientHeight < 4;
    });
    rolloutFailure = true;
    await rollout.getByRole("alert").waitFor();
    assert.equal(
      await rows.filter({ hasText: "新到达的执行记录" }).count(),
      1,
      "failure discarded read content",
    );
    rolloutFailure = false;
    await rollout.getByRole("alert").getByRole("button", { name: "重新读取", exact: true }).click();
    await rollout.getByRole("alert").waitFor({ state: "detached" });
    await rollout.getByRole("button", { name: "记录来源", exact: true }).click();
    await page.getByRole("menuitem", { name: "服务端已采集日志", exact: true }).click();
    await rows.filter({ hasText: "新到达的执行记录" }).waitFor();
    assert.match(page.url(), /source=logs/);
    assert.match(
      await rollout.getByRole("button", { name: "记录来源", exact: true }).innerText(),
      /已采集日志/,
    );
    await reader.hover();
    await page.mouse.wheel(0, -100);
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    const logAnchor = await rows.evaluateAll((nodes) => {
      const top = document.querySelector("[data-trace-scroll]")!.getBoundingClientRect().top + 36;
      const row = nodes.find((node) => node.getBoundingClientRect().top > top)!;
      return { seq: (row as HTMLElement).dataset.seq, top: row.getBoundingClientRect().top };
    });
    await rollout.getByRole("button", { name: "返回任务", exact: true }).click();
    await rollout.waitFor({ state: "detached" });
    await page.goForward();
    await reader.locator(`[data-seq="${logAnchor.seq}"]`).waitFor();
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    assert.match(
      await rollout.getByRole("button", { name: "记录来源", exact: true }).innerText(),
      /已采集日志/,
    );
    assert(
      Math.abs(
        (await reader.locator(`[data-seq="${logAnchor.seq}"]`).boundingBox())!.y - logAnchor.top,
      ) < 3,
      "browser forward lost the collected-log reading position",
    );
    assert(await rollout.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `rollout-${viewport.width}.png`), fullPage: true });
    await rollout.getByRole("button", { name: "返回任务", exact: true }).click();
    await rollout.waitFor({ state: "detached" });
    await card.getByRole("button", { name: "展开执行步骤" }).waitFor();
    task.runs[0]!.observed = "running";
    await card.getByRole("button", { name: "展开执行步骤" }).click();
    await card
      .locator("article")
      .filter({ hasText: "go test ./internal/domain" })
      .getByRole("button", { name: /在完整过程中查看/ })
      .click();
    await rollout.waitFor();
    assert.match(page.url(), /source=logs/);
    await rollout.getByRole("tab", { name: "输出", exact: true }).click();
    await rollout
      .getByRole("tabpanel")
      .getByText(/ok {3}example\/internal\/domain/)
      .waitFor();
    await rollout.getByRole("button", { name: "关闭记录详情", exact: true }).click();
    await rollout.getByText("阅读历史", { exact: true }).waitFor();
    await page.goBack();
    await rollout.waitFor({ state: "detached" });
    await card.waitFor({ state: "visible" });
    assert.equal(new URL(page.url()).pathname, `/tasks/${taskID}`, "browser back left the task");
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
    const reader = backlog.getByRole("region", { name: "执行过程监控", exact: true });
    await reader.getByText("尚无原生过程记录。", { exact: true }).waitFor();
    rolloutEmpty = false;
    await reader.getByText("隔离记录 150", { exact: true }).waitFor();
    await reader.locator("[data-trace-scroll]").hover();
    await backlog.mouse.wheel(0, -100000);
    await reader.getByText("隔离记录 1", { exact: true }).waitFor({ state: "attached" });
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
      .getByRole("button", { name: "执行记录", exact: true });
    await select.waitFor();
    assert.match(await select.innerText(), /实施/);
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

    await acceptance;
    assert.equal(
      await select.innerText().then((text) => text.includes("验收")),
      true,
      "default reader failed to follow acceptance",
    );
    await select.click();
    await page.getByRole("menuitem", { name: /实施/ }).click();
    appendRun("run_retry", "implement", 2);
    await select.click();
    await page.getByRole("menuitem", { name: /第 3 次/ }).waitFor();
    await page.keyboard.press("Escape");
    assert.equal(
      await select.innerText().then((text) => text.includes("实施")),
      true,
      "explicit history selection changed on a newer run",
    );
    assert(
      !reads.some((p) => p.startsWith("/api/v1/runs/run_retry/rollout")),
      "historical reader fetched the unselected new run",
    );
    await select.click();
    await page.getByRole("menuitem", { name: /第 3 次/ }).click();
    const next = page.waitForResponse(
      (r) => new URL(r.url()).pathname === "/api/v1/runs/run_accept_next/rollout",
    );
    appendRun("run_accept_next", "accept", 2);

    await next;
    assert.equal(
      await select.innerText().then((text) => text.includes("验收")),
      true,
      "selecting latest did not restore following",
    );
    console.log("PASS reader follows new runs, pins selected history, and resumes following");
  } finally {
    task.runs.splice(1);
    await selectionContext.close();
  }

  // The inspector can remain on a record outside the newly reconstructed ledger page.
  for (const source of ["native", "logs"] as const) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${address.port}/tasks/${taskID}`);
      await page
        .getByRole("region", { name: "执行过程与用量", exact: true })
        .getByRole("button", { name: /查看完整过程/ })
        .waitFor();
      await navigate(
        page,
        `/tasks/${taskID}/runs/${runID}${source === "logs" ? "?source=logs" : ""}`,
      );
      const monitor = page.getByRole("region", { name: "执行过程监控", exact: true });
      const pane = monitor.locator("[data-trace-scroll]");
      await pane.locator('[data-seq="145"]').click();
      await monitor.getByRole("tab", { name: "原始记录", exact: true }).click();
      await monitor
        .getByRole("tabpanel")
        .getByText(/aggregatedOutput/)
        .waitFor();
      await pane.hover();
      await page.mouse.wheel(0, -1);
      await monitor.getByText("阅读历史", { exact: true }).waitFor();
      await pane.locator('[data-seq="60"]').scrollIntoViewIfNeeded();
      await pane.evaluate((el) => {
        el.scrollTop +=
          el.querySelector('[data-seq="60"]')!.getBoundingClientRect().top -
          el.getBoundingClientRect().top -
          40;
      });
      await page.waitForTimeout(80);
      const anchorY = (await pane.locator('[data-seq="60"]').boundingBox())!.y;
      await monitor.getByRole("button", { name: "返回任务", exact: true }).click();
      await monitor.waitFor({ state: "detached" });
      await page.goForward();
      await pane.locator('[data-seq="60"]').waitFor();
      await monitor
        .getByRole("tabpanel")
        .getByText(/aggregatedOutput/)
        .waitFor();
      assert.equal(
        await pane.locator('[data-seq="145"]').count(),
        0,
        "fixture did not put the selection outside the restored page",
      );
      assert.equal(
        await monitor
          .getByRole("tab", { name: "原始记录", exact: true })
          .getAttribute("aria-selected"),
        "true",
      );
      assert(
        Math.abs((await pane.locator('[data-seq="60"]').boundingBox())!.y - anchorY) < 3,
        "retained inspector displaced the historical anchor",
      );
      assert.deepEqual(errors, []);
      console.log(
        `PASS ${source}: inspector outside restored window retains selection and raw tab`,
      );
    } finally {
      await context.close();
    }
  }
  // An explicit latest request supersedes a record-specific entry on later Forward navigation.
  for (const source of ["native", "logs"] as const) {
    const context = await browser.newContext({
      viewport: { width: 390, height: 844 },
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${address.port}/tasks/${taskID}`);
      await page
        .getByRole("region", { name: "执行过程与用量", exact: true })
        .getByRole("button", { name: /查看完整过程/ })
        .waitFor();
      await navigate(
        page,
        `/tasks/${taskID}/runs/${runID}?record=60${source === "logs" ? "&source=logs" : ""}`,
      );
      const monitor = page.getByRole("region", { name: "执行过程监控", exact: true });
      const pane = monitor.locator("[data-trace-scroll]");
      await monitor.getByRole("button", { name: "关闭记录详情", exact: true }).click();
      await monitor.getByRole("button", { name: "回到最新", exact: true }).click();
      await pane.locator('[data-seq="150"]').waitFor();
      assert.equal(new URL(page.url()).searchParams.get("record"), null);
      await monitor.getByRole("button", { name: "返回任务", exact: true }).click();
      await monitor.waitFor({ state: "detached" });
      await page.goForward();
      await pane.locator('[data-seq="150"]').waitFor();
      await monitor.getByText("跟随最新", { exact: true }).waitFor();
      assert.deepEqual(errors, []);
      console.log(`PASS ${source}: explicit latest survives Forward from a record-specific entry`);
    } finally {
      await context.close();
    }
  }

  const originalLogs = [...logs];
  // Repeated parent/child turn identities are distinct contiguous table groups.
  for (const source of ["native", "logs"] as const) {
    logs.splice(
      0,
      logs.length,
      ...Array.from({ length: 150 }, (_, i) => ({
        seq: i + 1,
        native_id: `interleaved_${i + 1}`,
        kind: "item.completed",
        body: {
          thread_id: Math.floor(i / 30) % 2 ? "child" : "parent",
          turn_id: "turn",
          data: { type: "agentMessage", text: `交错记录 ${i + 1}` },
        },
        created_at: "2026-10-27T03:00:00Z",
      })),
    );
    const context = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.goto(
        `http://127.0.0.1:${address.port}/tasks/${taskID}/runs/${runID}${source === "logs" ? "?source=logs" : ""}`,
      );
      const pane = page
        .getByRole("region", { name: "执行过程监控", exact: true })
        .locator("[data-trace-scroll]");
      await pane.locator('[data-seq="150"]').waitFor();
      await pane.hover();
      await page.mouse.wheel(0, -100000);
      await pane.locator('[data-seq="1"]').waitFor({ state: "attached" });
      const sequences = await pane
        .locator("[data-seq]")
        .evaluateAll((rows) => rows.map((row) => Number((row as HTMLElement).dataset.seq)));
      assert.deepEqual(
        sequences,
        Array.from({ length: 150 }, (_, i) => i + 1),
        "interleaved history created duplicated or misordered table rows",
      );
      assert.deepEqual(errors, []);
      console.log(`PASS ${source}: interleaved turn pagination has unique ordered rows`);
    } finally {
      await context.close();
    }
  }
  // Following a busy run retains an inspected row after the bounded ledger evicts it.
  for (const source of ["native", "logs"] as const) {
    for (const tool of [false, true]) {
      logs.splice(
        0,
        logs.length,
        ...Array.from({ length: 150 }, (_, i) => ({
          seq: i + 1,
          native_id: `stream_${i + 1}`,
          kind: "item.completed",
          body: { type: "agentMessage", text: `流式记录 ${i + 1}` },
          created_at: "2026-10-27T03:00:00Z",
        })),
      );
      if (tool)
        logs[149] = {
          ...logs[149]!,
          kind: "native.item_started",
          body: {
            thread_id: "stream-session",
            turn_id: "stream-turn",
            type: "commandExecution",
            command: "retained command",
            status: "inProgress",
          },
        };
      const width = tool ? 390 : 1440;
      const context = await browser.newContext({
        viewport: { width, height: 900 },
        serviceWorkers: "block",
      });
      try {
        const page = await context.newPage();
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        await page.goto(
          `http://127.0.0.1:${address.port}/tasks/${taskID}/runs/${runID}${source === "logs" ? "?source=logs" : ""}`,
        );
        const monitor = page.getByRole("region", { name: "执行过程监控", exact: true });
        const pane = monitor.locator("[data-trace-scroll]");
        await pane.locator('[data-seq="150"]').press("Enter");
        await monitor.getByRole("tab", { name: "原始记录", exact: true }).click();
        await monitor
          .getByRole("tabpanel")
          .getByText(/stream_150/)
          .waitFor();
        assert.equal(await monitor.getByText("跟随最新", { exact: true }).count(), 1);
        for (let seq = 151; seq <= 550; seq++)
          logs.push({
            seq,
            native_id: `stream_${seq}`,
            kind: "item.completed",
            body: { type: "agentMessage", text: `流式记录 ${seq}` },
            created_at: "2026-10-27T03:01:00Z",
          });
        await pane.locator('[data-seq="550"]').waitFor({ state: "attached" });
        assert.equal(
          await pane.locator('[data-seq="150"]').count(),
          0,
          "fixture did not evict the selected row",
        );
        assert.equal(await pane.locator("[data-seq]").count(), 400);
        await monitor
          .getByRole("tabpanel")
          .getByText(/stream_150/)
          .waitFor();
        assert.equal(
          await monitor
            .getByRole("tab", { name: "原始记录", exact: true })
            .getAttribute("aria-selected"),
          "true",
        );
        if (tool) {
          logs.push({
            seq: 551,
            native_id: "stream_150",
            kind: "item.completed",
            body: {
              thread_id: "stream-session",
              turn_id: "stream-turn",
              type: "commandExecution",
              command: "retained command",
              status: "completed",
              aggregatedOutput: "retained command completed",
            },
            created_at: "2026-10-27T03:01:01Z",
          });
          await monitor
            .getByRole("tabpanel")
            .getByText(/retained command completed/)
            .waitFor();
        }
        assert.deepEqual(errors, []);
        console.log(
          `PASS ${source} ${width}: live eviction retains inspector${tool ? " and later tool result" : ""}`,
        );
      } finally {
        await context.close();
      }
    }
  }
  logs.splice(0, logs.length, ...originalLogs);

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
  await shortCard
    .getByText(/\d+ 个步骤/)
    .first()
    .waitFor();
  await shortCard.getByRole("button", { name: "展开执行步骤" }).click();
  await shortCard.getByText("隔离记录 10", { exact: true }).first().waitFor();
  await shortCard.getByRole("button", { name: /查看完整过程/ }).click();
  const short = boundary.getByRole("region", { name: "执行过程监控", exact: true });
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
  assert.equal(await short.locator("[data-seq]").count(), 11);
  assert.equal(await short.getByText("向上滚动读取更早记录", { exact: true }).count(), 0);
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
  releaseHistory?.();
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
