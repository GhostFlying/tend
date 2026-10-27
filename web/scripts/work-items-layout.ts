/// <reference lib="dom" />
// Render the built app against synthetic ledger projections, never a deployed API.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, type Locator } from "playwright";
import type {
  DecisionView,
  Envelope,
  Snapshot,
  TaskDetail,
  UpgradeView,
  WorkItemView,
  WorkItemChoice,
} from "../src/api/generated/api.ts";
import { columns } from "../src/workitems/model.ts";

const root = fileURLToPath(new URL("../../internal/webui/dist/app/", import.meta.url));
const output = process.env.TEND_LAYOUT_OUTPUT ?? join(tmpdir(), "tend-work-items-layout");
await mkdir(output, { recursive: true });
const id = "wi_01M5M3ME31875GDA8F7P5XTD74";
const taskId = "task_01M5M3ME31875GDA8F7P5XTD74";
const task: TaskDetail = {
  task_id: taskId,
  title: "设计稿与说明",
  project: "tend",
  lifecycle: "completed",
  control_mode: "managed",
  control_revision: 1,
  spec_version: 1,
  rating: "p2",
  block_reason: "none",
  workspace: "example",
  next_implement_generation: 1,
  work_item_ids: [id],
  runs: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
  actions: [],
  spec: {},
};
const longTask: TaskDetail = {
  ...task,
  task_id: `${taskId.slice(0, -1)}5`,
  title:
    "最近完成的长标题执行任务：保留历史证据、交付与验收事实，在窄屏资源列表中完整换行并保持任务详情入口可达",
};
const queuedTask: TaskDetail = {
  ...task,
  task_id: `${taskId.slice(0, -1)}6`,
  title: "等待资源的执行任务",
  lifecycle: "queued",
  execution: {
    state: "queued",
    role: "implement",
    queue_reason: { code: "host_capacity", host_id: "示例主机 1", message: "Waiting for capacity" },
  },
};
const upgrade: UpgradeView = {
  upgrade_id: "upgrade-example",
  release_id: "example-release",
  status: "running",
  phase: "checking",
  revision: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  hosts: [],
};
const decision: DecisionView = {
  decision_id: "decision-example",
  version: 1,
  task_id: taskId,
  question: "是否继续调查？",
  status: "open",
  options: ["继续调查"],
  worker: {
    question_key: "example-question",
    run_id: "run-example",
    role: "implement",
    spec_version: 1,
    question_message_id: "message-example",
    evidence: "仅用于隔离回归的合成问题。",
  },
};
const longProject = "这是一个很长的项目名称用于验证完整换行和筛选输入的可用性";
function initialItems(): WorkItemView[] {
  const states = [
    "unplanned",
    "unplanned",
    "unplanned",
    "unplanned",
    "unplanned",
    "active",
    "checking",
    "attention",
    "closed",
  ] as const;
  return states.map((column, index) => ({
    work_item_id: index ? `${id.slice(0, -1)}${"56789ABC"[index - 1]}` : id,
    title:
      index === 0
        ? "改善移动端工作项的阅读体验"
        : index === 4
          ? "长标题工作项：核查移动端最后一张卡片以及仍然需要持续跟进的承诺，标题应当完整换行，全部阶段与交付操作保持可达"
          : `持续跟进目标 ${index + 1}`,
    project: index === 4 ? longProject : index === 2 || index === 8 ? "docs" : "tend",
    intent: "保留真实账本语义和未完成承诺。",
    status: column === "closed" ? "closed" : "open",
    column,
    revision: 2,
    created_at: 1,
    updated_at: 1,
    background: [{ kind: "fact", text: "仅用于验证的共享背景。", source: "synthetic fixture" }],
    tasks: column === "active" ? [queuedTask] : [task],
    ...(column === "active"
      ? {
          execution: {
            queued: 1,
            accepting: 0,
            cancelled: 0,
            completed: 0,
            idle: 0,
            running: 0,
            unknown: 0,
            paused: 0,
            waiting_owner: 0,
          },
        }
      : {}),
    stages: [
      { stage_id: "investigate", title: "调查与实现", status: "done", task_ids: [taskId] },
      { stage_id: "next", title: "讨论后续安排", status: "planned" },
    ],
    follow_ons: [
      {
        follow_on_id: "follow-example",
        kind: "commitment",
        status: "open",
        text: "后续仍需明确安排。",
        source: "synthetic fixture",
      },
    ],
    deliveries: [
      {
        delivery_id: "pr-example",
        provider: "github",
        repository: "example/repo",
        number: 1,
        url: "https://example.invalid/review/1",
      },
    ],
    observations: [
      { work_item_id: id, delivery_id: "pr-example", generation: 1, state: "unknown" },
    ],
  }));
}
const planChoice: WorkItemChoice = {
  choice_id: "choice-example",
  work_item_id: id,
  version: 1,
  status: "open",
  question: "是否继续完成 PR、合入并上线？",
  options: ["继续上线", "稍后决定"],
  source_id: "event-example",
  message_id: "message-example",
  created_at: 1,
  updated_at: 1,
};
let items = initialItems();
assert.equal(new Set(items.map((item) => item.work_item_id)).size, items.length);
let event = 1;
let fail = false;
let pause = false;
let upgradeVisible = false;
const held: ServerResponse[] = [];
const streams = new Set<ServerResponse>();
const reads: string[] = [];
const writes: Envelope[] = [];
const initialHosts = () =>
  Array.from({ length: 12 }, (_, i) => ({
    host_id: `示例主机 ${i + 1}`,
    online: i !== 1,
    slots: 4,
    open_runs: i ? 0 : 1,
    os: "linux",
    arch: "amd64",
    worker: "orca" as const,
    labels: [],
    ...(i === 1
      ? { maintenance: { mode: "maintenance", phase: "draining", upgrade_id: upgrade.upgrade_id } }
      : {}),
  }));
let hosts = initialHosts().slice(0, 2);
function snapshot(): Snapshot {
  return {
    event_seq: event,
    tasks: [task, longTask, queuedTask],
    ...(upgradeVisible ? { upgrade } : {}),
    decisions: [decision],
    hosts,
    queue: [],
    projects: ["tend", "docs", longProject].map((project) => ({
      project,
      title: project,
      weight: 1,
      queued: 0,
      open_runs: 0,
      open_tasks: 0,
    })),
  };
}
function json(res: ServerResponse, value: unknown, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(value));
}
function pageItems(url: URL) {
  const project = url.searchParams.get("project");
  return {
    items: items.filter(
      (item) =>
        (!project || item.project === project) &&
        (url.searchParams.get("include_closed") !== "false" || item.status !== "closed"),
    ),
  };
}
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://fixture.invalid");
  const path = url.pathname;
  if (path.startsWith("/api/")) reads.push(path + url.search);
  if (path === "/api/v1/session")
    return json(res, {
      token: "fixture",
      login: "Example owner",
      scope: "owner",
      deployment_id: "layout-fixture",
    });
  if (path === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`event: start\ndata: {"event_seq":${event}}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path === "/api/v1/work-item-choices/choice-example") return json(res, planChoice);
  if (path === "/api/v1/snapshot") return json(res, snapshot());
  if (path === "/api/v1/work-items") {
    if (fail)
      return json(res, { error: { code: "internal", message: "Synthetic read failure" } }, 500);
    if (pause && url.searchParams.get("include_closed") === "true") {
      held.push(res);
      return;
    }
    return json(res, pageItems(url));
  }
  if (path.startsWith("/api/v1/work-items/"))
    return json(
      res,
      items.find((item) => item.work_item_id === path.split("/").at(-1)) ?? items[0],
    );
  if (path === `/api/v1/tasks/${taskId}`) return json(res, task);
  if (path === `/api/v1/tasks/${longTask.task_id}`) return json(res, longTask);
  if (path === `/api/v1/tasks/${queuedTask.task_id}`) return json(res, queuedTask);
  if (path === `/api/v1/upgrades/${upgrade.upgrade_id}`) return json(res, { upgrade });
  if (path === "/api/v1/tasks")
    return json(res, {
      tasks: url.searchParams.get("lifecycle") === "completed" ? [task, longTask] : [],
    });
  if (path === "/api/v1/repos")
    return json(res, {
      repos: Array.from({ length: 12 }, (_, i) => ({
        repo:
          i === 11
            ? "a-long-logical-repository-name-that-must-wrap-completely-without-hiding-the-last-registration"
            : `sample/repo-${i + 1}`,
        hosts: [hosts[0]!.host_id],
        retired: i === 1,
      })),
    });
  if (path === "/api/v1/conversation") return json(res, { items: [], more: false });
  if (path === "/api/v1/conversation/settings" || path === "/api/v1/conversation/handoff")
    return json(res, { enabled: false, revision: 1 });
  if (path === "/api/v1/ops" && req.method === "POST") {
    let body = "";
    for await (const chunk of req) body += String(chunk);
    const envelope = JSON.parse(body) as Envelope;
    writes.push(envelope);
    if (envelope.operation === "work_item_choices.respond") {
      assert.equal(envelope.expected_control_revision, planChoice.version);
      planChoice.status = "answered";
      planChoice.answer = (envelope.payload as { answer: string }).answer;
      planChoice.version++;
      return json(res, {
        operation: { request_id: envelope.request_id, status: "succeeded", result: planChoice },
      });
    }
    assert.equal(envelope.operation, "decisions.respond");
    assert.equal(envelope.target.id, decision.decision_id);
    assert.equal((envelope.payload as { version: number }).version, 1);
    decision.status = "answered";
    return json(res, { operation: { status: "succeeded", result: {} } });
  }
  if (path.startsWith("/api/"))
    return json(res, { error: { code: "not_found", message: "Not in synthetic data" } }, 404);
  try {
    const file = resolve(root, `.${path}${path.endsWith("/") ? "index.html" : ""}`);
    assert(file.startsWith(root));
    res.setHeader(
      "Content-Type",
      path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css" : "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.writeHead(404);
    res.end();
  }
});
function publish() {
  event++;
  for (const res of streams)
    res.write(
      `event: ledger\ndata: ${JSON.stringify({ event_seq: event, object_type: "work_item", object_id: id, kind: "work_item.updated", created_at: "2026-01-01T00:00:00Z" })}\n\n`,
    );
}
async function reachable(control: Locator, minHeight = 0) {
  await control.scrollIntoViewIfNeeded();
  const diagnostics = await control.evaluate((el) => {
    const r = el.getBoundingClientRect();
    return {
      rect: r.toJSON(),
      viewport: [innerWidth, innerHeight],
      hit: document
        .elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
        ?.outerHTML.slice(0, 200),
    };
  });
  assert(
    await control.evaluate((el, height) => {
      const rect = el.getBoundingClientRect();
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
      return (
        rect.height >= height &&
        rect.width >= height &&
        // Scrolling rounds fractional CSS pixels; hit testing remains authoritative.
        rect.left >= -1 &&
        rect.right <= innerWidth + 1 &&
        rect.top >= -1 &&
        rect.bottom <= innerHeight + 1 &&
        hit &&
        el.contains(hit)
      );
    }, minHeight),
    `unreachable control: ${await control.textContent()} ${JSON.stringify(diagnostics)}`,
  );
}
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch({
  ...(process.env.TEND_LAYOUT_BROWSER ? { executablePath: process.env.TEND_LAYOUT_BROWSER } : {}),
});
const results: unknown[] = [];
try {
  for (const width of [320, 390, 430, 1280]) {
    items = initialItems();
    hosts = initialHosts().slice(0, 2);
    fail = pause = false;
    upgradeVisible = false;
    decision.status = "open";
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      reducedMotion: "reduce",
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${address.port}`);
    const mobile = width <= 760;
    const nav = page.getByRole("navigation", { name: "视图" });
    const openBoard = async () => {
      if (mobile) await nav.getByRole("button", { name: "工作项", exact: true }).click();
      else await page.getByRole("button", { name: "工作项 · 白板 ›" }).click();
    };
    await openBoard();
    const board = page.getByRole("region", { name: "工作项白板", exact: true });
    await board.getByRole("button", { name: items[0]!.title, exact: true }).waitFor();
    const filter = page.getByLabel("按项目筛选工作项");
    const tablist = page.getByRole("tablist", { name: "工作项状态", exact: true });
    const current = board.locator('[data-mobile-active="true"]');
    const title = board.getByRole("button", { name: items[0]!.title, exact: true });
    const reading = page.locator('[class*="reading"]').filter({ visible: true });
    if (mobile) {
      for (const [index, column] of columns.entries()) {
        const tab = tablist.getByRole("tab", { name: new RegExp(column.title) });
        await reachable(tab, 44);
        await tab.click();
        assert.equal(await current.getAttribute("aria-label"), column.title);
        assert.equal(
          await current.locator("article").count(),
          initialItems().filter((item) => item.column === column.id).length,
        );
        if (index === 4) {
          await current.getByRole("button", { name: "查看待安排" }).click();
          assert.equal(await current.getAttribute("aria-label"), "待安排");
        }
        if (column.id === "active") {
          await current.getByText("等待主机空闲槽位", { exact: true }).waitFor();
          assert.equal(await current.getByLabel("执行概况").count(), 1);
          await page.screenshot({ path: join(output, `queued-${width}.png`) });
        }
      }
      await tablist.getByRole("tab").last().focus();
      await page.keyboard.press("Home");
      assert.equal(await tablist.getByRole("tab").first().getAttribute("aria-selected"), "true");
      await page.keyboard.press("ArrowRight");
      assert.equal(await current.getAttribute("aria-label"), "推进中");
      await page.keyboard.press("Home");
      await reachable(page.getByRole("button", { name: "刷新工作项", exact: true }), 44);
      await reading.evaluate((el) => {
        el.scrollTop = 0;
      });
      await page.getByRole("heading", { name: "工作项", exact: true }).click();
    }
    const geometry = await title.evaluate((el) => {
      const card = el.closest("article")!.getBoundingClientRect();
      return {
        cardLeft: card.left,
        cardTop: card.top,
        titleTop: el.getBoundingClientRect().top,
        cardWidth: card.width,
      };
    });
    await page.screenshot({ path: join(output, `first-${width}.png`) });
    assert(
      await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      "no page overflow",
    );
    assert.equal(
      await page.getByRole("button", { name: /^资源与记录/ }).count(),
      mobile ? 1 : 0,
      "desktop keeps sidebar resources",
    );
    const firstCard = title.locator("..");
    const stages = firstCard.locator("summary");
    await reachable(stages, mobile ? 44 : 0);
    await stages.click();
    upgradeVisible = true;
    publish();
    const banner = page
      .getByRole("button", { name: "查看发布进度", exact: true })
      .filter({ visible: true });
    await banner.waitFor();
    await banner.click();
    const upgradePanel = page.getByRole("dialog", { name: "发布详情", exact: true });
    await upgradePanel.getByRole("heading", { name: upgrade.release_id, exact: true }).waitFor();
    await page.screenshot({ path: join(output, `upgrade-${width}.png`) });
    await page.keyboard.press("Escape");
    await upgradePanel.waitFor({ state: "detached" });
    assert(await banner.evaluate((el) => el === document.activeElement));
    upgradeVisible = false;
    publish();
    await banner.waitFor({ state: "detached" });
    const stageTask = firstCard.getByRole("button", {
      name: "设计稿与说明 已完成",
      exact: true,
    });
    await reachable(stageTask, mobile ? 44 : 0);
    await page.screenshot({ path: join(output, `stages-${width}.png`) });
    await stageTask.click();
    const taskDetail = page.getByRole("dialog", { name: "任务详情", exact: true });
    await taskDetail.getByText(task.title, { exact: true }).waitFor();
    await taskDetail.getByRole("button", { name: "关闭详情", exact: true }).click();
    assert(await stageTask.evaluate((el) => el === document.activeElement));
    await stages.click();
    await filter.fill(longProject);
    await board.getByRole("button", { name: items[4]!.title, exact: true }).waitFor();
    assert(
      reads.some(
        (path) =>
          new URL(path, "http://fixture.invalid").searchParams.get("project") === longProject,
      ),
    );
    await page.getByRole("button", { name: "清除项目筛选" }).click();
    await title.waitFor();
    await filter.fill("docs");
    await board.getByRole("button", { name: "持续跟进目标 3", exact: true }).waitFor();
    if (mobile) assert.equal(await current.locator("article").count(), 1);
    await reachable(page.getByRole("button", { name: "清除项目筛选" }), mobile ? 44 : 0);
    await page.getByRole("button", { name: "清除项目筛选" }).click();
    await title.waitFor();
    assert(await filter.evaluate((el) => el === document.activeElement));
    await filter.fill("missing");
    const empty = (
      mobile ? current : board.getByRole("region", { name: "待安排", exact: true })
    ).getByRole("button", { name: "查看全部项目" });
    await empty.waitFor();
    await page.screenshot({ path: join(output, `filtered-empty-${width}.png`) });
    await empty.click();
    await title.waitFor();
    if (mobile) {
      const last = current.getByRole("button", { name: items[4]!.title, exact: true });
      await reachable(last, 44);
      assert(await last.evaluate((el) => el.scrollWidth <= el.clientWidth));
      const more = current
        .locator("article")
        .last()
        .getByRole("button", { name: "背景、后续与交付" });
      await reachable(more, 44);
      await page.screenshot({ path: join(output, `scroll-${width}.png`) });
      await more.click();
      const lastDetails = page.getByRole("dialog", { name: "工作项详情", exact: true });
      await lastDetails.getByRole("heading", { name: items[4]!.title, exact: true }).waitFor();
      await reachable(lastDetails.getByRole("button", { name: "关闭详情" }), 44);
      await lastDetails.getByRole("button", { name: "关闭详情" }).click();
      assert(await more.evaluate((el) => el === document.activeElement));
      const top = await reading.evaluate((el) => el.scrollTop);
      const entry = page.getByRole("button", { name: /^资源与记录/ });
      const panel = page.getByRole("dialog", { name: "资源与记录", exact: true });
      await reachable(entry, 44);
      await entry.click();
      await panel.waitFor();
      assert.equal(await reading.evaluate((el) => el.scrollTop), top);
      const panelBox = (await panel.boundingBox())!;
      await page.mouse.click(panelBox.x + 4, panelBox.y + 4);
      assert(await panel.isVisible(), "clicking the panel surface keeps it open");
      await page.mouse.move(2, 70);
      await page.mouse.wheel(0, 400);
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      assert.equal(
        await reading.evaluate((el) => el.scrollTop),
        top,
        "modal prevents background wheel scrolling",
      );
      await page.screenshot({ path: join(output, `resources-${width}.png`) });
      await page.keyboard.press("Tab");
      assert(await panel.evaluate((el) => el.contains(document.activeElement)));
      await page.keyboard.press("Escape");
      await panel.waitFor({ state: "detached" });
      assert(await entry.evaluate((el) => el === document.activeElement));
      assert.equal(await reading.evaluate((el) => el.scrollTop), top);
      await entry.click();
      hosts = initialHosts();
      publish();
      const host = panel.getByRole("button", { name: "主机详情：示例主机 12", exact: true });
      await reachable(host, 44);
      await host.click();
      const hostPanel = page.getByRole("dialog", { name: "主机详情", exact: true });
      await hostPanel.waitFor();
      await page.keyboard.press("Escape");
      await hostPanel.waitFor({ state: "detached" });
      assert(await host.evaluate((el) => el === document.activeElement));
      await panel.getByRole("tab", { name: "已注册仓库" }).click();
      await panel.getByText("全部 12 个 · 含已归档仓库").waitFor();
      await reachable(panel.getByRole("heading", { name: /^a-long-logical/ }));
      assert(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth));
      await page.screenshot({ path: join(output, `resources-repos-${width}.png`) });
      await panel.getByRole("tab", { name: "已注册仓库" }).focus();
      await page.keyboard.press("Home");
      assert.equal(
        await panel.getByRole("tab", { name: "执行主机" }).getAttribute("aria-selected"),
        "true",
      );
      const maintainingHost = panel.getByRole("button", {
        name: "主机详情：示例主机 2",
        exact: true,
      });
      await maintainingHost.click();
      await hostPanel.getByRole("button", { name: "查看发布详情", exact: true }).click();
      await upgradePanel.getByRole("heading", { name: upgrade.release_id, exact: true }).waitFor();
      await page.keyboard.press("Escape");
      await upgradePanel.waitFor({ state: "detached" });
      assert(await maintainingHost.evaluate((el) => el === document.activeElement));
      await panel.getByRole("tab", { name: "执行主机" }).focus();
      await page.keyboard.press("End");
      assert.equal(
        await panel.getByRole("tab", { name: "最近完成" }).getAttribute("aria-selected"),
        "true",
      );
      await panel.getByRole("tab", { name: "最近完成" }).click();
      const longRecent = panel.getByRole("button", {
        name: `${longTask.title} 已完成 未关联工作项`,
        exact: true,
      });
      await reachable(longRecent, 44);
      assert(
        await longRecent.evaluate(
          (el) => el.scrollHeight <= el.clientHeight && el.scrollWidth <= el.clientWidth,
        ),
        "recent long titles fit their rows",
      );
      await page.screenshot({ path: join(output, `resources-recent-${width}.png`) });
      const recent = panel.getByRole("button", {
        name: "设计稿与说明 已完成 未关联工作项",
        exact: true,
      });
      await reachable(recent, 44);
      await recent.click();
      await panel.waitFor({ state: "detached" });
      const detail = page.getByRole("dialog", { name: "任务详情", exact: true });
      await detail.getByText(task.title, { exact: true }).waitFor();
      await detail.getByRole("button", { name: "关闭详情", exact: true }).click();
      assert(await entry.evaluate((el) => el === document.activeElement));
      await entry.click();
      await panel.getByRole("button", { name: "查看任务执行" }).click();
      await page.getByRole("heading", { name: "任务执行", exact: true }).waitFor();
      await page.getByRole("button", { name: "返回工作项" }).click();
      await title.waitFor();
      await reading.evaluate((el) => {
        el.scrollTop = 0;
      });
    } else {
      await reachable(board.getByRole("region", { name: "已结束", exact: true }));
      await page.screenshot({ path: join(output, `desktop-last-column-${width}.png`) });
      await reachable(title);
    }
    const originalTasks = items[0]!.tasks ?? [];
    items[0]!.tasks = [
      {
        ...task,
        task_id: "task-running",
        title: "实现已批准页面",
        lifecycle: "running",
        execution: { state: "running" },
      },
      { ...queuedTask },
      {
        ...task,
        task_id: "task-starting",
        title: "正在准备验收",
        lifecycle: "awaiting_acceptance",
        execution: { state: "starting", role: "accept" },
      },
      { ...task, completed_at: 10, execution: { state: "completed" } },
      {
        ...task,
        task_id: "task-cancelled",
        title: "保留取消记录",
        lifecycle: "cancelled",
        ended_at: 20,
        execution: { state: "cancelled" },
      },
    ];
    items[0]!.stages!.push({
      stage_id: "implement",
      title: "实现与交互验证",
      status: "active",
      task_ids: ["task-running", "task-starting", queuedTask.task_id],
    });
    await title.click();
    const detail = page.getByRole("dialog", { name: "工作项详情", exact: true });
    await detail.getByRole("heading", { name: items[0]!.title, exact: true }).waitFor();
    const taskTabs = detail.getByRole("tablist", { name: "关联任务筛选" });
    await detail.getByRole("button", { name: "1 执行中", exact: true }).waitFor();
    assert.equal(
      await detail
        .getByRole("tab", { name: "当前（3）", exact: true })
        .getAttribute("aria-selected"),
      "true",
    );
    assert(
      await detail
        .getByRole("button", { name: /实现已批准页面/ })
        .first()
        .evaluate((el) => el.getBoundingClientRect().bottom < innerHeight),
    );
    await page.screenshot({ path: join(output, `detail-current-${width}.png`) });
    await taskTabs.getByRole("tab", { name: "已结束（2）", exact: true }).click();
    await detail.getByRole("button", { name: /保留取消记录/ }).waitFor();
    await detail.getByRole("button", { name: "1 执行中", exact: true }).waitFor();
    await page.screenshot({ path: join(output, `detail-ended-${width}.png`) });
    await detail.getByRole("button", { name: "1 执行中", exact: true }).click();
    assert.equal(await detail.getByRole("tabpanel").getByRole("button").count(), 2);
    const activeTab = taskTabs.getByRole("tab", { name: /^当前/ });
    await activeTab.focus();
    publish();
    await page.waitForTimeout(250);
    assert.equal(await detail.getByText("筛选：执行中", { exact: true }).count(), 1);
    assert(await activeTab.evaluate((el) => el === document.activeElement));
    await detail.getByRole("button", { name: "清除状态筛选" }).click();
    await activeTab.press("ArrowRight");
    assert.equal(
      await taskTabs.getByRole("tab", { name: /^已结束/ }).getAttribute("aria-selected"),
      "true",
    );
    await taskTabs.getByRole("tab", { name: /^当前/ }).click();
    planChoice.status = "open";
    planChoice.version = 1;
    items[0]!.choices = [planChoice];
    publish();
    await detail.getByRole("button", { name: "评审材料", exact: true }).click();
    const choiceCard = detail.getByRole("region", { name: "下一步安排" });
    await choiceCard.getByRole("button", { name: "稍后决定", exact: true }).waitFor();
    await choiceCard.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(output, `detail-choice-${width}.png`) });
    await choiceCard.getByText("其他安排", { exact: true }).click();
    await choiceCard.getByLabel("你的安排").fill("先让我看看效果，再安排上线");
    await choiceCard.getByRole("button", { name: "发送回答", exact: true }).click();
    await detail.getByRole("button", { name: "历史", exact: true }).click();
    await detail.getByText("已回答 · 是否继续完成 PR、合入并上线？", { exact: true }).waitFor();
    assert.equal(planChoice.answer, "先让我看看效果，再安排上线");
    assert.equal(await detail.getByRole("button", { name: "继续上线", exact: true }).count(), 0);
    delete items[0]!.choices;
    await detail.getByRole("button", { name: "跟进与交付", exact: true }).click();
    await reachable(detail.getByRole("button", { name: "继续讨论", exact: true }), mobile ? 44 : 0);
    await reachable(
      detail.getByRole("button", { name: "提出暂缓安排", exact: true }),
      mobile ? 44 : 0,
    );
    await reachable(detail.getByRole("link", { name: "example/repo #1 ↗", exact: true }));
    assert.equal(await detail.getByRole("button", { name: "刷新交付状态" }).count(), 1);
    await detail.getByRole("button", { name: "历史", exact: true }).click();
    await detail.getByText("查看计划历史", { exact: true }).click();
    await detail.getByRole("button", { name: "读取历史计划" }).click();
    await detail
      .getByText("历史计划与留存依据；不包含当前执行和观察状态。", { exact: false })
      .waitFor();
    await detail.getByRole("button", { name: "回到当前计划" }).click();
    await reachable(detail.getByRole("button", { name: "关闭详情" }), mobile ? 44 : 0);
    await detail.getByRole("button", { name: "关闭详情" }).click();
    assert(await title.evaluate((el) => el === document.activeElement));
    items[0]!.tasks = originalTasks;
    items[0]!.stages!.pop();
    if (mobile)
      await reading.evaluate((el) => {
        el.scrollTop = 0;
      });
    const countBeforeRefresh = reads.length;
    await Promise.all([
      page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/work-items"),
      page.getByRole("button", { name: "刷新工作项", exact: true }).click(),
    ]);
    assert(reads.length > countBeforeRefresh);
    fail = true;
    await page.getByRole("button", { name: "刷新工作项", exact: true }).click();
    await page.getByRole("button", { name: "重试工作项" }).waitFor();
    assert.equal(await title.count(), 1, "failed refresh retains previous facts");
    await page.screenshot({ path: join(output, `failure-${width}.png`) });
    fail = false;
    await page.getByRole("button", { name: "重试工作项" }).click();
    await page.getByRole("button", { name: "重试工作项" }).waitFor({ state: "detached" });
    pause = true;
    await filter.fill("tend");
    await board.getByText("正在读取工作项…").first().waitFor();
    await page.screenshot({ path: join(output, `loading-${width}.png`) });
    pause = false;
    for (const response of held.splice(0))
      json(response, { items: items.filter((item) => item.project === "tend") });
    await title.waitFor();
    await page.getByRole("button", { name: "清除项目筛选" }).click();
    await title.waitFor();
    await context.setOffline(true);
    for (const response of streams) response.end();
    await page.getByText("等待连接恢复 · 当前显示最后读取的工作项", { exact: true }).waitFor();
    assert.equal(await title.count(), 1);
    await page.screenshot({ path: join(output, `offline-${width}.png`) });
    await context.setOffline(false);
    await page
      .getByText("等待连接恢复 · 当前显示最后读取的工作项", { exact: true })
      .waitFor({ state: "detached", timeout: 20000 });
    items[0] = { ...items[0]!, column: "active", revision: 3 };
    publish();
    await page.waitForResponse((response) => response.url().includes("include_closed=true"));
    await page.waitForFunction(
      (itemTitle) =>
        [
          ...document.querySelectorAll(
            '[aria-label="工作项白板"] section[aria-label="推进中"] button',
          ),
        ].some((el) => el.textContent?.trim() === itemTitle),
      items[0]!.title,
    );
    assert.equal(
      await board
        .getByRole("region", { name: "待安排", exact: true })
        .getByRole("button", { name: items[0]!.title, exact: true })
        .count(),
      0,
      "event moves the item out of its old category",
    );
    if (mobile) {
      await tablist.getByRole("tab", { name: /^推进中/ }).click();
      await title.waitFor();
      assert.match((await tablist.getByRole("tab", { name: /^推进中/ }).textContent())!, /2/);
      await current.getByText("等待主机空闲槽位", { exact: true }).waitFor();
    }
    await title.click();
    await detail.getByRole("button", { name: "跟进与交付", exact: true }).click();
    await detail.getByText("后续仍需明确安排。", { exact: true }).waitFor();
    await detail.getByRole("button", { name: "继续讨论", exact: true }).click();
    const composer = page.getByRole("textbox", { name: "消息", exact: true });
    await composer.waitFor();
    assert.match(await composer.inputValue(), /改善移动端工作项/);
    if (mobile) {
      await nav.getByRole("button", { name: /^待处理/ }).click();
      await page.getByRole("button", { name: "继续调查", exact: true }).click();
      assert.equal(writes.at(-1)?.operation, "decisions.respond");
    }
    await openBoard();
    items = [];
    publish();
    await (mobile ? current : board)
      .getByText("暂无持续跟进的工作项", { exact: true })
      .first()
      .waitFor();
    if (mobile) await tablist.getByRole("tab", { name: /^待安排/ }).click();
    if (mobile)
      await reading.evaluate((el) => {
        el.scrollTop = 0;
      });
    await page.screenshot({ path: join(output, `empty-${width}.png`) });
    await (mobile ? current : board)
      .getByRole("button", { name: "回到对话", exact: true })
      .first()
      .click();
    await composer.waitFor();
    assert.deepEqual(errors, []);
    results.push({
      width,
      height: 844,
      geometry,
      pageOverflow: false,
      browserErrors: errors,
      passed: true,
    });
    console.log(
      `PASS ${width}: layout, filters, six states, resources, focus, scrolling, history, drafts, events, empty, loading, failure, offline${mobile ? ", worker answer" : ""}`,
    );
    await context.close();
  }
  await writeFile(
    join(output, "validation.json"),
    JSON.stringify(
      {
        browser: browser.version(),
        syntheticAPI: true,
        results,
        reads,
        writes: writes.map(({ operation, target, payload }) => ({ operation, target, payload })),
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
  for (const response of streams) response.end();
  for (const response of held) response.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
