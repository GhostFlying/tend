import { issueBoardFixture } from "./issue-fixture.ts";
/// <reference lib="dom" />
// Render the built app against synthetic ledger projections, never a deployed API.
import {
  issueSidebar,
  openIssueAux,
  openIssueDefinition,
  closeIssue,
} from "./issue-detail-helpers.ts";
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, type Locator } from "playwright";
import type {
  Envelope,
  Snapshot,
  TaskDetail,
  UpgradeView,
  IssueView,
  IssueSummary,
  WorkItemChoice,
} from "../src/api/generated/api.ts";

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
  issue_ids: [id],
  primary_issue_id: id,
  primary_issue_title: "改善移动端Issue的阅读体验",
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
const decision = {
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
function initialItems(): IssueView[] {
  return Array.from({ length: 10 }, (_, index) => ({
    issue_id: index ? `${id.slice(0, -1)}${"56789ABCD"[index - 1]}` : id,
    plan: {
      title:
        index === 0
          ? "改善移动端Issue的阅读体验"
          : index === 4
            ? "长标题Issue：核查移动端最后一张记录以及仍然需要持续跟进的承诺，标题应当完整换行，全部定义与交付操作保持可达"
            : `持续跟进目标 ${index + 1}`,
      project: index === 4 ? longProject : index === 2 || index === 8 ? "docs" : "tend",
      intent: "保留真实账本语义和未完成承诺。",
      scope: "当前范围V2",
      completion_criteria: ["当前完成条件V2"],
      background: [{ kind: "fact", text: "仅用于验证的共享背景。", source: "synthetic fixture" }],
      stages: [
        { stage_id: "investigate", title: "调查与实现", status: "done", task_ids: [taskId] },
        { stage_id: "next", title: "讨论后续安排", status: "planned" },
      ],
      follow_ons: [
        {
          follow_on_id: "follow-example",
          kind: "commitment",
          status: index === 8 ? "transferred" : "open",
          text: "后续仍需明确安排。",
          source: "synthetic fixture",
          ...(index === 8 ? { transferred_to: taskId } : {}),
        },
      ],
      default_workspace_task_id: taskId,
      deliveries: [
        {
          delivery_id: "pr-example",
          provider: "github",
          repository: "example/repo",
          number: 1,
          url: "https://example.invalid/review/1",
        },
      ],
    },
    revision: 2,
    assignment_epoch: 1,
    depends_on: [],
    assignment_state: "active",
    assignee: { kind: "owner", ref: "Example owner" },
    state: index === 8 ? "closed" : index === 9 ? "cancelled" : "active",
    ...(index === 7 ? { parent_issue_id: id } : {}),
    ...(index === 8
      ? {
          closure: {
            kind: "completed",
            reason: "当前收尾依据V2",
            evidence: [
              { kind: "owner_decision", reference: "decision-retained", summary: "留存用户决定" },
            ],
          },
        }
      : {}),
    created_at: 1,
    updated_at: 1,
    tasks: index === 5 ? [queuedTask] : [task],
  }));
}
function summary(item: IssueView): IssueSummary {
  return {
    issue_id: item.issue_id,
    title: item.plan.title,
    project: item.plan.project,
    state: item.state,
    revision: item.revision,
    assignee: item.assignee,
    assignment_epoch: item.assignment_epoch,
    assignment_state: item.assignment_state,
    ...(item.parent_issue_id ? { parent_issue_id: item.parent_issue_id } : {}),
  };
}
function history(item: IssueView): IssueView {
  return {
    ...item,
    revision: 1,
    state: "closed",
    plan: {
      ...item.plan,
      title: "历史定义V1",
      intent: "历史目标V1",
      scope: "历史范围V1",
      completion_criteria: ["历史完成条件V1"],
      background: [{ kind: "user_constraint", text: "历史用户约束V1", source: "原用户说明" }],
      follow_ons: [
        {
          follow_on_id: "old-follow",
          kind: "problem",
          status: "transferred",
          text: "历史问题转交V1",
          source: "原记录",
          transferred_to: taskId,
          evidence: [
            { kind: "acceptance", summary: "历史验收依据V1", reference: "acceptance-original" },
          ],
        },
      ],
    },
    closure: {
      kind: "completed",
      reason: "历史收尾V1",
      evidence: [
        { kind: "owner_decision", summary: "历史用户依据V1", reference: "choice-original" },
      ],
    },
  };
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
assert.equal(new Set(items.map((item) => item.issue_id)).size, items.length);
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
    decisions: [],
    hosts,
    queue: [],
    projects: ["tend", "docs", "child-project", longProject].map((project) => ({
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
  res.end(JSON.stringify(issueBoardFixture(res.req.url, value)));
}
function pageItems(url: URL) {
  const project = url.searchParams.get("project"),
    parent = url.searchParams.get("parent_id");
  const matches = items.filter(
    (item) =>
      (!project || item.plan.project === project) &&
      (parent === null || (!parent ? !item.parent_issue_id : item.parent_issue_id === parent)) &&
      (url.searchParams.get("include_closed") === "true" ||
        !["closed", "cancelled"].includes(item.state)),
  );
  const after = url.searchParams.get("after");
  return {
    items: (after ? matches.slice(3) : matches.slice(0, 3)).map(summary),
    more: !after && matches.length > 3,
    ...(!after && matches.length > 3 ? { next: "page2" } : {}),
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
  if (path.endsWith("/activity") || path.endsWith("/results"))
    return json(res, { items: [], more: false, event_seq: 0 });
  if (path === "/api/v1/issues") {
    if (fail)
      return json(res, { error: { code: "internal", message: "Synthetic read failure" } }, 500);
    if (pause && url.searchParams.get("include_closed") === "true") {
      held.push(res);
      return;
    }
    return json(res, pageItems(url));
  }
  if (path.startsWith("/api/v1/issues/")) {
    const selected = items.find((item) => item.issue_id === path.split("/")[4]);
    if (path.endsWith("/context"))
      return json(res, { issue_id: selected?.issue_id, context_revision: 1, sources: [] });
    if (path.endsWith("/deliveries")) return json(res, { items: [] });
    if (path.endsWith("/followups"))
      return json(res, {
        enabled: false,
        items: [],
        issue_id: selected?.issue_id,
        event_seq: event,
      });
    if (selected)
      return json(res, url.searchParams.get("revision") === "1" ? history(selected) : selected);
  }
  if (path === "/api/v1/inbox" || path === "/api/v1/next-actions") return json(res, { items: [] });
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
  if (path === "/api/v1/conversation")
    return json(res, {
      items: [
        {
          message_id: "choice-message",
          seq: 1,
          kind: "message",
          author: "main_agent",
          body: "原问题保留原回答业务",
          created_at: "2026-01-01T00:00:00Z",
          choices: [planChoice],
        },
      ],
      more: false,
    });
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
      `event: ledger\ndata: ${JSON.stringify({ event_seq: event, object_type: "issue", object_id: id, kind: "issue.updated", created_at: "2026-01-01T00:00:00Z" })}\n\n`,
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
    fail = pause = false;
    hosts = initialHosts().slice(0, 2);
    upgradeVisible = false;
    planChoice.status = "open";
    planChoice.version = 1;
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
    const openBoard = async () => {
      if (width <= 760)
        await page
          .getByRole("navigation", { name: "视图" })
          .getByRole("button", { name: "Issue", exact: true })
          .click();
      else await page.getByRole("button", { name: "Issue · 白板 ›", exact: true }).click();
    };
    await openBoard();
    await page.getByRole("tab", { name: "层级", exact: true }).click();
    await page.getByLabel("显示已结束").uncheck();
    const board = page.getByRole("region", { name: "Issue白板", exact: true });
    const title = board.getByRole("button", { name: items[0]!.plan.title, exact: true });
    await title.waitFor();
    assert.equal(await board.getByRole("treeitem").count(), 3);
    await board.getByRole("button", { name: "下一页" }).click();
    await board.getByRole("button", { name: items[4]!.plan.title, exact: true }).waitFor();
    await reachable(board.getByRole("button", { name: items[4]!.plan.title, exact: true }));
    assert(await board.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `tree-last-${width}.png`) });
    const filter = board.getByLabel("按项目筛选Issue");
    await filter.fill("docs");
    await board.getByRole("button", { name: "持续跟进目标 3", exact: true }).waitFor();
    assert.equal(await board.getByRole("treeitem").count(), 1);
    await board.getByLabel("显示已结束").check();
    await board.getByRole("button", { name: "持续跟进目标 9", exact: true }).waitFor();
    assert(
      reads.some(
        (path) =>
          new URL(path, "http://fixture.invalid").searchParams.get("include_closed") === "true",
      ),
    );
    // Project filtering keeps the fixed root scope; children remain in the hierarchy.
    items[7]!.plan.project = "child-project";
    await filter.fill("child-project");
    await board.getByText("暂无 Issue。", { exact: true }).waitFor();
    const query = reads
      .map((path) => new URL(path, "https://fixture.invalid"))
      .findLast(
        (url) =>
          url.pathname === "/api/v1/issues" && url.searchParams.get("project") === "child-project",
      );
    assert.equal(query?.searchParams.get("parent_id"), "");
    await page.screenshot({ path: join(output, `cross-project-child-${width}.png`) });
    await board.getByRole("button", { name: "任务执行记录", exact: true }).click();
    await page.getByRole("button", { name: "返回Issue", exact: true }).click();
    assert.equal(await filter.inputValue(), "child-project");
    await filter.fill("");
    await title.waitFor();
    await board.getByRole("button", { name: `展开 ${items[0]!.plan.title}`, exact: true }).click();
    await board.getByRole("button", { name: items[7]!.plan.title, exact: true }).waitFor();
    // Closed/cancelled children use the same include_closed selection.
    items[7]!.state = "cancelled";
    publish();
    await board
      .getByText("已取消", { exact: true })
      .waitFor()
      .catch(async (error) => {
        await writeFile(
          join(output, "child-diagnostic.json"),
          JSON.stringify({ reads: reads.slice(-30), body: await board.innerText() }, null, 2),
        );
        await page.screenshot({ path: join(output, "child-diagnostic.png") });
        throw error;
      });
    await board.getByLabel("显示已结束").uncheck();
    await board.getByText("已取消", { exact: true }).waitFor({ state: "hidden" });
    await board.getByLabel("显示已结束").check();
    await title.waitFor();
    await board.getByRole("button", { name: `展开 ${items[0]!.plan.title}`, exact: true }).click();
    await board.getByText("已取消", { exact: true }).waitFor();
    await page.screenshot({ path: join(output, `closed-child-${width}.png`) });
    await title.click();
    const detail = page;
    await detail.getByRole("heading", { name: items[0]!.plan.title, exact: true }).waitFor();
    const side = await issueSidebar(page);
    await side.getByRole("heading", { name: "执行记录", exact: true }).waitFor();
    await side
      .getByRole("button", { name: new RegExp(task.title) })
      .first()
      .click();
    const taskDetail = page.getByRole("dialog", { name: "任务详情", exact: true });
    await taskDetail
      .getByText(task.title, { exact: true })
      .filter({ visible: true })
      .first()
      .waitFor();
    await taskDetail.getByRole("button", { name: /返回Issue/ }).click();
    await openIssueDefinition(page);
    await detail.getByText("后续仍需明确安排。", { exact: true }).waitFor();
    const history = await openIssueAux(page, "历史");
    const version = history.getByLabel("版本", { exact: true });
    await version.fill("1");
    await history.getByText("历史问题转交V1", { exact: true }).waitFor();
    for (const summary of await detail
      .locator("summary")
      .filter({ hasText: /^留存依据/ })
      .all())
      await summary.click();
    for (const value of [
      "历史范围V1",
      "历史完成条件V1",
      "历史用户约束V1",
      "历史问题转交V1",
      "历史验收依据V1",
      "历史收尾V1",
      "历史用户依据V1",
    ])
      await history.getByText(value, { exact: true }).waitFor();
    assert.equal(await history.getByText("当前完成条件V2", { exact: true }).count(), 0);
    await page.screenshot({ path: join(output, `history-full-${width}.png`) });
    await closeIssue(page);
    await title.waitFor({ state: "visible" });
    await page.waitForFunction(
      (element) => element === document.activeElement,
      await title.elementHandle(),
    );
    assert(await title.evaluate((el) => el === document.activeElement));
    await filter.fill("docs");
    await board.getByLabel("显示已结束").check();
    await board.getByRole("button", { name: "持续跟进目标 9", exact: true }).click();
    await openIssueDefinition(page);
    await detail.getByText("当前收尾依据V2", { exact: true }).waitFor();
    await detail
      .locator("summary")
      .filter({ hasText: /^留存依据/ })
      .click();
    await detail.getByText("留存用户决定", { exact: true }).waitFor();
    await closeIssue(page);
    await filter.fill("tend");
    await board.getByRole("button", { name: "下一页", exact: true }).click();
    await board.getByRole("button", { name: "持续跟进目标 10", exact: true }).waitFor();
    await board.getByText("已取消", { exact: true }).waitFor();
    const pageQuery = reads
      .map((path) => new URL(path, "http://fixture.invalid"))
      .findLast(
        (url) => url.pathname === "/api/v1/issues" && url.searchParams.get("after") === "page2",
      );
    assert.equal(pageQuery?.searchParams.get("project"), "tend");
    assert.equal(pageQuery?.searchParams.get("include_closed"), "true");
    await filter.fill("docs");
    await board.getByRole("button", { name: "持续跟进目标 9", exact: true }).waitFor();
    // Refresh through the original lifecycle; a failed read retains last facts.
    fail = true;
    publish();
    await board.getByText("暂时无法读取，已有内容仍保留。", { exact: false }).waitFor();
    assert.equal(
      await board.getByRole("button", { name: "持续跟进目标 9", exact: true }).count(),
      1,
    );
    await page.screenshot({ path: join(output, `read-error-${width}.png`) });
    fail = false;
    await board.getByRole("button", { name: "重新读取", exact: true }).click();
    await board.getByText("暂时无法读取", { exact: false }).waitFor({ state: "hidden" });
    await context.setOffline(true);
    for (const stream of streams) stream.end();
    await board.getByText("等待连接恢复 · 当前显示最后读取的Issue", { exact: true }).waitFor();
    assert.equal(
      await board.getByRole("button", { name: "持续跟进目标 9", exact: true }).count(),
      1,
    );
    await context.setOffline(false);
    await page.evaluate(() => window.dispatchEvent(new Event("online")));
    await page.getByRole("button", { name: "返回对话", exact: true }).click();
    // Preserve the actual historical Choice question/version/answer effect, not an Attention surrogate.
    const choice = page.getByRole("region", { name: "下一步安排" });
    await choice.getByRole("heading", { name: planChoice.question }).waitFor();
    await choice.getByText("其他安排", { exact: true }).click();
    await choice.getByLabel("你的安排").fill("先让我看看效果，再安排上线");
    await choice.getByRole("button", { name: "发送回答", exact: true }).click();
    await page.getByText(`已回答 · ${planChoice.question}`, { exact: true }).waitFor();
    assert.equal(planChoice.answer, "先让我看看效果，再安排上线");
    assert.equal(writes.at(-1)?.operation, "work_item_choices.respond");
    await openBoard();
    items = [];
    publish();
    await board.getByText("暂无 Issue。", { exact: true }).waitFor();
    await page.screenshot({ path: join(output, `empty-${width}.png`) });
    assert.deepEqual(errors, []);
    results.push({ width, reads: reads.length, writes: writes.length });
    console.log(
      `PASS canonical Issue ${width}: project/child/closed/pagination, complete selected history and current closure, task/acceptance entry, original Choice, refresh/error/offline/empty`,
    );
    await context.close();
  }
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2) + "\n");
} finally {
  await browser.close();
  for (const response of held) response.end();
  for (const response of streams) response.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
