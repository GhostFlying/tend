/// <reference lib="dom" />
// A deterministic all-route tour; no model, deployment or external API is used.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile, writeFile, readdir } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot, navigate } from "./navigation.ts";
import { issueBoardFixture, issueView } from "./issue-fixture.ts";
import type {
  IssueView,
  TaskDetail,
  AttentionView,
  MessagingMessage,
} from "../src/api/generated/api.ts";

const output = process.env.TEND_LAYOUT_OUTPUT ?? "/tmp/tend-ui-tour";
await mkdir(output, { recursive: true });
const root = fixtureRoot();
const now = Date.now();
const task: TaskDetail = {
  task_id: "task-tour",
  title: "核对发布步骤与回滚入口",
  project: "sample",
  lifecycle: "running",
  block_reason: "none",
  control_mode: "managed",
  control_revision: 1,
  spec_version: 1,
  rating: "p1",
  workspace: "",
  next_implement_generation: 2,
  actions: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
  spec: { goal: "整理可重复核对的发布说明", acceptance: ["独立验收可复现"] },
  execution: { state: "running", run_id: "run-tour", role: "implement" },
  runs: [
    {
      run_id: "run-tour",
      role: "implement",
      generation: 1,
      spec_version: 1,
      host: "runner-example",
      observed: "running",
    },
  ],
};
const completedTask: TaskDetail = {
  ...task,
  task_id: "task-completed",
  title: "补齐验收设备清单",
  lifecycle: "completed",
  execution: { state: "completed", run_id: "run-completed", role: "implement" },
  runs: [{ ...task.runs![0]!, run_id: "run-completed", observed: "finished" }],
};
const statuses = ["backlog", "todo", "in_progress", "in_review", "done", "cancelled"] as const;
const issues: IssueView[] = statuses.map((status, index) => ({
  ...issueView(),
  issue_id: `issue-${status}`,
  state: status === "done" ? "closed" : status === "cancelled" ? "cancelled" : "active",
  plan: {
    title:
      index === 0
        ? "整理多平台发布流程、验收证据以及异常情况下可重复执行的完整回滚步骤"
        : ["", "核对发布入口", "发布流程整理", "独立验收发布说明", "已完成发布说明", "暂停旧方案"][
            index
          ]!,
    project: "sample",
    intent: "使发布、验收与回滚步骤清晰可追踪。",
    stages: [
      { stage_id: "prepare", title: "整理说明", status: "done" },
      { stage_id: "review", title: "独立核对", status: "active" },
    ],
  },
  work_status: status,
  tasks: status === "in_progress" ? [task] : [],
  created_at: now,
  updated_at: now,
  progress: {
    issue_revision: 1,
    summary: "发布说明已整理，正在补齐回滚验证证据。",
    next_step: "核对异常分支的操作入口",
    updated_at: now,
    updated_by: { kind: "owner", ref: "示例负责人" },
  },
}));
issues.push({
  ...issues[2]!,
  issue_id: "issue-child",
  parent_issue_id: issues[2]!.issue_id,
  plan: { ...issues[2]!.plan, title: "补齐回滚验证证据" },
  tasks: [],
});
const attentions: AttentionView[] = (["decision", "action", "information"] as const).map(
  (kind) => ({
    attention_id: `attention-${kind}`,
    issue_id: issues[2]!.issue_id,
    revision: 1,
    kind,
    title: {
      decision: "确认灰度发布范围",
      action: "提供验收设备清单",
      information: "发布说明已更新",
    }[kind],
    status: "open",
    source_message_id: "source-tour",
    latest_action_reminders: false,
    raised_by: { kind: "main_agent", subject: "example" },
    created_at: now,
    updated_at: now,
    ...(kind === "decision"
      ? { question_version: 1, question_options: ["小范围灰度", "完整发布范围"] }
      : {}),
  }),
);
const host = {
  host_id: "runner-example",
  online: true,
  slots: 4,
  open_runs: 1,
  worker: "orca",
  os: "linux",
  arch: "amd64",
  labels: [],
};
const upgrade = {
  upgrade_id: "upgrade-tour",
  release_id: "release-example",
  status: "running",
  phase: "runners",
  updated_at: now,
  hosts: [],
};
const conversation = {
  conversation_id: "consultation-tour",
  title: "发布与回滚流程的完整核对咨询以及风险确认",
  kind: "consultation",
  agent_ref: "hosted/example",
  status: "idle",
  revision: 1,
  model: "example-model",
  effort: "high",
  last_message_at: now,
  last_message_seq: 0,
  preview: "",
};
const trigger = {
  message_id: "owner-tour",
  seq: 1,
  kind: "message",
  author: "owner",
  body: "请梳理发布与回滚流程，并核对证据。",
  created_at: new Date(now).toISOString(),
  state: "replied",
};
const tools = ["issues_read", "tasks_read", "repos_list"].map((name, index) => ({
  message_id: `tool-${index}`,
  seq: index + 2,
  kind: "message",
  author: "main_agent",
  reply_to: trigger.message_id,
  created_at: trigger.created_at,
  tool: {
    turn_id: "turn-tour",
    name,
    status: "completed",
    updated_at: trigger.created_at,
    input: "{}",
    output: "已核对相关记录",
  },
}));
const reply = {
  ...trigger,
  message_id: "reply-tour",
  seq: 5,
  author: "main_agent",
  body: "发布说明已经整理。请确认灰度范围，再补齐回滚验证证据。",
  reply_to: trigger.message_id,
};
const totals = {
  requests: 2,
  final: 2,
  input: 8000,
  input_known: 2,
  output: 1000,
  output_known: 2,
  total: 9000,
  total_known: 2,
  cache_read: 1000,
  cache_read_known: 2,
  cache_write: 0,
  cache_write_known: 2,
};
const imQuestion: MessagingMessage = {
  message_id: "message-im-question",
  sequence: 1,
  conversation_id: "",
  content_revision: 1,
  author: "system",
  body: "先验收手机还是桌面？",
  channel_id: "",
  direction: "publication",
  processing: "processed",
  created_at: now,
  attachments: [],
  issue_ids: [],
  source_message_ids: [],
  interaction: {
    interaction_id: "interaction-tour",
    object_id: "decision-tour",
    object_type: "decision",
    version: 1,
    question: "先验收手机还是桌面？",
    text_allowed: true,
    state: "open",
    options: [
      { option_id: "mobile", label: "手机优先" },
      { option_id: "desktop", label: "桌面优先" },
    ],
  },
};
const imStreams = new Set<ServerResponse>();
let imSequence = 1;
let imHideQuestion = false;
function imChanged() {
  imSequence++;
  for (const stream of imStreams)
    stream.write(
      `event: ledger\ndata: ${JSON.stringify({ event_seq: imSequence, object_type: "message", object_id: imQuestion.message_id, kind: "messaging.interaction_changed" })}\n\n`,
    );
}
const streams = new Set<ServerResponse>();
const unexpected: string[] = [];
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const p = url.pathname;
  const json = (value: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(issueBoardFixture(req.url, value)));
  };
  if (p === "/api/v1/events" || p === "/api/v1/messaging/events") {
    res.setHeader("Content-Type", "text/event-stream");
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    if (p.includes("/messaging/")) imStreams.add(res);
    res.on("close", () => imStreams.delete(res));
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (p === "/api/v1/session")
    return json({
      token: "tour-token",
      login: "owner",
      client_id: "tour",
      scope: "owner",
      deployment_id: "tour",
    });
  if (p === "/api/v1/snapshot")
    return json({
      event_seq: 1,
      tasks: [task, completedTask],
      hosts: [host],
      projects: [{ name: "sample", title: "示例项目", weight: 1, repos: ["sample"] }],
      queue: [],
      decisions: [],
      upgrade,
    });
  if (p === "/api/v1/messaging/messages")
    return json({ items: imHideQuestion ? [] : [imQuestion], next: 1, more: false });
  if (p === "/api/v1/messaging/messages/message-im-question") return json(imQuestion);
  if (p === "/api/v1/conversation") return json({ items: [trigger, reply], more: false });
  if (p.endsWith("/transcript"))
    return json({
      blocks: [{ position: 1, trigger, items: [...tools, reply], more: false }],
      event_seq: 1,
      more: false,
    });
  if (p.endsWith("/settings"))
    return json({
      enabled: true,
      available: true,
      revision: 1,
      model: "example-model",
      effort: "high",
      models: [
        {
          id: "example-model",
          label: "示例模型",
          supported_efforts: ["high"],
          default_effort: "high",
        },
      ],
    });
  if (p === "/api/v1/conversation/handoff")
    return json({ enabled: false, revision: 1, idle_minutes: 30 });
  if (p === "/api/v1/conversation/queue")
    return json({ items: [], enabled: true, event_seq: 1, total: 0 });
  if (p.endsWith("/usage"))
    return json({
      available: false,
      compact_supported: false,
      measurement: "unavailable",
      sampled_at: now,
      updated_at: now,
    });
  if (p.endsWith("/compactions")) return json({ items: [] });
  if (p === "/api/v1/issue-notification-settings") return json({ revision: 1, delay_minutes: 3 });
  if (p === "/api/v1/agent-conversations")
    return json({ items: [conversation], runtime_available: true });
  if (p.startsWith("/api/v1/agent-conversations/"))
    return json({ ...conversation, conversation_id: decodeURIComponent(p.split("/")[4]!) });
  if (p === "/api/v1/categories")
    return json({
      items: [
        {
          category_id: "planner",
          title: "统筹",
          description: "统筹目标与下一步",
          revision: 1,
          state: "active",
        },
      ],
      more: false,
    });
  if (p === "/api/v1/hosted-agents")
    return json({
      configuration_revision: 1,
      state: "registered_staged",
      main_agent_id: "example",
      projection: {
        execution_environment_id: "hosted",
        format_version: 1,
        source_kind: "local_file",
        runtimes: [{ runtime_id: "hosted", revision: 1, kind: "hosted-manager", cli: "traex" }],
        agents: [
          {
            identity: { execution_environment_id: "hosted", local_id: "example" },
            revision: 1,
            display_name: "示例负责人",
            runtime_binding_ref: { runtime_id: "hosted", revision: 1 },
            categories: ["planner"],
            purposes: ["main", "issue_manager", "consultation"],
            enabled: true,
            availability: { available: false, reason: "not_observed" },
          },
        ],
      },
    });
  if (/\/hosts\/[^/]+\/agents$/.test(p))
    return json({
      host_id: host.host_id,
      online: true,
      last_heartbeat_at_ms: now,
      projection: {
        execution_environment_id: host.host_id,
        format_version: 1,
        source_kind: "local_file",
        runtimes: [],
        agents: [],
      },
      executors: [],
    });
  if (p === "/api/v1/repos")
    return json({
      repos: [
        { repo: "sample", retired: false },
        { repo: "documentation", retired: false },
      ],
    });
  if (p === "/api/v1/tasks")
    return json({
      tasks: [task, completedTask].filter(
        (t) =>
          !url.searchParams.has("lifecycle") ||
          url.searchParams.get("lifecycle")!.split(",").includes(t.lifecycle),
      ),
      more: false,
    });
  if (p === "/api/v1/tasks/task-tour") return json(task);
  if (p === "/api/v1/tasks/task-completed") return json(completedTask);
  if (p.startsWith("/api/v1/runs/") && p.endsWith("/messages"))
    return json({ run_id: "run-tour", messages: [], more: false });
  if (p.startsWith("/api/v1/runs/") && p.endsWith("/rollout"))
    return json({
      run_id: p.split("/")[4],
      observed: p.includes("run-completed") ? "finished" : "running",
      logs: [
        { seq: 1, kind: "turn.started", created_at: new Date(now - 5000).toISOString(), body: {} },
        ...["item.started", "item.completed"].map((kind, index) => ({
          seq: index + 2,
          kind,
          created_at: new Date(now - 3000 + index * 1000).toISOString(),
          body: {
            kind,
            thread_id: "tour-thread",
            turn_id: "tour-turn",
            item_id: "tour-command",
            data: {
              type: "commandExecution",
              command: "示例检查",
              status: index ? "completed" : "inProgress",
              aggregatedOutput: index ? "示例结果" : "",
            },
          },
        })),
      ],
      before: 1,
      after: 3,
      watermark: 3,
      more_before: false,
      more_after: false,
    });
  if (p === "/api/v1/issues")
    return json({
      items: issues
        .filter(
          (i) =>
            !url.searchParams.has("parent_id") ||
            (i.parent_issue_id ?? "") === url.searchParams.get("parent_id"),
        )
        .map((i) => ({ ...i, title: i.plan.title, project: i.plan.project })),
      more: false,
    });
  if (p.endsWith("/results"))
    return json({
      items: [
        {
          title: "发布说明初稿",
          task_id: task.task_id,
          run_id: "run-tour",
          spec_version: 1,
          generation: 1,
          created_at: now,
          artifact: {
            artifact_id: "artifact-tour",
            commit: "abcdef1234567890",
            summary: "可重复核对的发布与回滚说明",
          },
          acceptances: [],
        },
      ],
      more: false,
    });
  if (p.endsWith("/deliveries")) return json({ issue_id: issues[2]!.issue_id, items: [] });
  if (p.endsWith("/activity"))
    return json({
      items: [
        {
          id: "activity-tour",
          kind: "message",
          author: "owner",
          created_at: now,
          position: 1,
          title: "补齐验收证据",
          block: { position: 1, trigger, items: [reply], more: false },
        },
      ],
      more: false,
      event_seq: 1,
    });
  if (p.endsWith("/followups")) return json({ items: [], more: false });
  if (p.startsWith("/api/v1/issues/")) {
    const id = p.split("/")[4]!;
    if (p.endsWith("/context"))
      return json({
        issue_id: id,
        revision: 1,
        prepared: true,
        dispatch_ready: true,
        coordination_state: "ready",
        sync_status: "current",
        facts: [],
        sources: [],
        more: false,
      });
    return json({
      ...(issues.find((i) => i.issue_id === id) ?? issues[2]),
      conversation: { conversation_id: `work-item:${id}`, can_send: true },
    });
  }
  if (p === "/api/v1/next-actions")
    return json({
      items: attentions
        .filter((a) => a.kind !== "information")
        .map((a, i) => ({
          entry_id: `next-${i}`,
          revision: 1,
          kind: "user_action",
          issue_id: a.issue_id,
          attention_id: a.attention_id,
          title: a.title,
          responsible: "owner",
          rank: i + 1,
          status: "open",
          updated_at: now,
        })),
      deadlines: [],
    });
  if (p === "/api/v1/inbox") return json({ items: attentions, more: false });
  if (p.startsWith("/api/v1/attentions/"))
    return json(attentions.find((a) => a.attention_id === p.split("/")[4]));
  if (p === "/api/v1/messages/source-tour")
    return json({
      message_id: "source-tour",
      kind: "message",
      author: "main_agent",
      body: "请列出可用于灰度验收的设备和发布模块。\n第二行保留原始内容。",
      created_at: trigger.created_at,
    });
  if (p === "/api/v1/token-usage")
    return json({
      revision: 1,
      as_of: now,
      timezone: "Asia/Shanghai",
      window: url.searchParams.get("window") ?? "total",
      start: null,
      end: now,
      totals,
      unattributed: { ...totals, requests: 0 },
      models: [{ name: "example-model", ...totals }],
      roles: [{ name: "implement", ...totals }],
      trend: [],
      records: [],
      next: 0,
      more: false,
      conflicts: 0,
      coverage: "隔离示例请求，不表示真实消费。",
    });
  if (p === "/api/v1/upgrades/upgrade-tour") return json({ upgrade });
  if (p === "/api/v1/archives")
    return json({
      items: [
        {
          archive_id: "archive-tour",
          state: "ready",
          metadata: {
            created_at: trigger.created_at,
            deployment_id: "previous-example",
            format_version: 1,
            schema: 57,
            record_kinds: ["task", "message"],
            original_count: 1,
            resource_count: 1,
          },
        },
      ],
      more: false,
    });
  if (p.startsWith("/api/v1/archives/archive-tour/records/"))
    return json({
      archive_id: "archive-tour",
      kind: p.split("/").at(-1),
      records: [
        {
          id: "archived-task",
          data: { task_id: "archived-task", title: "历史发布核对", lifecycle: "completed" },
        },
      ],
      more: false,
    });
  if (p.startsWith("/api/")) {
    unexpected.push(req.url!);
    res.statusCode = 404;
    return json({ error: { code: "not_found", message: "Unknown tour endpoint" } });
  }
  try {
    const file = resolve(root, p.split("/").at(-1)?.includes(".") ? `.${p}` : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "Content-Type",
      p.endsWith(".js")
        ? "application/javascript"
        : p.endsWith(".css")
          ? "text/css"
          : p.endsWith(".svg")
            ? "image/svg+xml"
            : p.endsWith(".png")
              ? "image/png"
              : p.endsWith(".json") || p.endsWith(".webmanifest")
                ? "application/json"
                : "text/html",
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
const origin = `http://127.0.0.1:${address.port}`;
const browser = await chromium.launch();
const routes = [
  "/",
  "/diagnostics/main",
  "/chat/consultation-tour",
  "/issues",
  "/issues?view=tree",
  "/issues/issue-in_progress",
  "/issues/issue-done",
  "/tasks",
  "/tasks/task-tour",
  "/tasks/task-completed",
  "/inbox",
  "/inbox/attention-decision",
  "/inbox/attention-action",
  "/inbox/attention-information",
  "/repos",
  "/usage",
  "/hosts",
  "/settings",
  "/settings/agents",
  "/archive",
  "/upgrades/upgrade-tour",
  "/more",
  "/404",
];
const results: unknown[] = [];
try {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 1024, height: 768 },
    { width: 390, height: 844 },
    { width: 320, height: 640 },
  ]) {
    for (const theme of ["light", "dark"] as const) {
      const context = await browser.newContext({
        viewport,
        colorScheme: theme,
        serviceWorkers: "block",
      });
      await context.addInitScript((value) => localStorage.setItem("tend.theme", value), theme);
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error") errors.push(message.text());
      });
      await page.goto(`${origin}/diagnostics/main`);
      await page.getByRole("textbox", { name: "消息", exact: true }).waitFor();
      let sidebarWidth: number | undefined;
      for (const [index, path] of routes.entries()) {
        await navigate(page, path);
        await page.locator("main h1:visible").waitFor();
        await page.waitForFunction(
          () =>
            !document.querySelector('[aria-busy="true"]') &&
            !Array.from(document.querySelectorAll("main h1")).some(
              (h) => h.textContent === "正在读取…",
            ),
        );
        // Let finite fixture reads and title-focus layout commits settle.
        await page.waitForTimeout(120);
        if (path === "/inbox")
          await page
            .getByText("请列出可用于灰度验收的设备和发布模块。", { exact: true })
            .first()
            .waitFor();
        if (
          [1440, 390].includes(viewport.width) &&
          theme === "light" &&
          !path.startsWith("/issues?") &&
          path !== "/issues"
        ) {
          const controls = page.locator("main summary:visible, main button[aria-expanded]:visible");
          const handles = await controls.elementHandles();
          const tested = new Set<string>();
          let probeIndex = 0;
          const probe = async (control: (typeof handles)[number], scroll = true) => {
            // Navigation menus are overlays, not growing in-place disclosures.
            if ((await control.getAttribute("aria-haspopup")) || !(await control.isVisible()))
              return;
            const id = await control.evaluate((node, next) => {
              const el = node as HTMLElement;
              return el.dataset.s8Probe ?? (el.dataset.s8Probe = next);
            }, String(probeIndex++));
            if (tested.has(id)) return;
            tested.add(id);
            if (scroll) await control.scrollIntoViewIfNeeded();
            const original = await control.evaluate(
              (node) => (node as Element).getBoundingClientRect().top,
            );
            const label = await control.textContent();
            for (const phase of ["open", "close"]) {
              await control.evaluate((node: HTMLElement) => node.click());
              await page.waitForTimeout(120);
              const y = await control.evaluate(
                (node) => (node as Element).getBoundingClientRect().top,
              );
              assert(
                Math.abs(y - original) <= 2,
                `${path} ${label} ${phase}: disclosure moved ${original} → ${y}`,
              );
              results.push({ disclosure: label, path, viewport, phase, original, y });
              if (phase === "open") {
                const container = await control.evaluateHandle((node) => {
                  const el = node as HTMLElement;
                  return el.tagName === "SUMMARY"
                    ? el.parentElement
                    : document.getElementById(
                        el.getAttribute("aria-controls")?.split(" ")[0] ?? "",
                      );
                });
                const root = container.asElement();
                if (root) {
                  const nested = await root.$$("summary:visible, button[aria-expanded]:visible");
                  for (const child of nested) {
                    if (
                      await child.evaluate(
                        (node, id) => (node as HTMLElement).dataset.s8Probe !== id,
                        id,
                      )
                    )
                      await probe(child, false);
                  }
                }
                await container.dispose();
              }
            }
          };
          for (const control of handles) await probe(control);
          if (path === "/tasks/task-tour") {
            const open = page.getByRole("button", { name: "展开执行步骤" });
            await open.click();
            await page.getByRole("button", { name: /查看完整过程/ }).click();
            const reader = page.getByRole("dialog", { name: "完整执行过程" });
            for (const summary of await reader.locator("summary:visible").elementHandles())
              await probe(summary);
            await page.getByRole("button", { name: "关闭完整过程" }).click();
            await open.click();
          }
          if (path === "/issues/issue-in_progress") {
            const summary = page.getByText("目标与完成条件", { exact: true });
            await summary.evaluate((node) => {
              for (let parent = node.parentElement; parent; parent = parent.parentElement)
                if (/auto|scroll/.test(getComputedStyle(parent).overflowY))
                  parent.scrollTop = parent.scrollHeight;
            });
            await page.waitForTimeout(120);
            const original = await summary.evaluate((node) => node.getBoundingClientRect().top);
            for (const phase of ["bottom-open", "bottom-close"]) {
              await summary.evaluate((node: HTMLElement) => node.click());
              await page.waitForTimeout(120);
              const y = await summary.evaluate((node) => node.getBoundingClientRect().top);
              assert(
                Math.abs(y - original) <= 2,
                `${path} ${phase}: following latest moved ${original} → ${y}`,
              );
              results.push({ disclosure: "目标与完成条件", path, viewport, phase, original, y });
            }
          }
        }
        if (viewport.width === 320) {
          const lines = await page
            .locator('[class*="line"][data-active]:visible')
            .evaluateAll((nodes) =>
              nodes.map((node) => {
                const el = node as HTMLElement;
                return {
                  width: el.getBoundingClientRect().width,
                  height: el.getBoundingClientRect().height,
                  parentWidth: el.parentElement!.getBoundingClientRect().width,
                };
              }),
            );
          for (const line of lines)
            assert(
              line.height <= 28 && line.width <= line.parentWidth + 1,
              `${path}: squeezed or overflowing status line ${JSON.stringify(line)}`,
            );
        }
        assert.deepEqual(errors, [], `${path}: console errors; API ${unexpected.join(", ")}`);
        const geometry = await page.evaluate(() => {
          const visible = (e: HTMLElement) =>
            e.checkVisibility() && e.getBoundingClientRect().width > 0;
          const tabs = document.querySelector<HTMLElement>('nav[aria-label="视图"]');
          const sidebar = document.querySelector<HTMLElement>("aside");
          const main = document.querySelector<HTMLElement>("main")!;
          const controls = Array.from(
            document.querySelectorAll<HTMLElement>('button, a, [role="tab"]'),
          ).filter(visible);
          const touch = Number.parseFloat(
            getComputedStyle(document.documentElement).getPropertyValue("--control-touch"),
          );
          const undersized =
            innerWidth > 760
              ? []
              : controls
                  .filter(
                    (e) =>
                      e.getAttribute("role") === "tab" ||
                      e.matches(
                        'nav[aria-label="视图"] a, nav[aria-label="更多导航"] a, h1 button, button[aria-label="发送"], button[aria-label="更多"]',
                      ) ||
                      Array.from(e.classList).some((c) => c.includes("primary")),
                  )
                  .filter((e) => {
                    const box = e.getBoundingClientRect();
                    return box.height < touch - 1 || box.width < touch - 1;
                  })
                  .map((e) => e.getAttribute("aria-label") ?? e.textContent?.trim());
          const clipped = controls
            .filter(
              (e) =>
                getComputedStyle(e).whiteSpace === "nowrap" &&
                e.textContent?.trim() &&
                e.scrollWidth > e.clientWidth + 1,
            )
            .map((e) => e.textContent?.trim());
          const last = Array.from(
            main.querySelectorAll<HTMLElement>("button, a, input, textarea, p, li"),
          )
            .filter(visible)
            .at(-1);
          return {
            width: document.documentElement.scrollWidth,
            headings: Array.from(document.querySelectorAll<HTMLElement>("h1")).filter(visible)
              .length,
            clipped,
            undersized,
            sidebar: sidebar?.getBoundingClientRect().width,
            tabs: !!tabs && visible(tabs),
            tabTop: tabs?.getBoundingClientRect().top,
            mainBottom: main.getBoundingClientRect().bottom,
            lastBottom: last?.getBoundingClientRect().bottom,
            detail: !!document.querySelector('[data-detail="true"]'),
          };
        });
        const name = `${viewport.width}-${theme}-${index}-${path.replace(/[^a-z0-9]+/gi, "-")}`;
        await page.mouse.move(0, 0);
        await page.screenshot({ path: join(output, `${name}.png`) });
        results.push({ path, viewport, theme, ...geometry });
        assert(
          geometry.width <= viewport.width,
          `${name}: horizontal page overflow ${geometry.width}`,
        );
        assert.equal(
          await page.evaluate(() => document.body.hasAttribute("data-dark")),
          theme === "dark",
          `${name}: theme`,
        );
        assert.equal(geometry.headings, 1, `${name}: exactly one visible h1`);
        assert.deepEqual(geometry.clipped, [], `${name}: clipped control text`);
        assert.deepEqual(geometry.undersized, [], `${name}: undersized mobile action`);
        if (viewport.width >= 1024) {
          sidebarWidth ??= geometry.sidebar;
          assert.equal(geometry.sidebar, sidebarWidth, `${name}: sidebar width changed`);
        }
        assert.equal(
          geometry.tabs,
          viewport.width <= 760 && !geometry.detail,
          `${name}: tab visibility`,
        );
        if (geometry.tabs)
          assert(
            geometry.mainBottom <= geometry.tabTop! + 1,
            `${name}: tabs overlap the content viewport`,
          );
        if (geometry.tabs) {
          const lastBottom = await page.evaluate(() => {
            const main = document.querySelector<HTMLElement>("main")!;
            for (const e of [main, ...main.querySelectorAll<HTMLElement>("*")]) {
              if (e.checkVisibility() && /(auto|scroll)/.test(getComputedStyle(e).overflowY))
                e.scrollTop = e.scrollHeight;
            }
            const last = Array.from(
              main.querySelectorAll<HTMLElement>("button, a, input, textarea, p, li"),
            )
              .filter((e) => e.checkVisibility() && e.getBoundingClientRect().width > 0)
              .at(-1);
            return last?.getBoundingClientRect().bottom;
          });
          if (lastBottom !== undefined)
            assert(
              lastBottom <= geometry.tabTop! + 1,
              `${name}: tabs overlap the last content element (${lastBottom})`,
            );
        }
        assert.deepEqual(errors, [], `${name}: console errors`);
      }
      if (viewport.width <= 760) {
        await navigate(page, "/diagnostics/main");
        await page.getByRole("heading", { level: 1 }).getByRole("button").click();
        await page.getByRole("dialog").waitFor();
        assert.equal(
          await page.locator('nav[aria-label="视图"]').isVisible(),
          false,
          "dialog hides tabs",
        );
        await page.screenshot({ path: join(output, `${viewport.width}-${theme}-chat-sheet.png`) });
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => !document.querySelector("dialog[open]"));
        await page.getByRole("button", { name: "更多", exact: true }).click();
        await page.getByRole("menuitem", { name: "补充来源", exact: true }).click();
        await page.getByRole("dialog", { name: "补充来源", exact: true }).waitFor();
        assert.equal(
          await page.locator('nav[aria-label="视图"]').isVisible(),
          false,
          "Sheet hides tabs",
        );
        await page.screenshot({
          path: join(output, `${viewport.width}-${theme}-source-sheet.png`),
        });
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => !document.querySelector("dialog[open]"));
        assert.equal(
          await page.locator('nav[aria-label="视图"]').isVisible(),
          true,
          "closing dialog restores tabs",
        );
      }
      await context.close();
    }
  }
  assert.deepEqual(unexpected, [], "fixture must implement every requested API");
  // Another channel resolves the same original question while this browser is composing.
  for (const width of [1440, 390]) {
    const reset = { ...imQuestion.interaction!, state: "open" };
    delete reset.handled_channel;
    delete reset.handled_at;
    delete reset.answer_summary;
    imQuestion.interaction = reset;
    const context = await browser.newContext({
      viewport: { width, height: 900 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    await page.goto(origin);
    const question = page.getByRole("region", { name: imQuestion.body, exact: true });
    await question.getByRole("button", { name: "手机优先", exact: true }).waitFor();
    await question.getByRole("button", { name: "文字回答", exact: true }).click();
    const composer = page.getByRole("textbox", { name: "发消息给 Tend", exact: true });
    await composer.fill("请先核对窄屏，这份补充说明不要丢失");
    imQuestion.interaction = {
      ...imQuestion.interaction!,
      state: "answered",
      handled_channel: "telegram",
      handled_at: now,
      answer_summary: "手机优先",
    };
    imChanged();
    await question.getByText("已在 Telegram 处理", { exact: true }).waitFor();
    assert.equal(await question.getByRole("button").count(), 0, "old question controls disappear");
    assert.equal(await composer.inputValue(), "请先核对窄屏，这份补充说明不要丢失");
    assert(
      await page.getByRole("button", { name: "发送", exact: true }).isDisabled(),
      "stale quote cannot submit",
    );
    // A persisted quote may be older than the latest window after reloading.
    imHideQuestion = true;
    await page.waitForTimeout(350);
    await page.reload();
    await page
      .getByText("此问题已不再接受回答。文字已保留，取消引用后可以作为新消息发送。", {
        exact: true,
      })
      .waitFor();
    assert.equal(await composer.inputValue(), "请先核对窄屏，这份补充说明不要丢失");
    assert(await page.getByRole("button", { name: "发送", exact: true }).isDisabled());
    imHideQuestion = false;
    imChanged();
    await page.getByRole("button", { name: "取消引用，保留文字", exact: true }).click();
    assert.equal(await composer.inputValue(), "请先核对窄屏，这份补充说明不要丢失");
    assert(await page.getByRole("button", { name: "发送", exact: true }).isEnabled());
    await page.screenshot({ path: join(output, `${width}-im-answered-elsewhere.png`) });
    await navigate(page, "/diagnostics/main");
    await page.getByRole("textbox", { name: "消息", exact: true }).fill("单独的诊断草稿");
    await navigate(page, "/");
    assert.equal(
      await composer.inputValue(),
      "请先核对窄屏，这份补充说明不要丢失",
      "IM and diagnostic drafts are isolated",
    );
    await page.reload();
    await composer.waitFor();
    await page.waitForFunction(
      () =>
        document.querySelector<HTMLTextAreaElement>('textarea[aria-label="发消息给 Tend"]')
          ?.value === "请先核对窄屏，这份补充说明不要丢失",
    );
    await page.close();
    const fresh = await context.newPage();
    await fresh.goto(origin);
    await fresh
      .locator('[aria-label="Tend 对话"]')
      .getByText(/暂存草稿 ·/)
      .click();
    await fresh
      .getByRole("button", { name: "请先核对窄屏，这份补充说明不要丢失 · 恢复", exact: true })
      .click();
    assert.equal(
      await fresh.getByRole("textbox", { name: "发消息给 Tend", exact: true }).inputValue(),
      "请先核对窄屏，这份补充说明不要丢失",
      "saved drafts remain recoverable after losing tab identity",
    );
    await context.close();
  }
  console.log(
    "PASS IM: cross-channel invalidation, retained draft, isolated diagnostics and reload",
  );
  // A stale route chunk must not unmount the chat, navigation or its draft.
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    const context = await browser.newContext({ viewport, serviceWorkers: "block" });
    const page = await context.newPage();
    const uncaught: string[] = [];
    page.on("pageerror", (error) => uncaught.push(error.message));
    const chunk = /\/assets\/More-[^/]+\.js$/;
    let rejected = 0;
    await context.route(chunk, async (request) => {
      rejected++;
      await request.fulfill({
        status: 404,
        contentType: "application/javascript",
        headers: { "Cache-Control": "no-store" },
        body: "// stale route chunk",
      });
    });
    try {
      await page.goto(`${origin}/diagnostics/main`);
      const composer = page.getByRole("textbox", { name: "消息", exact: true });
      await composer.fill("路由加载失败仍保留这份草稿");
      const originalComposer = await composer.elementHandle();
      const originalSidebar = await page.locator("aside").first().elementHandle();
      assert(originalComposer && originalSidebar);
      await navigate(page, "/more");
      await page.getByRole("heading", { name: "页面已更新", exact: true }).waitFor();
      await page
        .getByRole("alert")
        .getByText("页面已更新，需要重新加载。", { exact: true })
        .waitFor();
      assert.equal(rejected, 1, "a fresh route import receives the injected chunk 404");
      assert(
        await originalComposer.evaluate((element) => element.isConnected),
        "chat DOM survives route rejection",
      );
      assert(
        await originalSidebar.evaluate((element) => element.isConnected),
        "sidebar DOM survives route rejection",
      );
      assert.equal(await page.locator('nav[aria-label="视图"]').isVisible(), viewport.width <= 760);
      if (viewport.width > 760) assert(await page.locator("aside").first().isVisible());
      await page.screenshot({ path: join(output, `${viewport.width}-lazy-chunk-failure.png`) });
      await navigate(page, "/repos");
      await page.getByRole("heading", { name: "已注册仓库", exact: true }).waitFor();
      assert.equal(
        await page.getByRole("heading", { name: "页面已更新", exact: true }).count(),
        0,
        "changing routes resets the boundary",
      );
      await navigate(page, "/diagnostics/main");
      await composer.waitFor();
      assert(
        await originalComposer.evaluate(
          (element) => element === document.querySelector('textarea[aria-label="消息"]'),
        ),
        "returning to chat reuses the original composer",
      );
      assert.equal(await composer.inputValue(), "路由加载失败仍保留这份草稿");
      await navigate(page, "/more");
      await page.getByRole("heading", { name: "页面已更新", exact: true }).waitFor();
      await context.unroute(chunk);
      await Promise.all([
        page.waitForEvent("load"),
        page.getByRole("button", { name: "重新加载", exact: true }).click(),
      ]);
      if (viewport.width <= 760)
        await page.getByRole("navigation", { name: "更多导航", exact: true }).waitFor();
      else await page.getByRole("heading", { name: "Tend", exact: true }).waitFor();
      assert.equal(
        await page.getByRole("heading", { name: "页面已更新", exact: true }).count(),
        0,
        "manual reload fetches the available route chunk",
      );
      assert.deepEqual(uncaught, [], "route rejection is caught rather than tearing down the root");
    } finally {
      await context.close();
    }
  }
  assert.deepEqual(unexpected, [], "fault recovery only uses fixture APIs");
  console.log(
    "PASS rejected lazy chunks: route fallback, retained shell/chat/draft, navigation reset and manual reload",
  );
  // Prove every emitted lazy chunk is in this build's offline shell cache.
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  await page.goto(`${origin}/diagnostics/main`);
  console.log("Tour geometry complete; verifying offline shell");
  await page.evaluate(async () => {
    await navigator.serviceWorker.register("/sw.js");
    await Promise.race([
      navigator.serviceWorker.ready,
      new Promise((_, reject) =>
        setTimeout(() => reject(new Error("Service worker did not activate")), 10000),
      ),
    ]);
    if (!navigator.serviceWorker.controller)
      await new Promise<void>((done) =>
        navigator.serviceWorker.addEventListener("controllerchange", () => done(), { once: true }),
      );
  });
  const cached = await page.evaluate(async () => {
    const keys = await caches.keys();
    return (await (await caches.open(keys.find((k) => k.startsWith("tend-shell-"))!)).keys()).map(
      (r) => new URL(r.url).pathname,
    );
  });
  for (const file of await readdir(join(root, "assets")))
    assert(cached.includes(`/assets/${file}`), `${file} absent from offline shell`);
  await context.setOffline(true);
  await page.goto(`${origin}/more`);
  await page.getByRole("heading", { name: "更多", exact: true }).waitFor();
  await page.reload();
  await page.getByRole("heading", { name: "更多", exact: true }).waitFor();
  await context.close();
  console.log(
    `PASS UI tour: ${results.length} route/theme/viewport checks and offline lazy route reload`,
  );
} finally {
  await writeFile(join(output, "geometry.json"), JSON.stringify(results, null, 2));
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
