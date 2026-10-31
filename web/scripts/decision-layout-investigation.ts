import { fixtureRoot } from "./navigation.ts";
/// <reference lib="dom" />
// Exercise the built application with an isolated owner API. The fixture only
// accepts the decision write under test and never contacts a deployed server.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { chromium, webkit, type BrowserType, type Locator, type Page } from "playwright";
import type {
  ConversationItem,
  AttentionView,
  DecisionView,
  Envelope,
  Snapshot,
  TaskSummary,
} from "../src/api/generated/api.ts";

const appRoot = fixtureRoot();
const output = process.env.TEND_DECISION_OUTPUT ?? join(tmpdir(), "tend-decision-layout");
await mkdir(output, { recursive: true });

const question =
  "是否接受本轮完整支持 TraeX app-server 的原 turn 追加，并对当前 Orca 原始 TUI 明确返回 executor_unsupported？若必须覆盖 Orca TUI，则需要允许后续提供可指定原 turn 的原生接口或可验证的专属 app-server 连接，本轮不能用 terminal send 或重启模拟。";
const originalOptions = [
  "接受 TraeX app-server 支持，当前 Orca TUI 明确不支持并在报告列为上线限制",
  "必须覆盖 Orca TUI；继续调查可验证的原生原 turn 接口，不允许通过重启或新 turn 模拟",
];
const layoutOptions = [
  originalOptions[0]!,
  "中英混排 option：保留 executor_unsupported 与原 turn 的完整语义，不能裁切或只显示固定行数",
  "must_keep_the_original_turn_and_use_a_verifiable_dedicated_app_server_connection_without_restart_or_new_turn_simulation",
];

const task: TaskSummary = {
  task_id: "task_01M5WNC0000000000000000000",
  title: "实现向运行中 worker 追加消息且保持原 turn",
  project: "fixture",
  workspace: "fixture",
  lifecycle: "running",
  control_mode: "managed",
  control_revision: 1,
  spec_version: 1,
  next_implement_generation: 1,
  rating: "p2",
  block_reason: "waiting_decision",
};
const heldTask: TaskSummary = {
  ...task,
  task_id: "task_01M5WNC0000000000000000001",
  title: "检查交还按钮",
  control_mode: "human_control",
  workspace: "fixture-handback",
};

function workerDecision(options = currentOptions): DecisionView {
  return {
    ...(process.env.TEND_DECISION_CONTEXT === "1"
      ? {
          briefing: {
            revision: 1,
            summary: "环境故障与外部取证缺口分别处理。",
            owner_choice: "是否按现有证据交付，保留未核验项。",
            source_message_id: "msg_context",
            created_at: 1,
            issues: [
              {
                issue_id: "scratch",
                category: "environment",
                summary: "临时目录写入受阻",
                impact: "临时分析在内存中继续；不解释业务源码的访问失败。",
                next_action: "修复执行环境",
                actor: "main_agent",
                status: "workaround",
                source: "worker 执行结果",
              },
              {
                issue_id: "source",
                category: "evidence",
                summary: "业务源码尚未核验",
                impact: "根因仍为候选，不能宣称证实。",
                next_action: "确认只读访问条件",
                actor: "worker",
                status: "open",
                source: "代码读取结果",
              },
            ],
          },
        }
      : {}),
    decision_id: "decision_fixture",
    options,
    question,
    status: "open",
    task_id: task.task_id,
    version: 1,
    worker: {
      evidence: "Fixture evidence only; no production decision is read or changed.",
      question_key: "fixture_mobile_decision_layout",
      question_message_id: "msg_fixture_question",
      role: "implement",
      run_id: "run_fixture",
      spec_version: 1,
    },
  };
}

const effectDecision: DecisionView = {
  decision_id: "decision_effect_fixture",
  options: ["retry the implementation", "revise the spec"],
  effects: { "retry the implementation": "implement" },
  question: "The implementation run failed (no_changes). How should this task proceed?",
  status: "open",
  version: 1,
};

const history = Array.from({ length: 14 }, (_, index): ConversationItem[] => {
  const ownerSeq = index * 2 + 1;
  const at = new Date(Date.UTC(2026, 9, 27, 0, index)).toISOString();
  return [
    {
      author: "owner",
      body: `历史消息 ${index + 1}：用于制造可滚动的本地对话，不来自生产。`,
      created_at: at,
      kind: "message",
      message_id: `msg_fixture_owner_${index}`,
      seq: ownerSeq,
      state: "replied",
    },
    {
      assistant_state: "completed",
      author: "main_agent",
      body: "本地 fixture 回复。这里仅用于验证跳到最新控件的边界。",
      created_at: at,
      kind: "message",
      message_id: `msg_fixture_agent_${index}`,
      reply_to: `msg_fixture_owner_${index}`,
      seq: ownerSeq + 1,
      ...(index === 12
        ? {
            tool: {
              input: "Inspect fixture",
              name: "review_tool",
              output: Array.from({ length: 30 }, (_, line) => `output line ${line + 1}`).join("\n"),
              status: "completed" as const,
              turn_id: "turn_fixture",
              updated_at: at,
            },
          }
        : {}),
    },
  ];
}).flat();
const appendedHistory: ConversationItem[] = [];
const sharedHistory: ConversationItem[] = Array.from({ length: 6 }, (_, index) => {
  const seq = index + 1;
  const agent = seq === 3 || seq === 5 || seq === 6;
  return {
    seq,
    message_id: `msg_shared_${seq}`,
    kind: "message",
    author: agent ? "main_agent" : "owner",
    body: [
      "SHARED_ORIGINAL_INPUT",
      "SHARED_SUPPLEMENT_INPUT",
      "SHARED_REPLY_ONCE",
      "SHARED_LATER_INPUT",
      "SHARED_FINAL_ONCE",
      "SHARED_MORE_OUTPUT",
    ][index]!,
    created_at: new Date(Date.UTC(2026, 9, 28, 1, 0, seq)).toISOString(),
    ...(agent
      ? {
          reply_to: `msg_shared_${seq === 3 ? 2 : seq === 5 ? 4 : 1}`,
          assistant_revision: 1,
          assistant_state: "completed" as const,
        }
      : { state: "replied" as const }),
  };
});
let sharedTranscript = false;
let normalInbox = false;

let currentOptions = originalOptions;
let decisionVisible = true;
let upgradeVisible = true;
let pendingOption: string | null = null;
let includeAuxiliary = false;
let includeQueue = false;
let eventSeq = 1;
let responseMode: "success" | "failure" = "success";
let responseDelay = 0;
const writes: Envelope[] = [];
const streams = new Set<ServerResponse>();

function reset(options = originalOptions) {
  sharedTranscript = false;
  normalInbox = false;
  currentOptions = options;
  decisionVisible = true;
  upgradeVisible = true;
  pendingOption = null;
  includeAuxiliary = false;
  includeQueue = false;
  eventSeq = 1;
  responseMode = "success";
  responseDelay = 0;
  appendedHistory.length = 0;
  writes.length = 0;
}

function attentionFixture(): AttentionView {
  return {
    attention_id: "attention_fixture",
    kind: "decision",
    title: question,
    status: decisionVisible ? "open" : "resolved",
    revision: 1,
    decision_id: "decision_fixture",
    question_version: 1,
    question_options: currentOptions,
    decision: workerDecision(),
    task_id: task.task_id,
    raised_by: { kind: "agent", role: "implement" },
    created_at: 1,
    updated_at: 1,
    latest_action_reminders: false,
  };
}

function snapshot(): Snapshot {
  return {
    event_seq: eventSeq,
    tasks: includeAuxiliary ? [task, heldTask] : [task],
    projects: [],
    hosts: [],
    queue: [],
    decisions: [
      ...(decisionVisible ? [workerDecision()] : []),
      ...(includeAuxiliary ? [effectDecision] : []),
    ],
    ...(upgradeVisible
      ? {
          upgrade: {
            created_at: "2026-10-27T00:00:00Z",
            hosts: [],
            phase: "runners",
            release_id: "runner-config-maintenance-example",
            revision: 1,
            status: "running",
            updated_at: "2026-10-27T00:00:00Z",
            upgrade_id: "upgrade_fixture",
          },
        }
      : {}),
  };
}

function conversation() {
  if (sharedTranscript) return sharedHistory.slice(0, 5);
  const items = [...history, ...appendedHistory];
  if (!pendingOption) return items;
  return [
    ...items,
    {
      author: "owner",
      body: `关于 fixture：${pendingOption}`,
      created_at: "2026-10-27T00:30:00Z",
      decision: { decision_id: "decision_fixture", version: 1, option: pendingOption },
      kind: "message",
      message_id: "msg_fixture_pending_answer",
      seq: 100,
      state: "processing",
    },
  ];
}

function sendJSON(res: ServerResponse, body: unknown, status = 200) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(body));
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const path = url.pathname;
  res.setHeader("Cache-Control", "no-store");
  if (path === "/api/v1/session")
    return sendJSON(res, {
      token: "fixture-token",
      login: "Fixture owner",
      scope: "owner",
      deployment_id: "layout-fixture",
    });
  if (path === "/api/v1/ops" && req.method === "POST") {
    let raw = "";
    for await (const chunk of req) raw += String(chunk);
    const envelope = JSON.parse(raw) as Envelope;
    writes.push(envelope);
    assert.equal(envelope.operation, "decisions.respond");
    assert.deepEqual(envelope.target, { type: "decision", id: "decision_fixture" });
    assert.equal((envelope.payload as { version: number }).version, 1);
    if (responseDelay) await new Promise((done) => setTimeout(done, responseDelay));
    if (responseMode === "failure")
      return sendJSON(
        res,
        { error: { code: "invalid_state", message: "fixture rejected answer" } },
        409,
      );
    decisionVisible = false;
    return sendJSON(res, {
      operation: {
        request_id: envelope.request_id,
        status: "succeeded",
        result: { decision_id: "decision_fixture" },
      },
    });
  }
  if (req.method !== "GET")
    return sendJSON(
      res,
      { error: { code: "method_not_allowed", message: "fixture only accepts decision writes" } },
      405,
    );
  if (path === "/api/v1/snapshot") return sendJSON(res, snapshot());
  if (path === "/api/v1/conversation")
    return sendJSON(res, { items: conversation(), more: false, event_seq: eventSeq });
  if (path === "/api/v1/conversation/settings")
    return sendJSON(res, {
      enabled: true,
      available: true,
      revision: 1,
      model: "model-a",
      effort: "medium",
      models: Array.from({ length: 12 }, (_, index) => ({
        id: index === 0 ? "model-a" : `model-${index + 1}`,
        label: `Model ${index + 1}`,
        supported_efforts: ["medium", "high"],
        default_effort: "medium",
      })),
    });
  if (path === "/api/v1/conversation/handoff")
    return sendJSON(res, { enabled: false, revision: 1, idle_minutes: 60 });
  if (path.endsWith("/usage"))
    return sendJSON(res, {
      available: true,
      compact_supported: true,
      thread_id: "fixture-main",
      revision: 1,
      used_percentage: 42,
      active_tokens: 42_000,
      effective_window: 100_000,
      measurement: "observed",
      sampled_at: Date.now(),
      updated_at: Date.now(),
    });
  if (path.endsWith("/compactions")) return sendJSON(res, { items: [] });
  if (path === "/api/v1/agent-conversations") return sendJSON(res, { enabled: false, items: [] });
  if (path === "/api/v1/hosted-agents" || path === "/api/v1/next-actions")
    return sendJSON(res, { items: [] });
  // Explicitly exercise the retained decision dock only when the new Inbox
  // read is unavailable; normal empty Inbox pages must not masquerade as null.
  if (path === "/api/v1/inbox")
    return !normalInbox
      ? sendJSON(
          res,
          {
            error: {
              code: "unavailable",
              message: "Intentional Inbox read failure for dock fallback",
            },
          },
          503,
        )
      : sendJSON(res, { items: decisionVisible ? [attentionFixture()] : [], more: false });
  if (path === "/api/v1/repos") return sendJSON(res, { repos: [] });
  if (path === "/api/v1/issues") return sendJSON(res, { items: [] });
  if (path === "/api/v1/attentions/attention_fixture") return sendJSON(res, attentionFixture());
  if (path === `/api/v1/tasks/${task.task_id}`)
    return sendJSON(res, {
      ...task,
      spec: {},
      runs: [],
      actions: [],
      decisions: decisionVisible ? [workerDecision()] : [],
      artifacts: [],
      acceptances: [],
    });
  if (path === `/api/v1/tasks/${heldTask.task_id}`)
    return sendJSON(res, {
      ...heldTask,
      spec: {},
      runs: [],
      actions: [],
      decisions: [],
      artifacts: [],
      acceptances: [],
    });
  if (path === "/api/v1/tasks") return sendJSON(res, { tasks: [] });
  if (path === "/api/v1/conversation/queue") {
    const category = url.searchParams.get("category") ?? "owner";
    const items =
      includeQueue && category === "owner"
        ? Array.from({ length: 3 }, (_, index) => ({
            position: 100 + index,
            revision: 1,
            status: "queued",
            can_withdraw: true,
            item: {
              ...history[index * 2]!,
              body: `本地待处理消息 ${index + 1}，用于验证队列控件不会被决定卡片挤掉。`,
              message_id: `msg_fixture_queue_${index}`,
              seq: 100 + index,
              state: "queued",
            },
          }))
        : [];
    return sendJSON(res, {
      enabled: true,
      event_seq: eventSeq,
      items,
      total: items.length,
    });
  }
  if (sharedTranscript && path === "/api/v1/conversation/blocks/msg_shared_1")
    return sendJSON(res, {
      position: 1,
      trigger: sharedHistory[0],
      items: [sharedHistory[5]],
      more: false,
      next_seq: 6,
    });
  if (sharedTranscript && path === "/api/v1/conversation/transcript")
    return sendJSON(res, {
      blocks: [
        {
          position: 1,
          trigger: sharedHistory[0],
          items: [sharedHistory[2], sharedHistory[4]],
          more: true,
          next_seq: 5,
        },
        {
          position: 2,
          trigger: sharedHistory[1],
          items: [sharedHistory[2], sharedHistory[4]],
          more: false,
          next_seq: 5,
        },
        {
          position: 4,
          trigger: sharedHistory[3],
          items: [sharedHistory[4]],
          more: false,
          next_seq: 5,
        },
      ],
      event_seq: eventSeq,
      more: false,
    });
  if (path === "/api/v1/conversation/transcript")
    return sendJSON(res, {
      blocks: [...history, ...appendedHistory].reduce<unknown[]>((blocks, item, index, items) => {
        if (index % 2 === 0)
          blocks.push({
            position: index / 2 + 1,
            trigger: item,
            items: [items[index + 1]!],
            more: false,
          });
        return blocks;
      }, []),
      event_seq: eventSeq,
      more: false,
    });
  if (path === "/api/v1/events") {
    res.setHeader("Content-Type", "text/event-stream");
    res.write(`event: start\ndata: ${JSON.stringify({ event_seq: eventSeq })}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path.startsWith("/api/"))
    return sendJSON(res, { error: { code: "not_found", message: "not in fixture" } }, 404);
  try {
    const file = resolve(
      appRoot,
      path.split("/").at(-1)?.includes(".") ? `.${path}` : "index.html",
    );
    assert(file.startsWith(appRoot));
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
if (!address || typeof address === "string") throw new Error("fixture server has no TCP address");
const fixturePort = address.port;

const browserName = process.env.TEND_DECISION_BROWSER === "webkit" ? "webkit" : "chromium";
const browserType: BrowserType = browserName === "webkit" ? webkit : chromium;
const executablePath = process.env.TEND_DECISION_BROWSER_PATH ?? process.env.TEND_LAYOUT_BROWSER;
const browser = await browserType.launch({ ...(executablePath ? { executablePath } : {}) });

function publish(kind = "decision.updated") {
  eventSeq++;
  const event = {
    event_seq: eventSeq,
    object_type: kind.startsWith("upgrade") ? "upgrade" : "decision",
    object_id: kind.startsWith("upgrade") ? "upgrade_fixture" : "decision_fixture",
    kind,
    created_at: "2026-10-27T00:10:00Z",
  };
  for (const stream of streams) stream.write(`event: ledger\ndata: ${JSON.stringify(event)}\n\n`);
}

type Rect = {
  top: number;
  right: number;
  bottom: number;
  left: number;
  width: number;
  height: number;
};
type ButtonMeasure = {
  label: string;
  rect: Rect;
  textRect: Rect;
  paddingTop: number;
  paddingRight: number;
  paddingBottom: number;
  paddingLeft: number;
};

async function rect(locator: Locator): Promise<Rect> {
  return locator.evaluate((element) => {
    const r = element.getBoundingClientRect();
    return {
      top: r.top,
      right: r.right,
      bottom: r.bottom,
      left: r.left,
      width: r.width,
      height: r.height,
    };
  });
}

async function buttonMeasure(button: Locator): Promise<ButtonMeasure> {
  return button.evaluate((element) => {
    const box = element.getBoundingClientRect();
    const range = document.createRange();
    range.selectNodeContents(element);
    const text = range.getBoundingClientRect();
    const style = getComputedStyle(element);
    const plain = (r: DOMRect): Rect => ({
      top: r.top,
      right: r.right,
      bottom: r.bottom,
      left: r.left,
      width: r.width,
      height: r.height,
    });
    return {
      label: element.textContent?.trim() ?? "",
      rect: plain(box),
      textRect: plain(text),
      paddingTop: parseFloat(style.paddingTop),
      paddingRight: parseFloat(style.paddingRight),
      paddingBottom: parseFloat(style.paddingBottom),
      paddingLeft: parseFloat(style.paddingLeft),
    };
  });
}

function assertContained(measure: ButtonMeasure, mobile: boolean) {
  const tolerance = 1.5;
  assert(measure.textRect.top >= measure.rect.top + measure.paddingTop - tolerance, measure.label);
  assert(
    measure.textRect.bottom <= measure.rect.bottom - measure.paddingBottom + tolerance,
    measure.label,
  );
  assert(
    measure.textRect.left >= measure.rect.left + measure.paddingLeft - tolerance,
    measure.label,
  );
  assert(
    measure.textRect.right <= measure.rect.right - measure.paddingRight + tolerance,
    measure.label,
  );
  if (mobile) assert(measure.rect.height >= 44, `${measure.label} is below 44 CSS px`);
}

function overlaps(a: Rect, b: Rect) {
  return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
}

async function reachable(locator: Locator) {
  await locator.scrollIntoViewIfNeeded();
  const reachableNow = await locator.evaluate((element) => {
    const r = element.getBoundingClientRect();
    const x = Math.min(innerWidth - 1, Math.max(0, r.left + r.width / 2));
    const y = Math.min(innerHeight - 1, Math.max(0, r.top + r.height / 2));
    const hit = document.elementFromPoint(x, y);
    return (
      r.width > 0 &&
      r.height > 0 &&
      r.bottom > 0 &&
      r.top < innerHeight &&
      !!hit &&
      element.contains(hit)
    );
  });
  if (!reachableNow) {
    await locator.page().screenshot({ path: join(output, "reachability-failure.png") });
    await writeFile(
      join(output, "reachability-failure.json"),
      JSON.stringify(
        await locator.evaluate((element) => {
          const rect = element.getBoundingClientRect();
          const hit = document.elementFromPoint(
            Math.min(innerWidth - 1, Math.max(0, rect.left + rect.width / 2)),
            Math.min(innerHeight - 1, Math.max(0, rect.top + rect.height / 2)),
          );
          return {
            rect: rect.toJSON(),
            hit: hit?.outerHTML,
            active: document.activeElement?.outerHTML,
            ancestors: Array.from(
              (function* () {
                let node: Element | null = element;
                while (node) {
                  yield node;
                  node = node.parentElement;
                }
              })(),
            ).map((node) => ({
              tag: node.tagName,
              className: node.className,
              top: node.scrollTop,
              height: node.clientHeight,
              total: node.scrollHeight,
            })),
          };
        }),
        null,
        2,
      ),
    );
  }
  assert(
    reachableNow,
    `control is not reachable: ${(await locator.getAttribute("aria-label")) ?? (await locator.textContent())}`,
  );
}

async function setFontScale(page: Page, scale: number) {
  if (scale === 1) return;
  const declarations = await page.evaluate((factor) => {
    const style = getComputedStyle(document.documentElement);
    // Computed styles expose custom values but do not always enumerate their names.
    const names = new Set(
      Array.from(document.styleSheets).flatMap((sheet) =>
        Array.from(sheet.cssRules).flatMap((rule) =>
          rule instanceof CSSStyleRule ? Array.from(rule.style) : [],
        ),
      ),
    );
    return Array.from(names)
      .filter((name) => name.startsWith("--text-") || name.startsWith("--leading-"))
      .map((name) => {
        const value = style.getPropertyValue(name).trim();
        if (!/^\d+(?:\.\d+)?px$/.test(value)) throw new Error(`Invalid type token: ${name}`);
        return `${name}: ${Number.parseFloat(value) * factor}px;`;
      })
      .join(" ");
  }, scale);
  await page.addStyleTag({ content: `:root { ${declarations} }` });
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => done(undefined))));
}

async function openPage(width: number, height: number) {
  const context = await browser.newContext({
    viewport: { width, height },
    serviceWorkers: "block",
  });
  const page = await context.newPage();
  page.setDefaultTimeout(10_000);
  const errors: string[] = [];
  const consoleErrors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") {
      const expectedInboxFailure =
        !normalInbox &&
        message.location().url.includes("/api/v1/inbox") &&
        message.text().includes("503");
      if (!expectedInboxFailure) consoleErrors.push(message.text());
    }
  });
  await page.goto(`http://127.0.0.1:${fixturePort}/`);
  return { context, page, errors, consoleErrors };
}

function cardFor(page: Page) {
  return page.getByRole("main").locator("q").filter({ hasText: question }).locator("..");
}

async function showJump(page: Page) {
  const transcript = page.getByRole("region", { name: "对话记录" });
  await transcript.evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll", { bubbles: true }));
  });
  const jump = transcript.locator("..").getByRole("button", { name: "跳到最新" });
  await jump.waitFor();
  return { transcript, jump };
}

const geometry: unknown[] = [];

async function checkLayoutMatrix() {
  for (const viewport of [
    { width: 320, height: 720 },
    { width: 390, height: 844 },
    { width: 430, height: 932 },
    { width: 1440, height: 900 },
  ]) {
    for (const fontScale of [1, 1.5, 2]) {
      reset(layoutOptions);
      const { context, page, errors, consoleErrors } = await openPage(
        viewport.width,
        viewport.height,
      );
      try {
        await cardFor(page).waitFor();
        const baseFontSize = await page.evaluate(() =>
          Number.parseFloat(getComputedStyle(document.body).fontSize),
        );
        const baseQuestionLeading = await cardFor(page)
          .locator("q")
          .evaluate((element) => Number.parseFloat(getComputedStyle(element).lineHeight));
        await setFontScale(page, fontScale);
        const questionLeading = await cardFor(page)
          .locator("q")
          .evaluate((element) => Number.parseFloat(getComputedStyle(element).lineHeight));
        assert.equal(
          questionLeading,
          baseQuestionLeading * fontScale,
          "question leading must follow font scaling",
        );
        const bodyFontSize = await page.evaluate(() =>
          Number.parseFloat(getComputedStyle(document.body).fontSize),
        );
        assert.equal(bodyFontSize, baseFontSize * fontScale, "body text must follow font scaling");
        const card = cardFor(page);
        const options = layoutOptions.map((label) =>
          card.getByRole("button", { name: label, exact: true }),
        );
        const measures = await Promise.all(options.map(buttonMeasure));
        for (const measure of measures) assertContained(measure, viewport.width <= 760);
        for (let i = 0; i < measures.length; i++)
          for (let j = i + 1; j < measures.length; j++)
            assert(!overlaps(measures[i]!.rect, measures[j]!.rect), "option borders overlap");
        const viewportSize = await page.evaluate(() => ({
          innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
        }));
        assert(
          viewportSize.scrollWidth <= viewportSize.innerWidth,
          `${viewport.width}px page overflows to ${viewportSize.scrollWidth}px`,
        );
        const name = `${browserName}-${viewport.width}x${viewport.height}-font-${fontScale * 100}`;
        if (
          fontScale === 1 ||
          (viewport.width === 390 && fontScale > 1) ||
          (viewport.width === 320 && fontScale === 2)
        )
          await page.screenshot({ path: join(output, `${name}.png`) });
        for (const control of [
          ...options,
          card.getByLabel("补充说明或其他回答"),
          card.getByRole("button", { name: "发送给 worker" }),
          page.getByLabel("消息", { exact: true }),
        ])
          await reachable(control);
        assert.equal(errors.length, 0, errors.join("\n"));
        geometry.push({
          case: name,
          bodyFontSize,
          questionLeading,
          viewport: viewportSize,
          card: await rect(card),
          options: measures,
        });
        console.log(`PASS layout ${name}`);
      } catch (error) {
        await writeFile(
          join(output, `${browserName}-layout-failure.json`),
          JSON.stringify({
            viewport,
            fontScale,
            errors,
            consoleErrors,
            body: await page.locator("body").innerText(),
          }),
        );
        await page.screenshot({ path: join(output, `${browserName}-layout-failure.png`) });
        throw error;
      } finally {
        await context.close();
      }
    }
  }
}

async function checkNormalInboxAndTaskAnswer() {
  for (const width of [390, 1440]) {
    reset();
    normalInbox = true;
    const { context, page, errors, consoleErrors } = await openPage(width, 900);
    try {
      const pending = page.getByRole("complementary", { name: "当前待处理" });
      await pending.getByText(question, { exact: true }).waitFor();
      await pending.getByRole("link", { name: "查看并处理", exact: true }).click();
      const inbox = page.getByRole("region", { name: "待处理事项", exact: true });
      await inbox.getByRole("radio").first().waitFor();
      assert.equal(await inbox.getByRole("radio").count(), originalOptions.length);
      await inbox.getByRole("radio").first().check();
      assert.equal(
        writes.length,
        0,
        "reading/selecting the real pending question is not a submitted answer",
      );
      await inbox
        .getByRole("button", { name: width <= 760 ? "← 返回" : "稍后处理", exact: true })
        .click();
      await page.goto(`http://127.0.0.1:${fixturePort}/?task=${task.task_id}`);
      const detail = page.getByRole("region", { name: "任务详情", exact: true });
      await detail.getByRole("button", { name: originalOptions[0]!, exact: true }).waitFor();
      await detail.getByLabel("补充说明或其他回答").fill("来自原任务详情的回答");
      await detail.getByRole("button", { name: originalOptions[0]!, exact: true }).click();
      assert.equal(writes.at(-1)?.operation, "decisions.respond");
      assert.deepEqual(writes.at(-1)?.target, { type: "decision", id: "decision_fixture" });
      assert.equal((writes.at(-1)?.payload as { version: number }).version, 1);
      await page.screenshot({ path: join(output, `normal-inbox-task-answer-${width}.png`) });
      assert.deepEqual(errors, []);
      assert.deepEqual(consoleErrors, []);
      console.log(`PASS normal Inbox and original TaskDetails worker answer ${width}`);
    } finally {
      await context.close();
    }
  }
}

async function checkLowHeightAndSharedButtons() {
  reset(layoutOptions);
  includeAuxiliary = true;
  const { context, page, errors } = await openPage(390, 568);
  try {
    await cardFor(page).waitFor();
    await page.evaluate(() => {
      const header = document.querySelector("main header") as HTMLElement | null;
      const app = document.querySelector("#root > div") as HTMLElement | null;
      const navigation = document.querySelector('nav[aria-label="视图"]') as HTMLElement | null;
      if (!header || !app || !navigation) throw new Error("safe-area targets not found");
      header.style.paddingTop = "47px";
      header.style.minHeight = "99px";
      app.style.paddingBottom = "77px";
      navigation.style.height = "77px";
      navigation.style.paddingBottom = "21px";
    });
    await setFontScale(page, 1.5);
    await page.screenshot({ path: join(output, `${browserName}-safe-area-chat-390x568.png`) });
    const card = cardFor(page);
    for (const control of [
      ...layoutOptions.map((label) => card.getByRole("button", { name: label, exact: true })),
      card.getByLabel("补充说明或其他回答"),
      card.getByRole("button", { name: "发送给 worker" }),
      page.getByLabel("消息", { exact: true }),
      page.getByRole("navigation", { name: "视图" }),
    ])
      await reachable(control);
    const textarea = card.getByLabel("补充说明或其他回答");
    await textarea.focus();
    assert(await textarea.evaluate((element) => element === document.activeElement));
    const workerSubmit = card.getByRole("button", { name: "发送给 worker" });
    await reachable(workerSubmit);
    const submitBox = await rect(workerSubmit);
    await page.getByRole("button", { name: "下一个待决事项" }).click();
    const effect = page.getByRole("button").filter({ hasText: "重新实现" });
    await reachable(effect);
    assert((await effect.textContent())?.includes("tend 执行"));
    // Task control remains in the actual task detail, not the Attention Inbox.
    await page.goto(`http://127.0.0.1:${fixturePort}/?task=${heldTask.task_id}`);
    const handback = page
      .getByRole("region", { name: "任务详情" })
      .getByRole("button", { name: "交还给 tend" });
    await reachable(handback);
    const handbackBox = await rect(handback);
    const touch = await page.evaluate(() =>
      Number.parseFloat(
        getComputedStyle(document.documentElement).getPropertyValue("--control-touch"),
      ),
    );
    assert(Math.abs(handbackBox.height - touch) <= 1);
    assert(Math.abs(submitBox.height - touch) <= 1);
    await page.screenshot({ path: join(output, `${browserName}-safe-area-390x568.png`) });
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS reachability ${browserName}-safe-area-390x568`);
  } finally {
    await context.close();
  }
}

async function assertJumpBounded(page: Page, jump: Locator, transcript: Locator) {
  const jumpBox = await rect(jump);
  const transcriptBox = await rect(transcript);
  assert(jumpBox.top >= transcriptBox.top && jumpBox.bottom <= transcriptBox.bottom);
  const blockers = [
    page.getByRole("button", { name: "查看发布进度", exact: true }),
    cardFor(page),
    page.getByLabel("消息", { exact: true }).locator(".."),
    page.getByRole("navigation", { name: "视图" }),
  ];
  for (const blocker of blockers) {
    if (await blocker.isVisible()) assert(!overlaps(jumpBox, await rect(blocker)));
  }
}

async function checkJumpRecovery() {
  reset(originalOptions);
  decisionVisible = false;
  const { context, page, errors } = await openPage(390, 844);
  try {
    await page.getByText("历史消息 14", { exact: false }).waitFor();
    const banner = page.getByRole("button", { name: "查看发布进度", exact: true });
    await banner.waitFor();
    assert(
      await banner.evaluate((button) => {
        const bounds = button.getBoundingClientRect();
        const next = button.nextElementSibling!.getBoundingClientRect();
        return (
          Array.from(button.children).every((child) => {
            const box = child.getBoundingClientRect();
            return box.top >= bounds.top && box.bottom <= bounds.bottom;
          }) && bounds.bottom <= next.top
        );
      }),
      "390px upgrade banner contains wrapped text and avoids the next control",
    );
    assert.equal(
      await page
        .getByRole("region", { name: "对话记录" })
        .getByRole("button", { name: "跳到最新" })
        .count(),
      0,
    );
    let state = await showJump(page);
    decisionVisible = true;
    publish("decision.opened");
    await cardFor(page).waitFor();
    await assertJumpBounded(page, state.jump, state.transcript);
    await page.screenshot({ path: join(output, `${browserName}-jump-bounded.png`) });
    upgradeVisible = false;
    publish("upgrade.updated");
    await page
      .getByRole("button", { name: "查看发布进度", exact: true })
      .waitFor({ state: "hidden" });
    await setFontScale(page, 1.5);
    await page.setViewportSize({ width: 430, height: 932 });
    await assertJumpBounded(page, state.jump, state.transcript);
    decisionVisible = false;
    publish("decision.answered");
    await cardFor(page).waitFor({ state: "hidden" });
    await state.jump.click();
    await state.jump.waitFor({ state: "hidden" });
    state = await showJump(page);
    await state.jump.click();
    await state.jump.waitFor({ state: "hidden" });
    assert.equal(
      await page.locator("main header").getByRole("button", { name: "跳到最新" }).count(),
      0,
    );
    assert.equal(errors.length, 0, errors.join("\n"));
    await page.screenshot({ path: join(output, `${browserName}-jump-recovered.png`) });
    console.log(`PASS jump recovery ${browserName}`);
  } finally {
    await context.close();
  }
}

async function checkTranscriptExpansionWithoutFocus() {
  reset();
  decisionVisible = false;
  upgradeVisible = false;
  const { context, page, errors } = await openPage(390, 844);
  try {
    const transcript = page.getByRole("region", { name: "对话记录" });
    await transcript
      .locator("summary")
      .filter({ hasText: /^\d+ 个步骤$/ })
      .last()
      .click();
    const tool = transcript.getByRole("button", { name: /review tool/ });
    await tool.waitFor();
    const before = await rect(tool);
    await tool.evaluate((button) => {
      button.addEventListener("mousedown", (event) => event.preventDefault(), { once: true });
    });
    await tool.click();
    assert.equal(
      await tool.evaluate((button) => document.activeElement === button),
      false,
      "touch-style expansion unexpectedly focused the tool button",
    );
    await page.waitForFunction(
      (button) => button?.getAttribute("aria-expanded") === "true",
      await tool.elementHandle(),
    );
    await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => done(undefined))));
    const after = await rect(tool);
    const transcriptBox = await rect(transcript);
    assert(
      Math.abs(after.top - before.top) <= 2,
      `unfocused tool moved after expanding output: ${before.top} -> ${after.top}`,
    );
    assert(after.top >= transcriptBox.top && after.bottom <= transcriptBox.bottom);
    const jump = transcript.locator("..").getByRole("button", { name: "跳到最新" });
    await jump.waitFor();
    await jump.click();
    await jump.waitFor({ state: "hidden" });
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS unfocused transcript expansion ${browserName}`);
  } finally {
    await context.close();
  }
}

async function checkFocusedTranscriptResizeFollowing() {
  reset();
  decisionVisible = false;
  upgradeVisible = false;
  const { context, page, errors } = await openPage(430, 932);
  try {
    const transcript = page.getByRole("region", { name: "对话记录" });
    await transcript
      .locator("summary")
      .filter({ hasText: /^\d+ 个步骤$/ })
      .last()
      .click();
    const tool = transcript.getByRole("button", { name: /review tool/ });
    await tool.waitFor();
    await tool.focus();
    await transcript.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
      element.dispatchEvent(new Event("scroll", { bubbles: true }));
    });
    await page.setViewportSize({ width: 320, height: 720 });
    appendedHistory.push(
      {
        author: "owner",
        body: "视口变化后的新消息，用于验证焦点留在旧控件时仍会跟随最新。".repeat(12),
        created_at: "2026-10-27T00:40:00Z",
        kind: "message",
        message_id: "msg_fixture_resize_owner",
        seq: 110,
        state: "replied",
      },
      {
        assistant_state: "completed",
        author: "main_agent",
        body: "视口变化后的新回复应自动进入可见区域。".repeat(12),
        created_at: "2026-10-27T00:40:01Z",
        kind: "message",
        message_id: "msg_fixture_resize_agent",
        reply_to: "msg_fixture_resize_owner",
        seq: 111,
      },
    );
    publish("conversation.message");
    await page.getByText(/视口变化后的新回复应自动进入可见区域/).waitFor({ state: "attached" });
    await page.waitForFunction(() => {
      const element = document.querySelector<HTMLElement>('[aria-label="对话记录"]');
      return (
        element !== null && element.scrollHeight - element.scrollTop - element.clientHeight < 2
      );
    });
    assert.equal(
      await transcript.locator("..").getByRole("button", { name: "跳到最新" }).count(),
      0,
    );
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS focused transcript viewport resize ${browserName}`);
  } finally {
    await context.close();
  }
}

async function checkPausedTranscriptAnchor() {
  for (const width of [1440, 390]) {
    reset();
    decisionVisible = false;
    upgradeVisible = false;
    const { context, page } = await openPage(width, 844);
    const older = history[3]!;
    const original = older.body;
    try {
      const transcript = page.getByRole("region", { name: "对话记录" });
      const later = transcript.locator('[data-scroll-id="msg_fixture_owner_8"]');
      await later.scrollIntoViewIfNeeded();
      await transcript.evaluate((node) => {
        node.scrollTop -= 60;
      });
      await page.waitForTimeout(100);
      const y = await later.evaluate((node) => node.getBoundingClientRect().top);
      older.body += "较早回复继续生成。\n\n".repeat(60);
      publish("conversation.message");
      await transcript
        .getByText(/较早回复继续生成/)
        .first()
        .waitFor({ state: "attached" });
      await page.waitForTimeout(100);
      assert(
        Math.abs((await later.evaluate((node) => node.getBoundingClientRect().top)) - y) <= 2,
        "earlier streaming content displaced paused chat reading",
      );
      console.log(`PASS paused transcript anchor ${width}`);
    } finally {
      older.body = original!;
      await context.close();
    }
  }
}

async function checkDelayedStoredThumbnail() {
  for (const width of [1440, 390]) {
    reset();
    decisionVisible = false;
    upgradeVisible = false;
    appendedHistory.push(
      {
        author: "owner",
        kind: "message",
        message_id: "image-owner",
        seq: 101,
        body: "已保存的图片附件",
        state: "replied",
        created_at: "2026-10-27T01:00:00Z",
        attachments: [
          {
            file_id: "stored-image",
            sha256: "0".repeat(64),
            name: "stored-image.png",
            size: 100,
            media_type: "image/png",
          },
        ],
      },
      {
        author: "main_agent",
        kind: "message",
        message_id: "image-reply",
        seq: 102,
        reply_to: "image-owner",
        body: "图片消息之后的末尾回复",
        created_at: "2026-10-27T01:00:01Z",
      },
    );
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    let requested!: () => void;
    const arrived = new Promise<void>((resolve) => {
      requested = resolve;
    });
    await page.route(
      "**/conversation/attachments/stored-image/download-url?inline=1",
      async (route) => {
        requested();
        await pending;
        await route.fulfill({
          json: {
            url: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jHhoAAAAASUVORK5CYII=",
          },
        });
      },
    );
    try {
      await page.goto(`http://127.0.0.1:${fixturePort}/`);
      await arrived;
      const transcript = page.getByRole("region", { name: "对话记录" });
      await page.waitForTimeout(100);
      const before = await transcript.evaluate((el) => ({
        height: el.scrollHeight,
        gap: el.scrollHeight - el.scrollTop - el.clientHeight,
      }));
      assert(before.gap < 2, "initial latest image message must start at bottom");
      release();
      await page.getByRole("button", { name: "查看图片 stored-image.png" }).waitFor();
      await page.waitForTimeout(100);
      const after = await transcript.evaluate((el) => ({
        height: el.scrollHeight,
        gap: el.scrollHeight - el.scrollTop - el.clientHeight,
      }));
      assert.equal(after.height, before.height, "stored thumbnail inserted unreserved height");
      assert(after.gap < 2, "delayed stored thumbnail left latest reply below viewport");
      assert.equal(
        await transcript.locator("..").getByRole("button", { name: "跳到最新" }).count(),
        0,
      );
      console.log(`PASS delayed stored image thumbnail ${width}`);
    } finally {
      release();
      await context.close();
    }
  }
}

async function checkInitialSettingsKeyboard() {
  for (const width of [390, 1440]) {
    for (const key of ["ArrowDown", "ArrowUp"]) {
      reset();
      decisionVisible = false;
      const { context, page, errors } = await openPage(width, width === 390 ? 844 : 900);
      try {
        const settings = page.getByRole("button", { name: "主会话设置", exact: true });
        await settings.focus();
        await page.keyboard.press(key);
        const menu = page.getByRole("menu", { name: "主会话设置菜单", exact: true });
        await menu.waitFor();
        const item = menu.getByRole("menuitem", {
          name: key === "ArrowDown" ? "模型" : "思考强度",
          exact: true,
        });
        assert(
          await item.evaluate((element) => element === document.activeElement),
          "first settings opening lost keyboard focus",
        );
        await page.keyboard.press("Enter");
        await page
          .getByRole("menu", {
            name: key === "ArrowDown" ? "选择模型" : "选择思考强度",
            exact: true,
          })
          .waitFor();
        await page.keyboard.press("Escape");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "hidden" });
        assert(await settings.evaluate((element) => element === document.activeElement));
        assert.equal(errors.length, 0, errors.join("\n"));
        console.log(`PASS initial settings keyboard ${browserName}-${width}-${key}`);
      } finally {
        await context.close();
      }
    }
  }
}

async function checkUsageKeyboard() {
  for (const { width, height } of [
    { width: 390, height: 844 },
    { width: 1440, height: 900 },
    { width: 1440, height: 400 },
  ]) {
    reset();
    decisionVisible = false;
    const { context, page, errors, consoleErrors } = await openPage(width, height);
    try {
      const draft = page.getByRole("textbox", { name: "消息", exact: true });
      await draft.fill("Keep this unfinished draft");
      const usage = page.getByRole("button", { name: "上下文用量 42%", exact: true });
      await usage.waitFor();
      await usage.focus();
      await page.keyboard.press("Enter");
      const panel = page.getByRole("dialog", { name: "上下文用量", exact: true });
      await panel.waitFor();
      await page.waitForFunction(() => document.activeElement?.getAttribute("role") === "dialog");
      await page.keyboard.press("Tab");
      assert.equal(
        await panel
          .getByRole("button", { name: "手动 compact", exact: true })
          .evaluate((e) => e === document.activeElement),
        true,
      );
      await page.keyboard.press("Escape");
      await panel.waitFor({ state: "hidden" });
      assert.equal(await usage.evaluate((e) => e === document.activeElement), true);
      await page.keyboard.press("Enter");
      await panel.waitFor();
      await page.waitForFunction(() => document.activeElement?.getAttribute("role") === "dialog");
      await page.keyboard.press("Tab");
      await page.keyboard.press("Tab");
      assert.equal(
        await panel
          .getByRole("button", { name: "关闭", exact: true })
          .evaluate((e) => e === document.activeElement),
        true,
      );
      await page.keyboard.press("Enter");
      await panel.waitFor({ state: "hidden" });
      assert.equal(await usage.evaluate((e) => e === document.activeElement), true);
      assert.equal(await draft.inputValue(), "Keep this unfinished draft");
      assert.equal(writes.length, 0, "usage keyboard navigation submitted a business request");
      assert.equal(errors.length, 0, errors.join("\n"));
      assert.equal(consoleErrors.length, 0, consoleErrors.join("\n"));
      console.log(`PASS usage keyboard ${browserName}-${width}x${height}`);
    } finally {
      await context.close();
    }
  }
}

async function checkComposerOverlays() {
  reset();
  decisionVisible = false;
  for (const { width, height } of [
    { width: 390, height: 844 },
    { width: 1440, height: 900 },
    { width: 1440, height: 400 },
  ]) {
    const { context, page, errors } = await openPage(width, height);
    try {
      const settings = page.getByRole("button", { name: "主会话设置", exact: true });
      await settings.waitFor();
      await settings.click();
      const menu = page.getByRole("menu", { name: "主会话设置菜单", exact: true });
      await menu.waitFor();
      const modelPane = menu.getByRole("menuitem", { name: "模型", exact: true });
      await reachable(modelPane);
      await modelPane.click();
      const models = page.getByRole("menu", { name: "选择模型", exact: true });
      await models.waitFor();
      const back = models.getByRole("menuitem", { name: "返回主菜单", exact: true });
      await reachable(back);
      await reachable(models.getByRole("menuitemradio", { name: "Model 12", exact: true }));
      await back.click();
      await page.getByRole("menu", { name: "主会话设置菜单", exact: true }).waitFor();
      await page.keyboard.press("Escape");

      const usage = page.getByRole("button", { name: "上下文用量 42%", exact: true });
      await usage.waitFor();
      await usage.click();
      const popover = page.getByRole("dialog", { name: "上下文用量", exact: true });
      await popover.waitFor();
      await reachable(popover.getByRole("heading", { level: 3 }));
      await reachable(popover.getByText("42% 已使用", { exact: true }));
      await reachable(popover.getByText(/tokens/));
      await reachable(popover.getByRole("button", { name: "关闭", exact: true }));
      assert.equal(errors.length, 0, errors.join("\n"));
      if (width === 390)
        await page.screenshot({
          path: join(output, `${browserName}-composer-overlays-390x844.png`),
        });
      if (width === 390) {
        await page
          .getByRole("navigation", { name: "视图", exact: true })
          .getByRole("link", { name: "Issues", exact: true })
          .click();
      } else {
        await page.getByRole("link", { name: "Issues", exact: true }).click();
      }
      await popover.waitFor({ state: "hidden" });
      if (width === 390) {
        const navigation = page.getByRole("navigation", { name: "视图", exact: true });
        await navigation.getByRole("link", { name: "对话", exact: true }).click();
        await usage.waitFor();
        assert.equal(await usage.getAttribute("aria-expanded"), "false");
        await popover.waitFor({ state: "hidden" });
        await usage.click();
        await popover.waitFor();
        await navigation.getByRole("link", { name: "收件箱", exact: true }).click();
        await popover.waitFor({ state: "hidden" });
      }
      console.log(`PASS composer overlays ${browserName}-${width}`);
    } finally {
      await context.close();
    }
  }
}

async function checkConstrainedDockScroll() {
  reset(layoutOptions);
  includeQueue = true;
  const { context, page, errors } = await openPage(390, 844);
  try {
    await cardFor(page).waitFor();
    const queue = page.getByRole("region", { name: "待处理队列", exact: true });
    await queue.waitFor();
    const withdraw = queue.getByRole("button", { name: "撤回", exact: true }).first();
    await reachable(withdraw);
    assert((await rect(queue)).height > 0, "pending queue collapsed");

    await page
      .getByLabel("消息", { exact: true })
      .fill(
        Array.from({ length: 15 }, () => "本地多行草稿用于模拟软键盘后的低高度布局。").join("\n"),
      );
    await page.setViewportSize({ width: 390, height: 400 });
    const reply = cardFor(page).getByLabel("补充说明或其他回答");
    await reachable(reply);
    await reachable(page.getByLabel("消息", { exact: true }));
    const { transcript, jump } = await showJump(page);
    const jumpBox = await rect(jump);
    const transcriptBox = await rect(transcript);
    assert(jumpBox.top >= transcriptBox.top && jumpBox.bottom <= transcriptBox.bottom);
    assert(
      await jump.evaluate((element) => {
        const bounds = element.getBoundingClientRect();
        const hit = document.elementFromPoint(
          bounds.left + bounds.width / 2,
          bounds.top + bounds.height / 2,
        );
        return hit !== null && element.contains(hit);
      }),
      "keyboard-height jump control is not hit-testable",
    );
    await jump.click();
    await jump.waitFor({ state: "hidden" });
    assert.equal(errors.length, 0, errors.join("\n"));
    await page.screenshot({ path: join(output, `${browserName}-keyboard-budget-390x400.png`) });
    console.log(`PASS constrained dock scroll ${browserName}`);
  } finally {
    await context.close();
  }

  for (const viewport of [
    { width: 1440, height: 400 },
    { width: 844, height: 390 },
  ]) {
    reset(layoutOptions);
    includeQueue = true;
    const wide = await openPage(viewport.width, viewport.height);
    try {
      const card = cardFor(wide.page);
      await card.waitFor();
      await wide.page
        .getByLabel("消息", { exact: true })
        .fill(
          Array.from({ length: 15 }, () => "矮宽窗口中的多行草稿不应挤掉决定控件。").join("\n"),
        );
      const queue = wide.page.getByRole("region", { name: "待处理队列", exact: true });
      for (const control of [
        card.getByRole("button", { name: layoutOptions[0]!, exact: true }),
        card.getByRole("button", { name: "发送给 worker", exact: true }),
        queue.getByRole("button", { name: "撤回", exact: true }).first(),
        wide.page.getByLabel("消息", { exact: true }),
      ])
        await reachable(control);
      assert.equal(wide.errors.length, 0, wide.errors.join("\n"));
      console.log(`PASS constrained wide dock ${browserName}-${viewport.width}x${viewport.height}`);
    } finally {
      await wide.context.close();
    }
  }

  reset();
  decisionVisible = false;
  upgradeVisible = false;
  const empty = await openPage(390, 400);
  try {
    const transcript = empty.page.getByRole("region", { name: "对话记录" });
    await transcript.waitFor();
    const seatScroll = empty.page.locator('[class*="seatScroll_"]');
    assert.equal(await seatScroll.isVisible(), false, "empty dock remained in the flex layout");
    const emptyBudget = async () => {
      const header = await rect(empty.page.locator("main header"));
      const composer = await rect(empty.page.getByLabel("消息", { exact: true }).locator(".."));
      const bounds = await rect(transcript);
      assert(
        bounds.height >= composer.top - header.bottom - 24,
        "empty dock left unused space between header and composer",
      );
      return bounds.height;
    };
    const shortHeight = await emptyBudget();
    await empty.page.setViewportSize({ width: 390, height: 568 });
    const tallHeight = await emptyBudget();
    assert(tallHeight >= shortHeight + 140, "empty transcript did not use the taller viewport");
    await empty.page.screenshot({
      path: join(output, `${browserName}-empty-dock-390x568.png`),
    });
    assert.equal(empty.errors.length, 0, empty.errors.join("\n"));
    console.log(`PASS empty low-height transcript budget ${browserName}`);
  } finally {
    await empty.context.close();
  }
}

async function checkAttachmentDraftDock() {
  for (const viewport of [
    { width: 1440, height: 800 },
    { width: 1440, height: 600 },
    { width: 390, height: 568 },
  ]) {
    reset(layoutOptions);
    includeQueue = true;
    const { context, page, errors, consoleErrors } = await openPage(
      viewport.width,
      viewport.height,
    );
    try {
      const card = cardFor(page);
      await card.waitFor();
      // File inputs can be populated before IndexedDB draft hydration finishes.
      // Wait for the public attachment control to admit interaction first.
      await page.getByRole("button", { name: "添加附件", exact: true }).click({ trial: true });
      await page.getByLabel("选择附件", { exact: true }).setInputFiles(
        Array.from({ length: 16 }, (_, index) => ({
          name: `draft-${index + 1}.txt`,
          mimeType: "text/plain",
          buffer: Buffer.from("Local attachment fixture."),
        })),
      );
      const draft = page.getByLabel("消息", { exact: true });
      await draft.fill(
        Array.from({ length: 30 }, () => "附件和长草稿不应挤掉决定控件。").join("\n"),
      );
      const files = page.getByRole("list", { name: "待发送附件" });
      const { jump, transcript } = await showJump(page);
      await jump.click();
      await page.waitForFunction(() => {
        const element = document.querySelector('[aria-label="对话记录"]');
        return (
          element !== null && element.scrollHeight - element.scrollTop - element.clientHeight < 2
        );
      });
      assert(
        await transcript.evaluate((element) => {
          const r = element.getBoundingClientRect();
          const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
          return (
            r.top >= 0 &&
            r.bottom <= innerHeight &&
            r.height >= 40 &&
            hit !== null &&
            element.contains(hit) &&
            element.scrollHeight - element.scrollTop - element.clientHeight < 2
          );
        }),
        "floating jump did not reveal the latest transcript after focusing a tall draft",
      );
      assert.equal(await files.getByRole("listitem").count(), 16);
      const dock = page.locator('[class*="seatScroll_"]');
      assert((await rect(dock)).height >= 44, "attachments and draft collapsed the decision dock");
      for (const control of [
        card.getByRole("button", { name: layoutOptions[0]!, exact: true }),
        card.getByLabel("补充说明或其他回答"),
        card.getByRole("button", { name: "发送给 worker", exact: true }),
        page
          .getByRole("region", { name: "待处理队列", exact: true })
          .getByRole("button", { name: "撤回", exact: true })
          .first(),
        files.getByRole("button").last(),
        draft,
      ])
        await reachable(control);
      const settings = page.getByRole("button", { name: "主会话设置", exact: true });
      await reachable(settings);
      await settings.click();
      const menu = page.getByRole("menu", { name: "主会话设置菜单", exact: true });
      const modelPane = menu.getByRole("menuitem", { name: "模型", exact: true });
      await reachable(modelPane);
      await modelPane.click();
      await reachable(page.getByRole("menuitemradio", { name: "Model 12", exact: true }));
      await page.keyboard.press("Escape");
      assert.equal(
        await draft.inputValue(),
        Array.from({ length: 30 }, () => "附件和长草稿不应挤掉决定控件。").join("\n"),
      );
      assert.equal(errors.length, 0, errors.join("\n"));
      assert.equal(consoleErrors.length, 0, consoleErrors.join("\n"));
      assert.equal(writes.length, 0, "layout interactions submitted a message or decision");
      await page.screenshot({
        path: join(
          output,
          `${browserName}-attachment-draft-${viewport.width}x${viewport.height}.png`,
        ),
      });
      console.log(
        `PASS attachment and draft dock ${browserName}-${viewport.width}x${viewport.height}`,
      );
    } finally {
      await context.close();
    }
  }
}

async function checkDecisionStates() {
  reset(originalOptions);
  responseMode = "failure";
  responseDelay = 350;
  let opened = await openPage(390, 844);
  try {
    const { page, errors } = opened;
    await cardFor(page).waitFor();
    const card = cardFor(page);
    const textarea = card.getByLabel("补充说明或其他回答");
    const submit = card.getByRole("button", { name: "发送给 worker" });
    await textarea.fill("保留这条失败后可重试的说明");
    await submit.focus();
    await page.keyboard.press("Enter");
    await card.getByRole("button", { name: "正在发送…" }).waitFor();
    assert(await textarea.isDisabled());
    // The first frame after rejection may precede React's enabled-state commit.
    await page.evaluate(() => {
      const requestFrame = window.requestAnimationFrame.bind(window);
      const cancelFrame = window.cancelAnimationFrame.bind(window);
      let earlyFrame = false;
      let cancelled = false;
      window.requestAnimationFrame = (callback) => {
        if (earlyFrame || !document.body.textContent?.includes("fixture rejected answer"))
          return requestFrame(callback);
        earlyFrame = true;
        queueMicrotask(() => {
          if (!cancelled) callback(performance.now());
        });
        return -1;
      };
      window.cancelAnimationFrame = (id) => {
        if (id === -1) cancelled = true;
        else cancelFrame(id);
      };
    });
    for (const option of originalOptions)
      assert(await card.getByRole("button", { name: option, exact: true }).isDisabled());
    await card
      .getByRole("button", { name: originalOptions[0]!, exact: true })
      .evaluate((button) => (button as HTMLButtonElement).click());
    await page
      .getByRole("main")
      .getByText(/fixture rejected answer/)
      .waitFor();
    await reachable(
      page
        .getByRole("status")
        .filter({ hasText: /fixture rejected answer/ })
        .getByRole("button", { name: "知道了", exact: true }),
    );
    assert.equal(writes.length, 1);
    assert.deepEqual(writes[0]!.payload, {
      version: 1,
      text: "保留这条失败后可重试的说明",
    });
    assert.equal(await textarea.inputValue(), "保留这条失败后可重试的说明");
    assert(await submit.isEnabled());
    // Retry focus follows the committed state that re-enables the submit button.
    await page.waitForFunction(
      (element) => element === document.activeElement,
      await submit.elementHandle(),
      { timeout: 3000 },
    );
    assert(await submit.evaluate((element) => element === document.activeElement));
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS delayed failure ${browserName}`);
  } finally {
    await opened.context.close();
  }

  reset(originalOptions);
  responseMode = "failure";
  responseDelay = 350;
  opened = await openPage(390, 568);
  try {
    const { page, errors } = opened;
    await cardFor(page).waitFor();
    await page.evaluate(() => {
      const header = document.querySelector("main header") as HTMLElement | null;
      const app = document.querySelector("#root > div") as HTMLElement | null;
      const navigation = document.querySelector('nav[aria-label="视图"]') as HTMLElement | null;
      if (!header || !app || !navigation) throw new Error("safe-area targets not found");
      header.style.paddingTop = "47px";
      header.style.minHeight = "99px";
      app.style.paddingBottom = "77px";
      navigation.style.height = "77px";
      navigation.style.paddingBottom = "21px";
    });
    await setFontScale(page, 1.5);
    const card = cardFor(page);
    const choice = card.getByRole("button", { name: originalOptions[0]!, exact: true });
    await choice.click();
    await page
      .getByRole("main")
      .getByText(/fixture rejected answer/)
      .waitFor();
    for (const control of [
      choice,
      card.getByLabel("补充说明或其他回答"),
      card.getByRole("button", { name: "发送给 worker" }),
      page.getByLabel("消息", { exact: true }),
      page.getByRole("status").getByRole("button", { name: "知道了", exact: true }),
    ])
      await reachable(control);
    assert.equal(errors.length, 0, errors.join("\n"));
    await page.screenshot({
      path: join(output, `${browserName}-safe-area-failure-recovery-390x568.png`),
    });
    console.log(`PASS safe-area failure recovery ${browserName}`);
  } finally {
    await opened.context.close();
  }

  reset(originalOptions);
  responseMode = "failure";
  responseDelay = 350;
  opened = await openPage(390, 844);
  try {
    const { page, errors } = opened;
    await cardFor(page).waitFor();
    const card = cardFor(page);
    await card.getByLabel("补充说明或其他回答").fill("失败请求说明");
    await card.getByRole("button", { name: "发送给 worker" }).click();
    await card.getByRole("button", { name: "正在发送…" }).waitFor();
    const composer = page.getByLabel("消息", { exact: true });
    await composer.fill("请求期间继续写消息");
    await page
      .getByRole("main")
      .getByText(/fixture rejected answer/)
      .waitFor();
    assert(await composer.evaluate((element) => element === document.activeElement));
    assert.equal(await composer.inputValue(), "请求期间继续写消息");
    assert.equal(writes.length, 1);
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS delayed failure keeps moved focus ${browserName}`);
  } finally {
    await opened.context.close();
  }

  reset(originalOptions);
  responseDelay = 350;
  opened = await openPage(390, 844);
  try {
    const { page, errors } = opened;
    await cardFor(page).waitFor();
    const card = cardFor(page);
    const chosen = card.getByRole("button", { name: originalOptions[0]!, exact: true });
    await chosen.click();
    await page.waitForFunction(() =>
      [...document.querySelectorAll("button")].some((button) => button.disabled),
    );
    await chosen.evaluate((button) => (button as HTMLButtonElement).click());
    await cardFor(page).waitFor({ state: "hidden" });
    assert.equal(writes.length, 1);
    assert.deepEqual(writes[0]!.payload, { version: 1, option: originalOptions[0] });
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS delayed success ${browserName}`);
  } finally {
    await opened.context.close();
  }

  reset(originalOptions);
  pendingOption = originalOptions[0]!;
  opened = await openPage(390, 844);
  try {
    const { page, errors } = opened;
    await page.getByRole("main").getByText(`已回答「${pendingOption}」，等主 Agent 处理`).waitFor();
    assert.equal(
      await cardFor(page).getByRole("button", { name: pendingOption, exact: true }).count(),
      0,
    );
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log(`PASS pending state ${browserName}`);
  } finally {
    await opened.context.close();
  }
}

async function checkDecisionContext() {
  for (const width of [390, 1440]) {
    reset();
    const { context, page, errors } = await openPage(width, 900);
    try {
      const card = cardFor(page);
      await card.waitFor();
      const explanation = card.getByRole("region", { name: "问题与决策说明" });
      // section with aria-label is a named region; its text must not be folded.
      await explanation.getByText("临时目录写入受阻", { exact: true }).waitFor();
      assert.equal(await explanation.locator("details").count(), 0);
      assert.equal(await explanation.getByText("业务源码尚未核验", { exact: true }).count(), 1);
      assert.equal(await card.locator("details[open]").count(), 0);
      await page.screenshot({
        path: join(output, `decision-context-${width}.png`),
        fullPage: true,
      });
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        true,
      );
      await card.getByRole("button", { name: originalOptions[0]!, exact: true }).click();
      await page.waitForFunction(() => !document.body.innerText.includes("Worker 提问"));
      assert.equal(writes.length, 1);
      assert.equal(writes[0]!.operation, "decisions.respond");
      assert.equal(writes[0]!.target.id, "decision_fixture");
      assert.equal(errors.length, 0, errors.join("\n"));
      console.log(`PASS visible decision context and original answer ${width}`);
    } finally {
      await context.close();
    }
  }
}

async function checkSharedSteerTranscript() {
  for (const width of [390, 1440]) {
    reset();
    sharedTranscript = true;
    decisionVisible = false;
    upgradeVisible = false;
    const opened = await openPage(width, 900);
    try {
      const { page, errors } = opened;
      const transcript = page.getByRole("region", { name: "对话记录", exact: true });
      await transcript.getByText("SHARED_FINAL_ONCE", { exact: true }).waitFor();
      assert.equal(await transcript.getByText("SHARED_REPLY_ONCE", { exact: true }).count(), 1);
      assert.equal(await transcript.getByText("SHARED_FINAL_ONCE", { exact: true }).count(), 1);
      const text = await transcript.innerText();
      assert(text.indexOf("SHARED_SUPPLEMENT_INPUT") < text.indexOf("SHARED_REPLY_ONCE"));
      assert(text.indexOf("SHARED_LATER_INPUT") < text.indexOf("SHARED_FINAL_ONCE"));
      const more = transcript.getByRole("button", { name: "继续读取本轮消息", exact: true });
      assert.equal(await more.count(), 1, "deduplication lost a source block's continuation");
      await more.click();
      await transcript.getByText("SHARED_MORE_OUTPUT", { exact: true }).waitFor();
      assert.equal(await more.count(), 0);
      assert.equal(await transcript.getByText("SHARED_REPLY_ONCE", { exact: true }).count(), 1);
      assert.equal(await transcript.getByText("SHARED_FINAL_ONCE", { exact: true }).count(), 1);
      assert.equal(errors.length, 0, errors.join("\n"));
      assert.equal(writes.length, 0);
      await page.screenshot({ path: join(output, `${browserName}-shared-steer-${width}.png`) });
      console.log(
        `PASS shared steer transcript ${browserName}-${width}: unique replies, source order, continuation`,
      );
    } finally {
      await opened.context.close();
    }
  }
}

try {
  if (process.env.TEND_DECISION_CONTEXT === "1") {
    await checkDecisionContext();
  } else {
    await checkNormalInboxAndTaskAnswer();
    await checkLayoutMatrix();
    await checkLowHeightAndSharedButtons();
    await checkJumpRecovery();
    await checkTranscriptExpansionWithoutFocus();
    await checkFocusedTranscriptResizeFollowing();
    await checkDelayedStoredThumbnail();
    await checkPausedTranscriptAnchor();
    await checkComposerOverlays();
    await checkUsageKeyboard();
    await checkInitialSettingsKeyboard();
    await checkConstrainedDockScroll();
    await checkAttachmentDraftDock();
    await checkDecisionStates();
    await checkSharedSteerTranscript();
  }
  await writeFile(
    join(output, `${browserName}-geometry.json`),
    `${JSON.stringify(geometry, null, 2)}\n`,
  );
  console.log(`Browser ${browser.version()}; output ${output}`);
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
