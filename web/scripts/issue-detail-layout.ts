import { fixtureRoot, navigate } from "./navigation.ts";
/// <reference lib="dom" />
// D129: isolated complete-page and exact-recipient interaction regression.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { resolve, join } from "node:path";
import { chromium } from "playwright";
import { openIssueAux, issueSidebar } from "./issue-detail-helpers.ts";
import type {
  Envelope,
  IssueView,
  IssueActivityPage,
  IssueTranscriptBlock,
  IssueResultPage,
  AttentionView,
} from "../src/api/generated/api.ts";
const output = process.env.TEND_LAYOUT_OUTPUT;
assert(output, "Set TEND_LAYOUT_OUTPUT");
await mkdir(output, { recursive: true });
const root = fixtureRoot();
const now = Date.now();
let seq = 10;
let activityDelay = 0;
let holdEarlier: Promise<void> | null = null;
let earlierStarted = () => {};
let continuationBlock: IssueTranscriptBlock | null = null;
let continuationFailure = false;
let conversationFailure = false;
let continuationDelay = 0;
let thirdPage = false;
let pagedRevision = 0;
let pagedChildren = false;
let pagedActivity = false;
const streams = new Set<ServerResponse>();
const writes: Envelope[] = [];
const requests: string[] = [];
let dropProgressReply = false;
let progressRefusalReady = false;
const progressRequests = new Map<string, Envelope>();
const issue: IssueView = {
  issue_id: "wi_01ARZ3NDEKTSV4RRFFQ69G5FAV",
  state: "active",
  work_status: "in_review",
  assignee: { kind: "agent", ref: "hosted/qingzhu" },
  assignment_epoch: 1,
  assignment_state: "active",
  revision: 3,
  depends_on: [],
  created_at: now,
  updated_at: now,
  plan: {
    title: "发布流程整理",
    project: "sample",
    intent: "让发布步骤、验收证据和回滚入口清晰可追踪。",
    scope: "发布与回滚",
    completion_criteria: ["发布和回滚步骤可重复核对"],
    stages: [],
  },
  progress: {
    summary: "发布步骤与回滚文档已完成，灰度验证等待范围确认。",
    next_step: "确认灰度范围 → 启动验证 → 汇总验收结果",
    issue_revision: 3,
    updated_at: now,
    updated_by: { kind: "agent", ref: "hosted/qingzhu" },
  },
  conversation: { conversation_id: "issue:canonical-root", can_send: true },
  manager: {
    task_id: "task-manager",
    category_id: "issue-manager",
    state: "running",
    host_id: "hosted",
    conversation_id: "issue:canonical-root",
    native_thread_id: "native-manager-not-for-the-landing-page",
  },
  tasks: [],
};
const child = {
  ...issue,
  issue_id: "wi_01ARZ3NDEKTSV4RRFFQ69G5FAW",
  parent_issue_id: issue.issue_id,
  plan: { ...issue.plan, title: "整理发布步骤" },
  ancestors: [{ issue_id: issue.issue_id, title: issue.plan.title, state: issue.state }],
  conversation: { conversation_id: "issue:canonical-child", can_send: true },
  manager: {
    ...issue.manager!,
    task_id: "task-manager-child",
    conversation_id: "issue:canonical-child",
  },
  tasks: issue.tasks!.map((task) => ({ ...task, task_id: "task-manager-child" })),
};
let decision: AttentionView = {
  attention_id: "attention-decision",
  issue_id: issue.issue_id,
  kind: "decision",
  title: "首轮灰度覆盖哪些环境？",
  status: "open",
  revision: 1,
  question_version: 1,
  question_options: ["小范围灰度", "完整灰度"],
  latest_action_reminders: false,
  raised_by: { kind: "main_agent", subject: "manager" },
  created_at: now,
  updated_at: now,
};
const results: IssueResultPage = {
  items: [
    {
      task_id: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV",
      title: "发布与回滚清单",
      run_id: "run-one",
      spec_version: 1,
      generation: 1,
      created_at: now,
      artifact: {
        artifact_id: "artifact-one",
        run_id: "run-one",
        ref: {
          report: {
            body: "### 发布操作清单\n\n- 发布前：确认版本与环境\n- 发布时：分批验证并记录结果\n- 失败时：按清单回滚并核验服务",
          },
        },
      },
      acceptances: [
        {
          acceptance_id: "acceptance-one",
          artifact_id: "artifact-one",
          spec_version: 1,
          verdict: "passed",
          issues: [],
        },
      ],
    },
  ],
  more: false,
};
const activity: IssueActivityPage = {
  event_seq: seq,
  more: false,
  items: [
    {
      id: "conversation-one",
      position: 1001,
      kind: "conversation",
      created_at: now,
      author: "青竹",
      block: {
        position: 1,
        more: false,
        trigger: {
          message_id: "owner-one",
          seq: 1,
          kind: "message",
          author: "owner",
          body: "回滚步骤要包含验证服务恢复的方法。",
          created_at: new Date(now).toISOString(),
          state: "replied",
        },
        items: [
          {
            message_id: "reply-one",
            seq: 2,
            kind: "message",
            author: "main_agent",
            body: "发布步骤和回滚入口已核对。两个子项的交付均已独立验收，接下来需要确定灰度范围。",
            reply_to: "owner-one",
            created_at: new Date(now).toISOString(),
            assistant_state: "completed",
            assistant_revision: 1,
          },
        ],
      },
    },
    {
      id: "result:artifact-one",
      position: 1002,
      kind: "artifact.submitted",
      created_at: now,
      author: "执行 Agent",
      title: "实施已交付",
      artifact_id: "artifact-one",
      task_id: results.items![0]!.task_id,
    },
  ],
};
const publish = () => {
  for (const s of streams)
    s.write(
      `id: ${++seq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: seq, object_type: "issue", object_id: issue.issue_id, kind: "issue.progress_updated", created_at: new Date().toISOString() })}\n\n`,
    );
};
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://localhost");
  const p = u.pathname;
  requests.push(req.url!);
  res.setHeader("Cache-Control", "no-store");
  const json = (value: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(value));
  };
  if (p === "/api/v1/conversation/blocks/owner-one" && continuationBlock) {
    if (continuationDelay) await new Promise((resolve) => setTimeout(resolve, continuationDelay));
    if (continuationFailure)
      return json({ error: { code: "unavailable", message: "Continuation failed" } }, 503);
    if (thirdPage && u.searchParams.get("after") === "53")
      return json({
        ...continuationBlock,
        items: [
          {
            ...continuationBlock.items![0]!,
            message_id: "third-page-reply",
            seq: 54,
            body: "第三页保留消息",
          },
        ],
        more: false,
      });
    if (thirdPage && u.searchParams.get("after") === "51")
      return json({ ...continuationBlock, more: true, next_seq: 53 });
    return json(
      u.searchParams.has("after")
        ? continuationBlock
        : {
            ...continuationBlock,
            items: activity.items![0]!.block!.items,
            more: true,
            next_seq: 51,
          },
    );
  }
  if (p === "/api/v1/session")
    return json({
      token: "fixture",
      login: "owner",
      client_id: "browser",
      scope: "owner",
      deployment_id: "issue-detail-fixture",
    });
  if (p === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`event: start\ndata: {"event_seq":${seq}}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (p === "/api/v1/snapshot")
    return json({
      event_seq: seq,
      tasks: [],
      decisions: [],
      hosts: [],
      projects: [{ project: "sample", name: "发布流程", ready: 0, running: 0 }],
    });
  if (p === "/api/v1/hosted-agents")
    return json({
      projection: {
        agents: [
          {
            identity: { execution_environment_id: "hosted", local_id: "qingzhu" },
            display_name: "青竹",
            purposes: ["issue_manager"],
            enabled: true,
          },
        ],
        runtimes: [],
      },
      configured: true,
    });
  if (p === "/api/v1/issues")
    return json({
      ...(pagedChildren && u.searchParams.get("parent_id") === issue.issue_id
        ? { more: !u.searchParams.has("after"), next: "children-page2" }
        : { more: false }),
      items:
        pagedChildren && u.searchParams.get("parent_id") === issue.issue_id
          ? u.searchParams.has("after")
            ? [{ ...child, title: child.plan.title }]
            : Array.from({ length: 6 }, (_, index) => ({
                ...child,
                issue_id: child.issue_id.slice(0, -1) + index,
                title: `其它子项 ${index}`,
              }))
          : u.searchParams.get("parent_id") === issue.issue_id
            ? [{ ...child, title: child.plan.title, project: "sample" }]
            : u.searchParams.has("parent_id")
              ? []
              : [{ ...issue, title: issue.plan.title, project: "sample" }],
    });
  if (p.startsWith("/api/v1/issues/")) {
    const current = p.includes(child.issue_id) ? child : issue;
    if (p.endsWith("/activity")) {
      if (activityDelay) await new Promise((resolve) => setTimeout(resolve, activityDelay));
      if (pagedActivity && u.searchParams.has("before") && holdEarlier) {
        earlierStarted();
        await holdEarlier;
      }
      if (pagedActivity)
        return json(
          u.searchParams.has("before")
            ? {
                event_seq: seq,
                more: false,
                items: [
                  {
                    id: "earlier-fact",
                    position: 5,
                    kind: "issue.updated",
                    title: "更早的进展记录",
                    author: "system",
                    created_at: now - 1000,
                  },
                ],
              }
            : {
                ...activity,
                event_seq: seq,
                more: true,
                next_before: 11,
                items: Array.from({ length: 20 }, (_, index) => ({
                  id: `recent-fact-${index}`,
                  position: 11 + index,
                  kind: "issue.updated",
                  title: `最近进展 ${index} 更新 ${pagedRevision}`,
                  author: "system",
                  created_at: now,
                })),
              },
        );
      return json(activity);
    }
    if (p.endsWith("/results"))
      return json(
        u.searchParams.has("after")
          ? {
              ...results,
              more: false,
              items: [
                {
                  ...results.items![0]!,
                  title: "较早的交付",
                  artifact: { ...results.items![0]!.artifact, artifact_id: "artifact-older" },
                },
              ],
            }
          : { ...results, more: true, next: "older" },
      );
    if (p.endsWith("/context"))
      return json({ issue_id: current.issue_id, revision: 1, sources: [], facts: [], more: false });
    if (p.endsWith("/deliveries"))
      return json({
        issue_id: current.issue_id,
        items: [
          {
            delivery: {
              delivery_id: "external-one",
              provider: "github",
              repository: "sample/repo",
              number: 42,
            },
            observation: {
              delivery_id: "external-one",
              issue_id: current.issue_id,
              generation: 1,
              state: "merged",
              checked_at: now,
            },
          },
        ],
      });
    if (p.endsWith("/followups")) return json({ items: [], more: false });
    return json({
      ...current,
      attentions: { items: current === issue ? [decision] : [], more: false },
    });
  }
  if (p === "/api/v1/categories")
    return json({
      items: [
        {
          category_id: "issue-manager",
          revision: 1,
          title: "Issue 负责人",
          description: "",
          state: "active",
        },
      ],
      more: false,
    });
  if (p.startsWith("/api/v1/agent-conversations/")) {
    if (conversationFailure)
      return json({ error: { code: "unavailable", message: "Conversation unavailable" } }, 503);
    const id = decodeURIComponent(p.split("/").at(-1)!);
    return json({
      conversation_id: id,
      issue_id: id.includes("child") ? child.issue_id : issue.issue_id,
      title: issue.plan.title,
      kind: "issue",
      status: "idle",
      revision: 1,
      model: "model",
      effort: "high",
      last_message_seq: 0,
      last_message_at: now,
      preview: "",
    });
  }
  if (p === "/api/v1/conversation/transcript")
    return json({ blocks: [], more: false, event_seq: seq });
  if (p === "/api/v1/conversation/queue")
    return json({ items: [], total: 0, event_seq: seq, enabled: true });
  if (p === "/api/v1/conversation") return json({ items: [], more: false });
  if (p === "/api/v1/conversation/settings")
    return json({
      enabled: true,
      available: false,
      revision: 1,
      model: "model",
      effort: "high",
      models: [],
    });
  if (p === "/api/v1/conversation/handoff")
    return json({ enabled: false, pending: true, revision: 1, idle_minutes: 30 });
  if (p.includes("/usage"))
    return json({ available: false, measurement: "unavailable", sampled_at: now, updated_at: now });
  if (p.startsWith("/api/v1/artifacts/"))
    return json({
      ...results.items![0],
      artifact: {
        ...results.items![0]!.artifact,
        ref: {
          ...(results.items![0]!.artifact.ref as object),
          mounts: [
            {
              repo: "sample",
              path: "sample",
              base_sha: "a".repeat(40),
              commit_sha: "b".repeat(40),
              snapshot_sha: "c".repeat(40),
              branch: "topic",
              changed: true,
            },
          ],
        },
      },
    });
  if (p.startsWith("/api/v1/tasks/task_")) {
    const id = p.split("/").at(-1)!;
    const second = id.endsWith("FAW");
    const base = {
      task_id: id,
      title: second ? "关联任务 B" : "执行任务 A",
      project: "sample",
      lifecycle: "queued",
      control_mode: "managed",
      control_revision: 1,
      spec_version: 1,
      rating: "p2",
      block_reason: "none",
      workspace: "example",
      next_implement_generation: 1,
      issue_ids: [issue.issue_id],
      primary_issue_id: issue.issue_id,
      primary_issue_title: issue.plan.title,
      runs: [],
      artifacts: [],
      acceptances: [],
      decisions: [],
    };
    return json({
      ...base,
      relations: second
        ? []
        : [
            {
              kind: "related",
              direction: "outgoing",
              task: { ...base, task_id: "task_01ARZ3NDEKTSV4RRFFQ69G5FAW", title: "关联任务 B" },
            },
          ],
    });
  }
  if (p === "/api/v1/attentions/attention-decision") return json(decision);
  if (p.startsWith("/api/v1/requests/")) {
    const original = progressRequests.get(p.split("/").at(-1)!);
    if (!original || !progressRefusalReady)
      return json({ error: { code: "unavailable", message: "Pending receipt" } }, 503);
    return json({
      request_id: original.request_id,
      operation: original.operation,
      status: "failed",
      error: { code: "stale_revision", message: "Issue changed" },
    });
  }
  if (p === "/api/v1/ops") {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const envelope = JSON.parse(Buffer.concat(chunks).toString()) as Envelope;
    writes.push(envelope);
    if (envelope.operation === "issues.update_progress") {
      if (dropProgressReply) {
        progressRequests.set(envelope.request_id, envelope);
        req.socket.destroy();
        return;
      }
      if (envelope.expected_control_revision !== issue.revision)
        return json({
          operation: {
            status: "failed",
            error: { code: "stale_revision", message: "Issue changed" },
          },
        });
      const payload = envelope.payload as { summary: string; next_step: string };
      issue.revision++;
      issue.progress = { ...issue.progress!, ...payload, issue_revision: issue.revision };
      publish();
      return json({ operation: { status: "succeeded", result: issue.progress } });
    }
    if (envelope.operation === "attentions.respond") {
      decision = { ...decision, status: "resolved", revision: 2 };
      publish();
      return json({ operation: { status: "succeeded", result: decision } });
    }
    return json({
      operation: { status: "succeeded", result: { message_id: "sent-one", seq: 99 } },
    });
  }
  if (p.startsWith("/api/")) return json({ items: [], more: false });
  try {
    const file = resolve(root, p.split("/").at(-1)?.includes(".") ? `.${p}` : "index.html");
    assert(file.startsWith(root));
    res.setHeader(
      "Content-Type",
      p.endsWith(".js") ? "text/javascript" : p.endsWith(".css") ? "text/css" : "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.writeHead(404);
    res.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch();
try {
  for (const width of [1440, 390]) {
    conversationFailure = true;
    const context = await browser.newContext({ viewport: { width, height: 900 } });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
    await page.getByText("负责人对话暂时无法读取", { exact: true }).waitFor();
    assert.equal(await page.getByRole("textbox", { name: "消息", exact: true }).count(), 0);
    assert(await page.getByText(/发给 .*本 Issue 负责人/).isVisible());
    await context.close();
    conversationFailure = false;
    console.log(`PASS ${width}: sendable conversation read failure remains visible`);
  }
  for (const width of [1440, 1280, 1024, 430, 390, 320]) {
    issue.progress!.summary = "发布步骤与回滚文档已完成，灰度验证等待范围确认。";
    issue.state = "active";
    issue.assignment_state = "active";
    issue.conversation!.can_send = true;
    delete issue.conversation!.reason;
    decision = { ...decision, kind: "decision", status: "open" };
    const context = await browser.newContext({
      viewport: { width, height: 1000 },
      isMobile: width < 760,
      hasTouch: width < 760,
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (m) => {
      if (m.type() === "error") errors.push(m.text());
    });
    await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
    const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
    await detail.getByRole("heading", { name: "发布流程整理", exact: true }).waitFor();
    assert.equal(decodeURIComponent(new URL(page.url()).pathname), `/issues/${issue.issue_id}`);
    await detail
      .getByText("发布步骤与回滚文档已完成，灰度验证等待范围确认。", { exact: true })
      .waitFor();
    await detail.getByRole("textbox", { name: "消息", exact: true }).waitFor();
    await page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve, reject) => {
        const request = indexedDB.open("tend:deployment:issue-detail-fixture");
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      const tx = db.transaction("outbox", "readwrite");
      tx.objectStore("outbox").put({
        requestId: "old-sent",
        order: 1,
        envelope: {
          request_id: "old-sent",
          operation: "conversation.submit_message",
          target: { type: "conversation" },
          payload: { conversation_id: "issue:canonical-root", body: "旧的已完成消息" },
        },
        body: "旧的已完成消息",
        createdAt: Date.now() - 3600000,
        updatedAt: Date.now() - 3600000,
        state: "sent",
        messageId: "old-done",
        seq: 1,
      });
      await new Promise<void>((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
      });
      db.close();
    });
    await page.reload();
    await detail.getByRole("textbox", { name: "消息", exact: true }).waitFor();
    await detail
      .getByText("发布步骤与回滚文档已完成，灰度验证等待范围确认。", { exact: true })
      .waitFor();
    assert.equal(await detail.getByText("旧的已完成消息", { exact: true }).count(), 0);

    assert.equal(await page.getByRole("dialog", { name: "Issue 详情" }).count(), 0);
    await detail.getByText(/暂无活动执行/).waitFor();
    const alignedFooter = async () => {
      const edges = await detail.evaluate((root) => {
        const lane = root.querySelector("[data-issue-lane]")!;
        const footer = root.querySelector("[data-issue-footer]")!;
        const contentLeft =
          lane.getBoundingClientRect().left + parseFloat(getComputedStyle(lane).paddingLeft);
        const footerLeft =
          footer.getBoundingClientRect().left + parseFloat(getComputedStyle(footer).paddingLeft);
        const children = Array.from(footer.children).filter(
          (node) => node.getBoundingClientRect().height > 0,
        );
        return {
          contentLeft,
          footerLeft,
          children: children.map((node) => node.getBoundingClientRect().left),
        };
      });
      assert(Math.abs(edges.contentLeft - edges.footerLeft) <= 1, JSON.stringify(edges));
      for (const left of edges.children)
        assert(Math.abs(left - edges.contentLeft) <= 1, JSON.stringify(edges));
    };
    await alignedFooter();

    await detail.getByText(/由 (青竹|负责人 Agent) 更新/).waitFor();
    await detail.getByText(/执行 Agent 实施已交付/).waitFor();
    assert.equal(
      await detail.getByText("native-manager-not-for-the-landing-page", { exact: true }).count(),
      0,
    );
    assert(await detail.getByRole("button", { name: "查看并决定", exact: true }).isVisible());
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: join(output, `detail-parent-${width}.png`) });
    await detail.getByRole("textbox", { name: "消息", exact: true }).fill("父项保留的草稿");
    await detail
      .getByRole("button", { name: /整理发布步骤/ })
      .first()
      .click();
    await detail.getByRole("heading", { name: "整理发布步骤", exact: true }).waitFor();
    assert.equal(await detail.getByRole("textbox", { name: "消息", exact: true }).inputValue(), "");
    await detail.getByRole("textbox", { name: "消息", exact: true }).fill("子项的消息");
    await detail.getByRole("button", { name: "发送", exact: true }).click();
    await page.waitForFunction(() => document.body.textContent?.includes("已送达"));
    const send = writes.findLast((w) => w.operation === "conversation.submit_message")!;
    assert.equal(
      (send.payload as { conversation_id: string }).conversation_id,
      "issue:canonical-child",
    );
    await page.screenshot({ path: join(output, `detail-child-${width}.png`) });
    if (width <= 760)
      await detail.getByRole("button", { name: "返回 Issue 列表或上一项", exact: true }).click();
    else
      await detail
        .getByRole("navigation", { name: "Issue 路径" })
        .getByRole("button", { name: /发布流程整理/ })
        .click();
    await detail.getByRole("heading", { name: "发布流程整理", exact: true }).waitFor();
    assert.equal(
      await detail.getByRole("textbox", { name: "消息", exact: true }).inputValue(),
      "父项保留的草稿",
    );
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    requests.length = 0;
    const activityReloaded = page.waitForResponse((response) =>
      response.url().includes(`/issues/${issue.issue_id}/activity`),
    );
    publish();
    await activityReloaded;
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    assert(
      !requests.some((path) => path.includes(encodeURIComponent("issue:canonical-child"))),
      "inactive child must not reload its native session on ordinary Issue events",
    );
    await detail.getByRole("button", { name: "编辑", exact: true }).click();
    const progress = page.getByRole("dialog", { name: "编辑当前进展", exact: true });
    await progress
      .getByRole("textbox", { name: "当前进展", exact: true })
      .fill("已确认验证范围，准备独立验收。");
    await progress.getByRole("button", { name: "保存进展", exact: true }).click();
    await progress.waitFor({ state: "hidden" });
    await detail.getByText("已确认验证范围，准备独立验收。", { exact: true }).waitFor();
    assert.equal(
      writes.findLast((w) => w.operation === "issues.update_progress")?.target.id,
      issue.issue_id,
    );
    await detail.getByRole("button", { name: "编辑", exact: true }).click();
    await progress.waitFor();
    const baselineRevision = issue.revision;
    await progress.getByRole("textbox", { name: "当前进展", exact: true }).fill("尚未提交的旧结论");
    issue.revision++;
    issue.progress!.summary = "另一次负责人更新已经确认。";
    publish();
    await detail.getByText("另一次负责人更新已经确认。", { exact: true }).waitFor();
    await progress.getByRole("button", { name: "保存进展", exact: true }).click();
    await progress
      .getByText("Issue 已变化，请关闭后重新打开进展编辑；当前输入仍保留。", { exact: true })
      .waitFor();
    assert.equal(
      writes.findLast((w) => w.operation === "issues.update_progress")!.expected_control_revision,
      baselineRevision,
    );
    assert.equal(issue.progress!.summary, "另一次负责人更新已经确认。");
    await progress.getByRole("button", { name: /^关闭/ }).first().click();
    if (width < 1280) {
      await detail.getByRole("button", { name: "详情", exact: true }).click();
      const drawer = page.getByRole("dialog", { name: "Issue 辅助信息", exact: true });
      await drawer.getByRole("heading", { name: "交付物", exact: true }).waitFor();
      await drawer.getByRole("button", { name: /^关闭/ }).first().click();
    }
    await openIssueAux(page, "全部执行交付");
    const deliveries = page.getByRole("dialog", { name: "全部执行交付", exact: true });
    await deliveries.getByText("发布与回滚清单", { exact: true }).waitFor();
    await deliveries.getByRole("button", { name: "下一页交付", exact: true }).click();
    await deliveries.getByText("较早的交付", { exact: true }).waitFor();
    await deliveries.getByRole("button", { name: "上一页交付", exact: true }).click();
    await deliveries.getByText("发布与回滚清单", { exact: true }).waitFor();
    await deliveries.getByRole("button", { name: /^关闭/ }).first().click();
    const side = await issueSidebar(page);
    await side.getByText("待检查", { exact: true }).waitFor();
    assert.equal(
      await side.getByRole("button", { name: /发布与回滚清单/ }).count(),
      0,
      "sidebar does not repeat deliverables",
    );
    await side.getByRole("button", { name: /^查看全部交付 ·/ }).waitFor();
    if (width < 1280) await side.getByRole("button", { name: /^关闭/ }).first().click();
    const summary = detail.getByRole("region", { name: "交付物", exact: true });
    await summary.getByText("外部观察 · 已合并", { exact: true }).waitFor();
    await summary.getByRole("button", { name: /发布与回滚清单/ }).waitFor();
    await detail.locator('[data-artifact="artifact-one"]').waitFor();
    const resultCard = detail.locator('[data-artifact="artifact-one"]');
    await resultCard.getByRole("button", { name: "打开完整交付 →", exact: true }).click();
    for (const letter of ["b", "c"]) {
      const commit = resultCard.getByText(letter.repeat(7), { exact: true });
      await commit.waitFor();
      assert.equal(await commit.getAttribute("title"), letter.repeat(40));
    }
    const taskLink = resultCard.getByRole("button", { name: "查看任务与验收 →", exact: true });
    await taskLink.scrollIntoViewIfNeeded();
    const taskReadingTop = await detail
      .locator("[data-issue-scroll]")
      .evaluate((node) => node.scrollTop);
    await taskLink.click();
    const taskDialog = page.getByRole("region", { name: "任务详情", exact: true });
    await taskDialog.getByText("执行任务 A", { exact: true }).waitFor();
    await taskDialog.getByRole("button", { name: /关联任务 B/ }).click();
    await taskDialog.getByText("关联任务 B", { exact: true }).waitFor();
    await page.goBack();
    assert.equal(new URL(page.url()).pathname, `/issues/${issue.issue_id}`);
    await taskDialog.waitFor({ state: "hidden" });
    assert.equal(new URL(page.url()).pathname.startsWith("/tasks/"), false);
    assert.equal(
      await detail.getByRole("textbox", { name: "消息", exact: true }).inputValue(),
      "父项保留的草稿",
    );
    await page.waitForFunction((expected) => {
      const node = document.querySelector("[data-issue-scroll]");
      return node !== null && Math.abs(node.scrollTop - expected) < 4;
    }, taskReadingTop);
    assert(
      Math.abs(
        (await detail.locator("[data-issue-scroll]").evaluate((node) => node.scrollTop)) -
          taskReadingTop,
      ) < 4,
    );
    await page.waitForFunction(() => document.activeElement?.textContent === "查看任务与验收 →");
    assert(await taskLink.evaluate((element) => element === document.activeElement));

    const noticeScroller = detail.locator("[data-issue-scroll]");
    await noticeScroller.evaluate((node) => {
      node.scrollTop = 250;
    });
    await page.waitForTimeout(100);
    issue.revision++;
    issue.progress!.issue_revision = issue.revision;
    publish();
    await page.waitForTimeout(150);
    assert.equal(
      await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
      0,
      "details-only refresh produced a notice with independent activity/block counters",
    );

    // Long prose must not clip the trace, and tool expansion must pause bottom following.
    const traceBlock = activity.items![0]!.block!;
    const traceReply = traceBlock.items![0]!;
    const savedBody = traceReply.body;
    traceReply.body = "长回复正文仅自身被截断。\n\n".repeat(80);
    traceReply.assistant_revision!++;
    const toolID = `tool-disclosure-${width}`;
    traceBlock.items!.push({
      message_id: toolID,
      seq: 3,
      kind: "message",
      author: "main_agent",
      reply_to: "owner-one",
      created_at: new Date(now).toISOString(),
      tool: {
        name: "files_read",
        status: "running",
        turn_id: "turn-disclosure",
        updated_at: new Date(now).toISOString(),
        input: "示例文件",
        output: "工具输出不受回复截断影响。\n".repeat(80),
      },
    });
    publish();
    const traceTurn = detail.locator('[data-scroll-id="conversation-one"]');
    const traceToggle = traceTurn.locator("details[data-trace] > summary");
    await traceToggle.waitFor();
    assert.equal(
      await traceToggle.locator('xpath=ancestor::*[contains(@class,"replyClamp")]').count(),
      0,
      "long prose clamp contains the trace disclosure",
    );
    assert.equal(
      await traceTurn
        .locator('[role="status"]')
        .first()
        .locator('xpath=ancestor::*[contains(@class,"replyClamp")]')
        .count(),
      0,
      "long prose clamp contains live status",
    );
    await traceToggle.evaluate((node) => (node as HTMLElement).click());
    const toolToggle = traceTurn.locator(`[data-scroll-id="${toolID}"] button[aria-expanded]`);
    await toolToggle.waitFor();
    await toolToggle.scrollIntoViewIfNeeded();
    const issueScroll = detail.locator("[data-issue-scroll]");
    await issueScroll.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await page.waitForTimeout(100);
    const controlY = await toolToggle.evaluate((node) => node.getBoundingClientRect().top);
    await toolToggle.evaluate((node) => (node as HTMLElement).click());
    await page.waitForTimeout(100);
    assert(
      Math.abs(
        (await toolToggle.evaluate((node) => node.getBoundingClientRect().top)) - controlY,
      ) <= 2,
      "Issue tool expansion moved its control",
    );
    const toolOutput = traceTurn.getByText("工具输出不受回复截断影响。\n".repeat(80), {
      exact: true,
    });
    assert.equal(
      await toolOutput.locator('xpath=ancestor::*[contains(@class,"replyClamp")]').count(),
      0,
      "long prose clamp clips tool output",
    );
    assert(
      await toolOutput.evaluate((node) => node.getBoundingClientRect().height > 100),
      "fixture tool output must extend beyond the reply preview",
    );
    const readingTop = await issueScroll.evaluate((node) => node.scrollTop);
    traceReply.body += "新的流式正文，不应抢走工具阅读位置。\n\n".repeat(40);
    traceReply.assistant_revision!++;
    publish();
    await traceTurn
      .getByText(/新的流式正文/)
      .first()
      .waitFor({ state: "attached" });
    await page.waitForTimeout(150);
    assert(
      Math.abs((await issueScroll.evaluate((node) => node.scrollTop)) - readingTop) <= 2,
      "streamed Issue update resumed following over expanded tool output",
    );
    assert(
      Math.abs(
        (await toolToggle.evaluate((node) => node.getBoundingClientRect().top)) - controlY,
      ) <= 2,
      "streamed Issue update moved the tool reading control",
    );
    await toolToggle.evaluate((node) => (node as HTMLElement).click());
    await page.waitForTimeout(100);
    assert(
      Math.abs(
        (await toolToggle.evaluate((node) => node.getBoundingClientRect().top)) - controlY,
      ) <= 2,
      "Issue tool collapse moved its control",
    );
    traceBlock.items!.pop();
    traceReply.body = savedBody!;
    traceReply.assistant_revision!++;
    publish();
    await traceToggle.waitFor({ state: "detached" });

    // A stable activity ID can grow while streaming without changing ledger order.
    const reply = activity.items![0]!.block!.items![0]!;
    const originalBody = reply.body!;
    reply.body = "流式回复的详细进展。\n\n".repeat(50);
    reply.assistant_revision!++;
    publish();
    const turn = detail.locator('[data-scroll-id="conversation-one"]');
    await turn.getByRole("button", { name: "展开全文", exact: true }).click();
    const scroller = detail.locator("[data-issue-scroll]");
    await scroller.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await page.waitForFunction(() => {
      const n = document.querySelector("[data-issue-scroll]")!;
      return n.scrollHeight - n.scrollTop - n.clientHeight < 10;
    });
    const previousHeight = await scroller.evaluate((node) => node.scrollHeight);
    reply.body += "流式回复继续更新，保留当前阅读位置。\n\n".repeat(100);
    reply.assistant_revision!++;
    publish();
    await page.waitForFunction((previousHeight) => {
      const n = document.querySelector("[data-issue-scroll]")!;
      return n.scrollHeight > previousHeight && n.scrollHeight - n.scrollTop - n.clientHeight < 20;
    }, previousHeight);
    // Resize only the scrollport: existing latest intent must stay at bottom.
    await page.setViewportSize({ width: width > 760 ? 390 : 1440, height: 844 });
    await page.waitForFunction(() => {
      const node = document.querySelector("[data-issue-scroll]")!;
      return node.scrollHeight - node.scrollTop - node.clientHeight < 2;
    });
    await page.setViewportSize({ width, height: 1000 });
    await page.waitForFunction(() => {
      const node = document.querySelector("[data-issue-scroll]")!;
      return node.scrollHeight - node.scrollTop - node.clientHeight < 2;
    });

    // Paused intent survives viewport growth even when the new remaining
    // distance is inside the automatic follow threshold.
    await page.setViewportSize({ width, height: 800 });
    await page.waitForTimeout(150);
    await scroller.evaluate((node) => {
      node.scrollTop = node.scrollHeight - node.clientHeight - 220;
    });
    await page.waitForTimeout(150);
    const pausedTop = await scroller.evaluate((node) => node.scrollTop);
    await page.setViewportSize({ width, height: 950 });
    await page.waitForTimeout(200);
    assert(
      Math.abs((await scroller.evaluate((node) => node.scrollTop)) - pausedTop) <= 2,
      "viewport growth resumed paused Issue following",
    );
    const pausedHeight = await scroller.evaluate((node) => node.scrollHeight);
    reply.body += "窗口增高后仍保持暂停阅读。\n\n".repeat(30);
    reply.assistant_revision!++;
    publish();
    await page.waitForFunction((height) => {
      const node = document.querySelector("[data-issue-scroll]")!;
      return node.scrollHeight > height;
    }, pausedHeight);
    await page.waitForTimeout(150);
    assert(
      (await scroller.evaluate((node) => node.scrollHeight - node.scrollTop - node.clientHeight)) >=
        50,
      "content growth after resize resumed paused Issue following",
    );
    await page.setViewportSize({ width, height: 1000 });
    await page.waitForTimeout(100);
    const laterEntry = detail.locator('[data-scroll-id="result:artifact-one"]');
    await laterEntry.scrollIntoViewIfNeeded();
    await scroller.evaluate((node) => {
      node.scrollTop -= 120;
    });
    await page.waitForTimeout(100);
    const entryY = await laterEntry.evaluate((node) => node.getBoundingClientRect().top);
    reply.body += "在较早回复继续生成内容。\n\n".repeat(60);
    reply.assistant_revision!++;
    publish();
    await turn
      .getByText(/在较早回复继续生成内容/)
      .first()
      .waitFor({ state: "attached" });
    await page.waitForTimeout(100);
    assert(
      Math.abs((await laterEntry.evaluate((node) => node.getBoundingClientRect().top)) - entryY) <=
        2,
      "streamed reply above paused Issue reading displaced the later entry",
    );
    const originalProgress = issue.progress!.summary;
    issue.progress!.summary = "单独到达的当前进展更新。\n\n".repeat(60);
    issue.revision++;
    publish();
    await detail
      .getByText(/单独到达的当前进展更新/)
      .first()
      .waitFor({ state: "attached" });
    await page.waitForTimeout(100);
    assert(
      Math.abs((await laterEntry.evaluate((node) => node.getBoundingClientRect().top)) - entryY) <=
        2,
      "independent Issue progress detail update displaced paused activity reading",
    );
    issue.progress!.summary = originalProgress;
    issue.revision++;
    publish();
    await page.waitForTimeout(100);
    reply.body = originalBody;
    reply.assistant_revision!++;
    publish();
    await detail.getByRole("button", { name: "查看并决定", exact: true }).click();
    await page
      .getByRole("dialog", { name: "Issue 待处理事项", exact: true })
      .getByRole("button", { name: /^关闭/ })
      .first()
      .click();
    const originalActivity = activity.items!;
    const sharedReply = {
      ...activity.items![0]!.block!.items![0]!,
      message_id: "shared-reply",
      reply_to: "owner-supplement",
      body: "合并处理后的唯一回复。",
    };
    const firstBlock = activity.items![0]!.block!;
    activity.items = [
      { ...activity.items![0]!, block: { ...firstBlock, items: [sharedReply] } },
      originalActivity[1]!,
      {
        ...activity.items![0]!,
        id: "supplement-block",
        position: 1003,
        block: {
          ...firstBlock,
          position: 3,
          trigger: {
            ...firstBlock.trigger,
            message_id: "owner-supplement",
            seq: 3,
            body: "补充：保留验收原件。",
          },
          items: [sharedReply],
        },
      },
    ];
    publish();
    await detail.getByText("补充：保留验收原件。", { exact: true }).waitFor();
    assert.equal(await detail.getByText("合并处理后的唯一回复。", { exact: true }).count(), 1);
    const ordered = await detail.locator('[data-scroll-id="supplement-block"]').innerText();
    assert(ordered.indexOf("补充：保留验收原件。") < ordered.indexOf("合并处理后的唯一回复。"));
    activity.items = originalActivity;
    publish();
    issue.assignment_state = "handoff";
    issue.conversation = {
      conversation_id: "issue:canonical-root",
      can_send: false,
      reason: "handoff",
    };
    publish();
    await detail.getByText("正在交接负责人，当前执行事实仍予保留。", { exact: true }).waitFor();
    await detail.getByRole("textbox", { name: "消息", exact: true }).fill("交接时仍可保存的草稿");
    assert(await detail.getByRole("button", { name: "发送", exact: true }).isDisabled());
    issue.assignment_state = "active";
    issue.assignee = { kind: "owner", ref: "owner" };
    issue.conversation!.reason = "owner";
    publish();
    await detail
      .getByText("这个 Issue 由你负责，没有负责人 Agent 对话。", { exact: true })
      .waitFor();
    await alignedFooter();
    const ownerCopy = await detail
      .getByText("这个 Issue 由你负责，没有负责人 Agent 对话。", { exact: true })
      .boundingBox();
    assert(
      ownerCopy && ownerCopy.width >= Math.min(width - 80, 600),
      "owner explanation occupies a readable full row",
    );
    assert.equal(await detail.getByRole("textbox", { name: "消息", exact: true }).count(), 0);
    assert.equal(await detail.getByText(/发给 .*本 Issue 负责人/).count(), 0);
    const managerSent = page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/api/v1/ops",
    );
    await detail.getByRole("button", { name: "交给 Issue 负责人 Agent", exact: true }).click();
    await managerSent;
    assert.equal((writes.at(-1)!.payload as { category_id: string }).category_id, "issue-manager");
    assert.equal(writes.at(-1)!.operation, "issues.request_owner");
    issue.conversation = {
      conversation_id: "issue:canonical-root",
      can_send: true,
      reason: "manager_pending",
    };
    issue.manager = { task_id: "pending-manager", category_id: "issue-manager", state: "queued" };
    publish();
    await detail
      .getByText("等待执行主机领取负责人；消息会在领取后送达。", { exact: true })
      .waitFor();
    assert.equal(
      await detail.getByText(/主会话正在交接|托管主 Agent 暂不可用|托管主 Agent 未启用/).count(),
      0,
    );
    await detail.getByText("发给 Issue 负责人（待领取）", { exact: true }).waitFor();
    assert(!(await detail.getByRole("button", { name: "发送", exact: true }).isDisabled()));
    await page.screenshot({ path: join(output, `pending-manager-${width}.png`) });
    issue.conversation = {
      conversation_id: "issue:canonical-root",
      can_send: false,
      reason: "owner",
    };
    publish();
    await detail.getByRole("button", { name: "选择其他负责人 →", exact: true }).click();
    await detail.getByRole("dialog", { name: "交接负责人", exact: true }).waitFor();
    await page.keyboard.press("Escape");
    issue.state = "closed";
    issue.conversation!.reason = "ended";
    publish();
    await detail.getByRole("button", { name: "重新打开 Issue →", exact: true }).waitFor();
    await detail.getByText("Issue 已结束，对话记录仍可阅读。", { exact: true }).waitFor();
    issue.state = "paused";
    issue.assignee = { kind: "agent", ref: "hosted/qingzhu" };
    issue.conversation = { conversation_id: "issue:canonical-root", can_send: true };
    decision.kind = "information";
    const progressValue = issue.progress!;
    delete issue.progress;
    publish();
    await detail.getByText("负责人尚未更新进展", { exact: true }).waitFor();
    assert.equal(await detail.getByRole("button", { name: "查看并决定", exact: true }).count(), 0);
    issue.progress = progressValue;
    publish();
    await detail.getByRole("textbox", { name: "消息", exact: true }).fill("模拟本机队列保存失败");
    await page.evaluate(() => {
      const original = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function (...args: Parameters<IDBObjectStore["put"]>) {
        if (this.name === "outbox") throw new DOMException("Quota exceeded", "QuotaExceededError");
        return original.apply(this, args);
      };
    });
    await detail.getByRole("button", { name: "发送", exact: true }).click();
    await detail
      .getByText("消息未能保存到发送队列，文字和附件已恢复，请重试", { exact: true })
      .waitFor();
    assert.equal(
      await detail.getByRole("textbox", { name: "消息", exact: true }).inputValue(),
      "模拟本机队列保存失败",
    );
    await page.evaluate(() => {
      document.body.dataset.dark = "";
    });
    await page.screenshot({ path: join(output, `detail-dark-${width}.png`) });
    assert.deepEqual(errors, []);
    await context.close();
    console.log(
      `PASS ${width}: full-page, exact recipient, parent/child drafts, progress, decision, sidebar, handoff, dark theme`,
    );
  }
  const savedSummary = issue.progress!.summary;
  for (const width of [1440, 390]) {
    const text = "第一行\n第二行\n第三行\n第四行\n第五行";
    assert(text.length < 100);
    issue.progress!.summary = text;
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
    const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
    const summary = detail.getByText(text, { exact: true });
    const expand = detail.getByRole("button", { name: "展开全文", exact: true });
    await expand.waitFor();
    assert(await summary.evaluate((node) => node.scrollHeight > node.clientHeight + 1));
    await expand.click();
    const collapse = detail.getByRole("button", { name: "收起", exact: true });
    await collapse.waitFor();
    assert(await summary.evaluate((node) => node.scrollHeight <= node.clientHeight + 1));
    await collapse.click();
    await expand.waitFor();
    assert(await summary.evaluate((node) => node.scrollHeight > node.clientHeight + 1));
    await context.close();
    console.log(`PASS ${width}: short multiline progress can expand and collapse`);
  }
  {
    const text = "调查结论已确认，下一步等待负责人安排。".repeat(4);
    assert(text.length < 100);
    issue.progress!.summary = text;
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
    const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
    const summary = detail.getByText(text, { exact: true });
    await summary.waitFor();
    const expand = detail.getByRole("button", { name: "展开全文", exact: true });
    assert(await summary.evaluate((node) => node.scrollHeight <= node.clientHeight + 1));
    assert.equal(await expand.count(), 0);
    await page.setViewportSize({ width: 320, height: 1000 });
    await expand.waitFor();
    assert(await summary.evaluate((node) => node.scrollHeight > node.clientHeight + 1));
    await expand.click();
    const collapse = detail.getByRole("button", { name: "收起", exact: true });
    await collapse.waitFor();
    assert(await summary.evaluate((node) => node.scrollHeight <= node.clientHeight + 1));
    await collapse.click();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await expand.waitFor({ state: "hidden" });
    assert(await summary.evaluate((node) => node.scrollHeight <= node.clientHeight + 1));
    await context.close();
    console.log("PASS 1440/320: progress expansion follows actual wrapping after resize");
  }
  issue.progress!.summary = savedSummary;
  issue.assignee = { kind: "owner", ref: "owner" };
  issue.state = "active";
  issue.conversation = { can_send: false, reason: "owner" };
  const savedActivity = structuredClone(activity);
  activity.items = activity.items!.filter((entry) => !entry.block).slice(0, 2);
  activity.more = false;
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
    await page.getByText("这个 Issue 由你负责，没有负责人 Agent 对话。", { exact: true }).waitFor();
    await page.waitForTimeout(500);
    assert.equal(
      await page.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
      0,
    );
    await page.setViewportSize({ width, height: 1100 });
    await page.waitForTimeout(200);
    assert.equal(await page.getByText("有新进展 · 到最新", { exact: false }).count(), 0);
    await context.close();
  }
  Object.assign(activity, savedActivity);
  activity.items![0]!.block!.items![0]!.body = "需要从顶部开始阅读的历史。\n\n".repeat(200);
  activityDelay = 300;
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
    await page.getByText("这个 Issue 由你负责，没有负责人 Agent 对话。", { exact: true }).waitFor();
    const footerEdges = await page.evaluate(() => {
      const lane = document.querySelector("[data-issue-lane]")!;
      const reason = document.querySelector("[data-issue-footer] p")!;
      return [
        lane.getBoundingClientRect().left + parseFloat(getComputedStyle(lane).paddingLeft),
        reason.getBoundingClientRect().left,
      ];
    });
    assert(Math.abs(footerEdges[0]! - footerEdges[1]!) <= 1, JSON.stringify(footerEdges));
    await page.getByRole("button", { name: "展开全文", exact: true }).waitFor();
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    assert.equal(await page.locator("[data-issue-scroll]").evaluate((node) => node.scrollTop), 0);
    await context.close();
    console.log(`PASS ${width}: delayed read-only history opens at the Issue summary`);
  }
  pagedChildren = true;
  const context = await browser.newContext({ viewport: { width: 390, height: 1000 } });
  const page = await context.newPage();
  await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
  const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
  await detail.getByRole("button", { name: "查看全部子 Issue", exact: true }).click();
  await detail.getByRole("button", { name: "下一页子 Issue", exact: true }).click();
  await detail.getByRole("button", { name: /整理发布步骤/ }).click();
  await detail.getByRole("heading", { name: child.plan.title, exact: true }).waitFor();
  await detail.getByRole("button", { name: "返回 Issue 列表或上一项", exact: true }).click();
  await detail.getByRole("button", { name: "上一页子 Issue", exact: true }).waitFor();
  assert.equal(await detail.getByRole("button", { name: /其它子项 0/ }).count(), 0);
  await detail.getByRole("button", { name: /整理发布步骤/ }).waitFor();
  await context.close();
  console.log("PASS 390: returning from a child preserves the parent's selected child page");
  dropProgressReply = true;
  const recoveryContext = await browser.newContext({ viewport: { width: 390, height: 1000 } });
  const recovery = await recoveryContext.newPage();
  await recovery.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
  const recoveryDetail = recovery.getByRole("region", { name: "Issue 详情", exact: true });
  await recoveryDetail.getByRole("button", { name: "编辑", exact: true }).click();
  const editor = recovery.getByRole("dialog", { name: "编辑当前进展", exact: true });
  await editor.getByRole("textbox", { name: "当前进展", exact: true }).fill("原请求中的进展");
  await editor.getByRole("textbox", { name: "下一步", exact: true }).fill("原请求中的下一步");
  const beforeWrites = writes.length;
  await editor.getByRole("button", { name: "保存进展", exact: true }).click();
  await editor.getByRole("button", { name: "核对原更新请求", exact: true }).waitFor();
  await editor.getByRole("button", { name: /^关闭/ }).first().click();
  issue.revision++;
  issue.progress!.summary = "其他更新已被保存";
  issue.progress!.next_step = "其他人的下一步";
  publish();
  await recoveryDetail.getByText("其他更新已被保存", { exact: true }).waitFor();
  await recoveryDetail.getByRole("button", { name: "编辑", exact: true }).click();
  await editor.getByRole("button", { name: "核对原更新请求", exact: true }).waitFor();
  assert.equal(
    await editor.getByRole("textbox", { name: "当前进展", exact: true }).inputValue(),
    "原请求中的进展",
  );
  progressRefusalReady = true;
  await editor.getByRole("button", { name: "核对原更新请求", exact: true }).click();
  await editor
    .getByText("Issue 已变化，请关闭后重新打开进展编辑；当前输入仍保留。", { exact: true })
    .waitFor();
  assert.equal(
    await editor.getByRole("textbox", { name: "当前进展", exact: true }).inputValue(),
    "原请求中的进展",
  );
  assert.equal(
    await editor.getByRole("textbox", { name: "下一步", exact: true }).inputValue(),
    "原请求中的下一步",
  );
  const recoveredWrites = writes
    .slice(beforeWrites)
    .filter((envelope) => envelope.operation === "issues.update_progress");
  assert(recoveredWrites.length > 0);
  assert.equal(
    new Set(recoveredWrites.map((envelope) => envelope.request_id)).size,
    1,
    "receipt recovery preserves the original update identity",
  );
  for (const envelope of recoveredWrites)
    assert.deepEqual(
      envelope,
      recoveredWrites[0],
      "transport retries preserve the complete original envelope",
    );
  assert.equal(progressRequests.size, 1);

  assert.equal(issue.progress!.summary, "其他更新已被保存");
  await recoveryContext.close();
  console.log("PASS 390: restored progress text and next step survive a rejected original receipt");
  const isolationContext = await browser.newContext({ viewport: { width: 390, height: 1000 } });
  const isolation = await isolationContext.newPage();
  await isolation.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
  const isolatedIssue = isolation.getByRole("region", { name: "Issue 详情", exact: true });
  await isolatedIssue.getByRole("heading", { name: issue.plan.title, exact: true }).waitFor();
  await isolation.evaluate(async () => {
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const r = indexedDB.open("tend:deployment:issue-detail-fixture");
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
    });
    const tx = db.transaction("outbox", "readwrite");
    tx.objectStore("outbox").put({
      requestId: "main-failed",
      order: 1,
      envelope: {
        request_id: "main-failed",
        operation: "conversation.submit_message",
        target: { type: "conversation" },
        payload: { conversation_id: "main", body: "只属于主对话的待发内容" },
      },
      body: "只属于主对话的待发内容",
      createdAt: Date.now(),
      updatedAt: Date.now(),
      state: "failed",
      error: "等待在主对话中处理",
    });
    await new Promise<void>((resolve, reject) => {
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
    db.close();
  });
  await isolation.reload();
  await isolatedIssue
    .getByText("这个 Issue 由你负责，没有负责人 Agent 对话。", { exact: true })
    .waitFor();
  assert.equal(await isolatedIssue.getByText("只属于主对话的待发内容", { exact: true }).count(), 0);
  await isolatedIssue.getByRole("button", { name: "返回 Issue 列表或上一项", exact: true }).click();
  await navigate(isolation, "/diagnostics/main");
  await isolation.getByText("只属于主对话的待发内容", { exact: true }).waitFor();
  assert(
    !writes.some(
      (envelope) =>
        envelope.operation === "conversation.submit_message" &&
        (envelope.payload as { body?: string }).body === "只属于主对话的待发内容",
    ),
  );
  pagedActivity = true;
  await isolation.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
  const quietContext = await browser.newContext({ viewport: { width: 390, height: 1000 } });
  const quietPage = await quietContext.newPage();
  await quietPage.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
  const quietIssue = quietPage.getByRole("region", { name: "Issue 详情", exact: true });
  await quietIssue.locator('[data-scroll-id="recent-fact-19"]').waitFor({ state: "attached" });
  const previousPrompt = quietIssue.getByRole("button", {
    name: "有新进展 · 到最新 ↓",
    exact: true,
  });
  if (await previousPrompt.count()) await previousPrompt.click();
  await quietIssue.getByRole("button", { name: "查看更早记录", exact: true }).click();
  await quietIssue.locator('[data-scroll-id="earlier-fact"]').waitFor();
  assert.equal(
    await quietIssue.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
    0,
    "quiet earlier install produced a progress notice with independent counters",
  );
  await quietContext.close();
  let releaseEarlier = () => {};
  holdEarlier = new Promise<void>((resolve) => {
    releaseEarlier = resolve;
  });
  let notifyEarlier = () => {};
  const startedEarlier = new Promise<void>((resolve) => {
    notifyEarlier = resolve;
  });
  earlierStarted = notifyEarlier;
  await isolatedIssue.getByRole("button", { name: "查看更早记录", exact: true }).click();
  await startedEarlier;
  pagedRevision++;
  publish();
  await isolatedIssue.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).waitFor();
  releaseEarlier();
  holdEarlier = null;
  const earlierFact = isolatedIssue.locator('[data-scroll-id="earlier-fact"]');
  await earlierFact.waitFor();
  assert((await earlierFact.innerText()).includes("更早的进展记录"));
  assert.equal(
    await isolatedIssue.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
    1,
    "earlier pagination cleared a real update arriving during the request",
  );
  await isolatedIssue.locator("[data-issue-scroll]").evaluate((node) => {
    node.scrollTop = 200;
  });
  await isolation.waitForTimeout(100);
  pagedRevision++;
  publish();
  await isolatedIssue.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).waitFor();

  await isolationContext.close();
  console.log(
    "PASS 390: missing binding never leaks main's outbox; one click reads the earlier activity page",
  );
  pagedActivity = false;
  activityDelay = 0;
  for (const width of [1440, 390]) {
    const base = activity.items![0]!.block!;
    const originalMore = base.more;
    const baseBody = base.items![0]!.body;
    base.items![0]!.body = "读取前已展开的长回复。\n\n".repeat(80);
    base.more = true;
    const reply = {
      ...base.items![0]!,
      message_id: "continuation-reply",
      seq: 3,
      body: "续页流式回复。\n\n".repeat(80),
      assistant_state: "streaming" as const,
    };
    continuationBlock = { ...base, items: [reply], more: false };
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    try {
      await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      await detail.getByRole("button", { name: "展开全文", exact: true }).first().click();
      const load = detail.getByRole("button", { name: "继续读取本轮消息", exact: true });
      await load.scrollIntoViewIfNeeded();
      await page.waitForTimeout(100);
      const boundaryY = await load.evaluate((node) => node.getBoundingClientRect().top);
      await load.click();
      await detail
        .getByText(/续页流式回复/)
        .first()
        .waitFor({ state: "attached" });
      const firstFetched = detail.locator('[data-scroll-id="continuation-reply"]');
      await page.waitForTimeout(100);
      assert(
        Math.abs(
          (await firstFetched.evaluate((node) => node.getBoundingClientRect().top)) - boundaryY,
        ) <= 2,
        "explicit continuation read skipped the fetched page boundary",
      );
      assert.equal(
        await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
        0,
        "explicit continuation read showed a misleading new-progress prompt",
      );
      const scroller = detail.locator("[data-issue-scroll]");
      await scroller.evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await page.waitForTimeout(100);
      const height = await scroller.evaluate((node) => node.scrollHeight);
      reply.body += "续页新增正文，基础活动行保持不变。\n\n".repeat(60);
      publish();
      await detail
        .getByText(/续页新增正文/)
        .first()
        .waitFor({ state: "attached" });
      await page.waitForFunction((height) => {
        const node = document.querySelector("[data-issue-scroll]")!;
        return (
          node.scrollHeight > height && node.scrollHeight - node.scrollTop - node.clientHeight < 2
        );
      }, height);
      const later = detail.locator('[data-scroll-id="result:artifact-one"]');
      await later.scrollIntoViewIfNeeded();
      await scroller.evaluate((node) => {
        node.scrollTop -= 120;
      });
      await page.waitForTimeout(100);
      const y = await later.evaluate((node) => node.getBoundingClientRect().top);
      reply.body += "续页再次增长，保持暂停阅读。\n\n".repeat(60);
      publish();
      await detail
        .getByText(/续页再次增长/)
        .first()
        .waitFor({ state: "attached" });
      await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).waitFor();
      assert(
        Math.abs((await later.evaluate((node) => node.getBoundingClientRect().top)) - y) <= 2,
        "continuation-only growth displaced paused reading",
      );
      console.log(
        `PASS ${width}: continuation-only streaming follows latest and preserves paused anchor with prompt`,
      );
    } finally {
      await context.close();
      base.more = originalMore;
      base.items![0]!.body = baseBody!;
      continuationBlock = null;
    }
  }

  for (const width of [1440, 390]) {
    for (const scenario of ["failure", "scroll", "group", "progress", "send"] as const) {
      const base = activity.items![0]!.block!;
      const original = { items: base.items, more: base.more };
      const older = { ...base.items![0]!, body: "基础长回复内容。\n\n".repeat(100), seq: 2 };
      const tools = Array.from({ length: scenario === "group" ? 49 : 0 }, (_, index) => ({
        ...older,
        message_id: `existing-tool-${index}`,
        seq: index + 3,
        body: "",
        tool: {
          name: "files_read",
          status: "completed" as const,
          turn_id: "group-turn",
          updated_at: older.created_at,
          input: "{}",
          output: "已完成",
        },
      }));
      base.items = [older, ...tools];
      base.more = true;
      const newTool = { ...tools[0]!, message_id: "new-group-tool", seq: 52 };
      const reply = {
        ...older,
        message_id: `scenario-reply-${scenario}`,
        seq: 53,
        body: "新续页正文。\n\n".repeat(100),
      };
      continuationBlock = {
        ...base,
        items: scenario === "group" ? [newTool, reply] : [reply],
        more: false,
      };
      continuationFailure = scenario === "failure";
      continuationDelay = ["scroll", "progress"].includes(scenario) ? 700 : 0;
      const context = await browser.newContext({
        viewport: { width, height: 844 },
        serviceWorkers: "block",
      });
      const page = await context.newPage();
      let release = () => {};
      let arrived = Promise.resolve();
      if (scenario === "send") {
        issue.conversation = { conversation_id: "issue:canonical-root", can_send: true };
        let notify = () => {};
        arrived = new Promise<void>((resolve) => {
          notify = resolve;
        });
        const held = new Promise<void>((resolve) => {
          release = resolve;
        });
        await page.route("**/conversation/blocks/owner-one?projection=issues", async (route) => {
          notify();
          await held;
          await route.continue();
        });
      }
      try {
        await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
        const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
        await detail.getByRole("button", { name: "展开全文", exact: true }).first().click();
        if (scenario === "group") await detail.locator("details[data-trace] > summary").click();
        const load = detail.getByRole("button", { name: "继续读取本轮消息", exact: true });
        await load.scrollIntoViewIfNeeded();
        await page.waitForTimeout(100);
        const y = await load.evaluate((node) => node.getBoundingClientRect().top);
        await load.click();
        const scroller = detail.locator("[data-issue-scroll]");
        const later = detail.locator('[data-scroll-id="result:artifact-one"]');
        if (scenario === "failure") {
          await detail.getByText("本轮后续记录读取失败", { exact: false }).waitFor();
          await later.scrollIntoViewIfNeeded();
          await scroller.evaluate((node) => {
            node.scrollTop -= 100;
          });
          await page.waitForTimeout(100);
          const saved = await later.evaluate((node) => node.getBoundingClientRect().top);
          older.body += "失败后基础消息继续生成。\n\n".repeat(80);
          older.assistant_revision!++;
          publish();
          await detail
            .getByText(/失败后基础消息继续生成/)
            .first()
            .waitFor({ state: "attached" });
          await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).waitFor();
          assert(
            Math.abs((await later.evaluate((node) => node.getBoundingClientRect().top)) - saved) <=
              2,
            "failed pagination suppressed unrelated paused-anchor updates",
          );
        } else if (scenario === "scroll") {
          await scroller.evaluate((node) => {
            node.scrollTop -= 500;
          });
          await page.waitForTimeout(100);
          const saved = await scroller.evaluate((node) => node.scrollTop);
          await detail
            .getByText(/新续页正文/)
            .first()
            .waitFor({ state: "attached" });
          await page.waitForTimeout(100);
          assert(
            Math.abs((await scroller.evaluate((node) => node.scrollTop)) - saved) <= 2,
            "pagination undid scrolling performed during the request",
          );
        } else if (scenario === "send") {
          await arrived;
          const text = `续页等待期间发送的新消息 ${width}`;
          await detail.getByRole("textbox", { name: "消息", exact: true }).fill(text);
          await detail.getByRole("button", { name: "发送", exact: true }).click();
          await scroller.getByText(text, { exact: true }).waitFor();
          release();
          await detail
            .getByText(/新续页正文/)
            .first()
            .waitFor({ state: "attached" });
          await page.waitForTimeout(100);
          assert(
            await scroller.evaluate(
              (node) => node.scrollHeight - node.scrollTop - node.clientHeight < 2,
            ),
            "continuation response displaced latest after sending",
          );
          const sent = await scroller.getByText(text, { exact: true }).boundingBox();
          const viewport = await scroller.boundingBox();
          assert(
            sent &&
              viewport &&
              sent.y >= viewport.y &&
              sent.y + sent.height <= viewport.y + viewport.height,
            "newly sent message no longer visible at latest",
          );
          assert.equal(
            await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
            0,
          );
        } else if (scenario === "progress") {
          const progress = issue.progress!.summary;
          issue.progress!.summary = "等待续页时上方进展更新。\n\n".repeat(60);
          issue.revision++;
          publish();
          await detail
            .getByText(/等待续页时上方进展更新/)
            .first()
            .waitFor({ state: "attached" });
          const first = detail.locator('[data-scroll-id="scenario-reply-progress"]');
          await first.waitFor();
          await page.waitForTimeout(100);
          assert(
            Math.abs((await first.evaluate((node) => node.getBoundingClientRect().top)) - y) <= 2,
            "programmatic progress anchor restoration cancelled pending continuation boundary",
          );
          assert.equal(
            await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
            0,
          );
          issue.progress!.summary = progress;
          issue.revision++;
        } else {
          const group = detail.getByRole("button", { name: "使用了 50 个工具", exact: true });
          await group.waitFor();
          await page.waitForTimeout(100);
          assert(
            Math.abs((await group.evaluate((node) => node.getBoundingClientRect().top)) - y) <= 2,
            "pagination anchored a hidden grouped member instead of its visible disclosure",
          );
          assert.equal(
            await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
            0,
          );
        }
        console.log(`PASS continuation ${scenario} ${width}`);
      } finally {
        await context.close();
        release();
        base.items = original.items;
        base.more = original.more;
        continuationBlock = null;
        continuationFailure = false;
        continuationDelay = 0;
      }
    }
  }

  for (const width of [1440, 390]) {
    issue.conversation = { conversation_id: "issue:canonical-root", can_send: true };
    const base = activity.items![0]!.block!;
    const originalMore = base.more;
    base.more = true;
    thirdPage = true;
    continuationBlock = {
      ...base,
      items: [{ ...base.items![0]!, message_id: "second-page-reply", seq: 52, body: "第二页内容" }],
      more: true,
      next_seq: 53,
    };
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    try {
      await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      const more = detail.getByRole("button", { name: "继续读取本轮消息", exact: true });
      await more.click();
      await detail.getByText("第二页内容", { exact: true }).waitFor();
      await more.click();
      await detail.getByText("第三页保留消息", { exact: true }).waitFor();
      const taskLink = detail
        .getByRole("button", { name: "查看任务与验收 →", exact: true })
        .first();
      await taskLink.scrollIntoViewIfNeeded();
      const top = await detail.locator("[data-issue-scroll]").evaluate((node) => node.scrollTop);
      await taskLink.click();
      await page.getByRole("region", { name: "任务详情", exact: true }).waitFor();
      await page.goBack();
      await detail.getByText("第三页保留消息", { exact: true }).waitFor({ state: "attached" });
      await page.waitForTimeout(150);
      assert(
        Math.abs(
          (await detail.locator("[data-issue-scroll]").evaluate((node) => node.scrollTop)) - top,
        ) <= 2,
        "returning from task lost continued-page reading position",
      );
      assert.equal(await more.count(), 0, "returned turn forgot loaded continuation depth");
      await page.goto(`http://127.0.0.1:${address.port}/issues`);
      base.items![0]!.body = `新访问的实时回复 ${width}`;
      await detail.waitFor({ state: "hidden" });
      await navigate(page, `/issues/${issue.issue_id}`);
      await detail.getByRole("button", { name: "继续读取本轮消息", exact: true }).waitFor();
      publish();
      await detail
        .getByText(`新访问的实时回复 ${width}`, { exact: true })
        .waitFor({ state: "attached" });
      assert.equal(
        await detail.getByText("第三页保留消息", { exact: true }).count(),
        0,
        "fresh Issue visit reused a closed continuation cache",
      );
      console.log(`PASS third continuation page task return ${width}`);
    } finally {
      await context.close();
      base.more = originalMore;
      thirdPage = false;
      continuationBlock = null;
    }
  }
} finally {
  await browser.close();
  for (const s of streams) s.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
