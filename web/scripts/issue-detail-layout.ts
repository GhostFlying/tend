import { fixtureRoot, navigate } from "./navigation.ts";
/// <reference lib="dom" />
// D129: isolated complete-page and exact-recipient interaction regression.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { chromium } from "playwright";
import { openIssueAux, openIssueDefinition, issueSidebar } from "./issue-detail-helpers.ts";
import type {
  Envelope,
  BackgroundSourcePreview,
  IssueView,
  IssueActivityPage,
  IssueDeliveryView,
  IssueConversationQueueEntry,
  IssueConversationItem,
  IssueEventNotificationPage,
  IssueTranscriptBlock,
  IssueResultPage,
  AttentionView,
  SharedDeliveryReviewPage,
  UpdateIssuePayload,
  UpdateIssueProgressPayload,
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
let sharedReviews: SharedDeliveryReviewPage = { items: [], more: false };
let reportDownload: string | undefined;
const streams = new Set<ServerResponse>();
const writes: Envelope[] = [];
const requests: string[] = [];
let dropProgressReply = false;
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
let additionalAttentions: AttentionView[] = [];
let pagedResponsibility = false;
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
      title: "已交付成果",
      artifact_id: "artifact-one",
      task_id: results.items![0]!.task_id,
    },
  ],
};
const publish = (kind = "issue.progress_updated", item?: IssueConversationItem) => {
  for (const s of streams)
    s.write(
      `id: ${++seq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: seq, object_type: item ? "message" : kind.startsWith("work_item.") ? "work_item" : "issue", object_id: item?.message_id ?? issue.issue_id, kind, created_at: new Date().toISOString(), item })}\n\n`,
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
  if (p === "/report-download" && reportDownload !== undefined) {
    res.writeHead(200, {
      "Content-Type": "application/octet-stream",
      "Content-Disposition": 'attachment; filename="report.md"',
    });
    res.end(reportDownload);
    return;
  }
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
  if (p.startsWith("/api/v1/conversation/blocks/")) {
    const id = decodeURIComponent(p.slice("/api/v1/conversation/blocks/".length));
    const block = activity.items?.find((entry) => entry.block?.trigger.message_id === id)?.block;
    if (block) return json(block);
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
    if (p.endsWith("/delivery-reviews")) return json(sharedReviews);
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
  if (p === "/api/v1/tasks/task-manager" || p.startsWith("/api/v1/tasks/task_")) {
    const id = p.split("/").at(-1)!;
    const second = id.endsWith("FAW");
    const base = {
      task_id: id,
      title: id === "task-manager" ? "负责人执行" : second ? "关联任务 B" : "执行任务 A",
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
  if (p === "/api/v1/inbox") {
    const query = u.searchParams;
    const recipient = query.get("recipient") || "owner";
    let items = [decision, ...additionalAttentions].filter(
      (item) =>
        (!query.get("issue_id") || item.issue_id === query.get("issue_id")) &&
        (!query.get("responsible_issue_id") ||
          (item.responsibility ? item.responsibility.issue_id : item.issue_id) ===
            query.get("responsible_issue_id")) &&
        (recipient === "all" || (item.responsibility?.recipient_kind ?? "owner") === recipient) &&
        (!query.get("status") || item.status === query.get("status")) &&
        (!query.get("kind") || item.kind === query.get("kind")) &&
        (query.get("actionable_only") !== "true" || ["decision", "action"].includes(item.kind)) &&
        (query.get("include_archived") === "true" || !item.archived_at) &&
        (!query.get("after") || item.attention_id > query.get("after")!),
    );
    items.sort((a, b) => a.attention_id.localeCompare(b.attention_id));
    const limit = Math.min(Number(query.get("limit") || 50), pagedResponsibility ? 2 : 100);
    const more = items.length > limit;
    items = items.slice(0, limit);
    return json({ items, more, ...(more ? { next: items.at(-1)!.attention_id } : {}) });
  }
  if (p === "/api/v1/attentions/attention-decision") return json(decision);
  if (p === "/api/v1/ops") {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const envelope = JSON.parse(Buffer.concat(chunks).toString()) as Envelope;
    writes.push(envelope);
    if (envelope.operation === "issues.update") {
      const payload = envelope.payload as UpdateIssuePayload;
      if (
        envelope.target.id !== issue.issue_id ||
        envelope.expected_control_revision !== issue.revision ||
        payload.expected_assignment_epoch !== issue.assignment_epoch
      )
        return json({
          operation: {
            status: "failed",
            error: { code: "stale_revision", message: "Issue changed" },
          },
        });
      issue.plan = payload.plan;
      issue.revision++;
      publish();
      return json({ operation: { status: "succeeded", result: issue } });
    }
    if (envelope.operation === "issues.update_progress") {
      if (dropProgressReply) {
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
  {
    const context = await browser.newContext({ serviceWorkers: "block" });
    const savedSeq = seq;
    seq = 300;
    try {
      const page = await context.newPage();
      const row = activity.items![0]!;
      const original = row.block!;
      const error = "公开过程读取失败，已保存的讨论仍可阅读。";
      const block = {
        ...original,
        trigger: { ...original.trigger, author: "owner", state: "failed", body: "保留原讨论" },
        items: [],
        more: false,
        process: { items: [], more: false, error },
      } satisfies IssueTranscriptBlock;
      let current: IssueTranscriptBlock = block;
      const cursors: (number | null)[] = [];
      const activityPath = `/api/v1/issues/${issue.issue_id}/activity`;
      const detailPath = `/api/v1/conversation/blocks/${block.trigger.message_id}`;
      await page.route(
        (url) => url.pathname === activityPath,
        (route) => {
          const param = new URL(route.request().url()).searchParams.get("since");
          const since = param === null ? null : Number(param);
          if (seq === 100) cursors.push(since);
          if (since !== null && since > seq)
            return route.fulfill({
              status: 400,
              json: { error: { code: "invalid_request", message: "invalid activity cursor" } },
            });
          return route.fulfill({
            json: {
              items:
                since === seq
                  ? []
                  : [
                      {
                        ...row,
                        id: seq === 100 ? "rebuilt-turn" : row.id,
                        block:
                          seq === 100
                            ? { ...block, trigger: { ...block.trigger, body: "恢复后的权威讨论" } }
                            : current,
                      },
                    ],
              readable: true,
              more: false,
              event_seq: seq,
            },
          });
        },
      );
      let retries = 0;
      const refreshError = "本次公开过程详情读取失败。";
      await page.route(
        (url) => url.pathname === detailPath,
        (route) => {
          retries++;
          return route.fulfill({
            json: {
              ...current,
              process: {
                items: [],
                more: false,
                ...(retries === 2 ? { error: refreshError } : {}),
              },
            },
          });
        },
      );
      const connected = page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/v1/events",
      );
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      await connected;
      const discussion = page.locator(`article[data-scroll-id="${row.id}"]`);
      await discussion.getByText("保留原讨论", { exact: true }).waitFor();
      const notice = discussion.getByRole("status").filter({ hasText: error });
      await notice.waitFor();
      assert.equal(retries, 0);
      await discussion.getByRole("button", { name: "重新读取公开过程", exact: true }).click();
      await notice.waitFor({ state: "hidden" });
      assert.equal(retries, 1);

      current = { ...block, trigger: { ...block.trigger, body: "无公开回复的后续活动" } };
      publish("conversation.message", {
        ...current.trigger,
        conversation_id: issue.conversation!.conversation_id!,
      });
      await discussion.getByText("无公开回复的后续活动", { exact: true }).waitFor();
      const refreshedNotice = discussion.getByRole("status").filter({ hasText: refreshError });
      await refreshedNotice.waitFor();
      assert.equal(retries, 2);
      await refreshedNotice.getByRole("button", { name: "重新读取公开过程", exact: true }).click();
      await refreshedNotice.waitFor({ state: "hidden" });
      await notice.waitFor({ state: "hidden" });
      assert.equal(retries, 3);

      seq = 100;
      for (const stream of streams) stream.end(`event: reset\ndata: {"event_seq":${seq}}\n\n`);
      await page.getByText("恢复后的权威讨论", { exact: true }).waitFor();
      assert.equal(cursors[0], null, "an event-stream reset must request a fresh activity head");
      assert.equal(await discussion.count(), 0);
      console.log(
        "PASS readable process error retry without a reply and authoritative activity reset",
      );
    } finally {
      await context.close();
      seq = savedSeq;
    }
  }

  {
    const context = await browser.newContext({ serviceWorkers: "block" });
    try {
      const page = await context.newPage();
      const row = activity.items![0]!;
      const original = row.block!;
      const { message_id, seq: triggerSeq, created_at } = original.trigger;
      const tool = {
        message_id: "urgent-exec-in-progress",
        seq: triggerSeq + 1,
        author: "main_agent",
        kind: "tool_use",
        created_at,
        reply_to: message_id,
        tool: {
          name: "exec_command",
          status: "running",
          turn_id: "owner-turn",
          updated_at: created_at,
        },
      } satisfies IssueConversationItem;
      const block = {
        ...original,
        trigger: {
          message_id,
          seq: triggerSeq,
          created_at,
          kind: "message",
          author: "external",
          body: "检查当前进度",
          issue_owner_steer: {
            message_id,
            issue_id: issue.issue_id,
            assignment_epoch: 1,
            task_id: "owner-task",
            run_id: "owner-run",
            thread_id: "owner-thread",
            turn_id: "owner-turn",
            source_message_id: "urgent-source",
            source_revision: 1,
            sender: { kind: "owner" },
            status: "consumed",
            native_item_id: "owner-native-input",
          },
        },
        items: [{ ...original.items![0]!, seq: triggerSeq + 2, body: "已汇报第一步，仍在检查。" }],
        process: {
          items: [],
          more: false,
          has_tools: true,
          started_at: now - 5_000,
          state: "processing",
          active_tool: "exec_command",
        },
      } satisfies IssueTranscriptBlock;
      let current: IssueTranscriptBlock = block;
      let detailReads = 0;
      await page.route(
        (url) => url.pathname === `/api/v1/issues/${issue.issue_id}/activity`,
        (route) =>
          route.fulfill({
            json: {
              items: [{ ...row, block: current }],
              readable: true,
              more: false,
              event_seq: seq,
            },
          }),
      );
      await page.route(
        (url) => url.pathname === `/api/v1/conversation/blocks/${message_id}`,
        (route) => {
          detailReads++;
          return route.fulfill({ json: { ...block, process: { items: [tool], more: false } } });
        },
      );
      const connected = page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/v1/events",
      );
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      await connected;
      const discussion = page.locator(`article[data-scroll-id="${row.id}"]`);
      await discussion.getByText("已汇报第一步，仍在检查。", { exact: true }).waitFor();
      const stage = discussion.getByRole("status").filter({ hasText: /^正在执行命令$/ });
      await stage.waitFor();
      await discussion
        .locator('[data-active="true"]')
        .getByText(/^\d+:\d{2}$/)
        .waitFor();
      const trace = discussion.getByRole("button", { name: /查看过程/ });
      await trace.click();
      await discussion.locator(`[data-tool-item="${tool.message_id}"]`).waitFor();
      assert(await stage.isVisible(), "opening tool details changed the active tool label");
      await trace.click();
      assert(await stage.isVisible(), "closing tool details changed the active tool label");

      current = { ...block, process: { ...block.process, active_tool: "" } };
      publish("conversation.message", {
        ...block.trigger,
        conversation_id: issue.conversation!.conversation_id!,
      });
      await discussion
        .getByRole("status")
        .filter({ hasText: /^处理中$/ })
        .waitFor();
      assert.equal(detailReads, 1, "a closed view must clear the old tool without reading details");

      current = {
        ...block,
        process: { ...block.process, state: "replied", active_tool: "", ended_at: now },
      };
      publish("conversation.message", {
        ...block.trigger,
        conversation_id: issue.conversation!.conversation_id!,
      });
      await discussion
        .getByRole("status")
        .filter({ hasText: /^已完成$/ })
        .waitFor();
      console.log("PASS urgent process state remains accurate with public prose and loaded tools");
    } finally {
      await context.close();
    }
  }

  for (const readerMoves of [false, true]) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 844 },
      serviceWorkers: "block",
    });
    let releaseResults = () => {};
    try {
      const page = await context.newPage();
      const row = activity.items![0]!;
      const block = row.block!;
      const items = Array.from({ length: 24 }, (_, index) => {
        const id = `return-layout-${index}`;
        const position = row.position + index + 1;
        return {
          ...row,
          id,
          position,
          block: {
            ...block,
            position,
            trigger: { ...block.trigger, message_id: id },
            items: [
              {
                ...block.items![0]!,
                message_id: `${id}-reply`,
                reply_to: id,
                body: `返回前读到 ${index}`,
              },
            ],
          },
        };
      });
      const issuePath = `/issues/${issue.issue_id}`;
      const taskPath = `/tasks/${results.items![0]!.task_id}`;
      let returning = false;
      const hold = new Promise<void>((resolve) => {
        releaseResults = resolve;
      });
      await page.route(
        (u) => u.pathname === `/api/v1/issues/${issue.issue_id}/activity`,
        (route) => route.fulfill({ json: { items, readable: true, more: false, event_seq: seq } }),
      );
      await page.route(
        (u) => u.pathname === `/api/v1/issues/${issue.issue_id}/results`,
        async (route) => {
          if (returning) await hold;
          await route.fallback();
        },
      );
      const frame = () => page.evaluate(() => new Promise(requestAnimationFrame));
      const scroller = page.locator("[data-issue-scroll]");
      const ready = page.getByRole("button", { name: "查看全部交付 · 1+ 项 →" });
      const position = () => scroller.evaluate((node) => node.scrollTop);
      const move = (top: number) =>
        scroller.evaluate((node, value) => {
          node.scrollTop = value;
        }, top);
      const discussion = page.getByText("返回前读到 20", { exact: true });
      await page.goto(`http://127.0.0.1:${address.port}${issuePath}`);
      await ready.waitFor({ state: "attached" });
      await discussion.waitFor({ state: "attached" });
      await move(1200);
      await frame();
      assert.equal(await position(), 1200);
      const timeOrigin = await page.evaluate(() => performance.timeOrigin);
      await navigate(page, taskPath);
      await page.getByRole("region", { name: "任务详情", exact: true }).waitFor();
      returning = true;
      await page.goBack();
      await page.waitForURL((u) => u.pathname === issuePath);
      await discussion.waitFor({ state: "attached" });
      assert.equal(await page.evaluate(() => performance.timeOrigin), timeOrigin);
      assert.equal(await ready.count(), 0);
      // A layout correction can move scrollTop without the reader touching the page.
      await move(1360);
      await frame();
      let expected = 1200;
      if (readerMoves) {
        const bounds = (await scroller.boundingBox())!;
        await page.mouse.move(bounds.x + 20, bounds.y + bounds.height / 2);
        await page.mouse.wheel(0, 740);
        await page.waitForFunction(
          () => document.querySelector("[data-issue-scroll]")!.scrollTop > 2000,
        );
        expected = await position();
      }
      releaseResults();
      await ready.waitFor({ state: "attached" });
      await frame();
      const actual = await position();
      assert(
        Math.abs(actual - expected) <= 2,
        `return after late results: expected ${expected}, got ${actual}`,
      );
      console.log(
        `PASS task return with delayed results, ${readerMoves ? "manual" : "layout"} scroll (${actual} px)`,
      );
    } finally {
      releaseResults();
      await context.close();
    }
  }

  for (const followEnd of [true, false]) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    const row = activity.items![0]!;
    const block = row.block!;
    const prose = "首屏保留可读正文";
    const tool = {
      message_id: "readable-fixture-tool",
      seq: 501,
      author: "main_agent",
      kind: "tool_use",
      created_at: new Date(now).toISOString(),
      tool: {
        name: "fixture_tool",
        input: "fixture input",
        output: "fixture output",
        status: "completed",
        turn_id: "fixture-turn",
        updated_at: new Date(now).toISOString(),
      },
    } satisfies IssueConversationItem;
    const readableBlock = {
      ...block,
      items: [{ ...block.items![0]!, body: prose }],
      process: { items: [], has_tools: true, more: false, started_at: now - 1000, ended_at: now },
      more: false,
    };
    const trailing = Array.from({ length: 18 }, (_, index) => {
      const id = `readable-tail-${index}`;
      const position = row.position + index + 1;
      return {
        ...row,
        id,
        position,
        block: {
          ...readableBlock,
          position,
          process: { ...readableBlock.process, has_tools: false },
          trigger: { ...block.trigger, message_id: id },
          items: [
            {
              ...block.items![0]!,
              message_id: `${id}-reply`,
              reply_to: id,
              body: `继续阅读 ${index}`,
            },
          ],
        },
      };
    });
    const firstTools = Array.from({ length: 14 }, (_, index) => ({
      ...tool,
      message_id: `readable-tool-${index}`,
      seq: tool.seq + index,
    }));
    const processCursor = firstTools.at(-1)!.seq;
    const activityPath = `/api/v1/issues/${issue.issue_id}/activity`;
    const processPath = `/api/v1/conversation/blocks/${block.trigger.message_id}`;
    let detailRequests = 0;
    let changedAt = 0;
    let currentProse = prose;
    let appended: typeof trailing | null = null;
    let starting = () => {};
    let release = () => {};
    const started = new Promise<void>((resolve) => {
      starting = resolve;
    });
    const releaseDetail = new Promise<void>((resolve) => {
      release = resolve;
    });
    let signalSecond = () => {};
    let releaseSecond = () => {};
    let heldSecond = false;
    const secondStarted = new Promise<void>((resolve) => {
      signalSecond = resolve;
    });
    const secondGate = new Promise<void>((resolve) => {
      releaseSecond = resolve;
    });
    let releaseResults = () => {};
    if (followEnd) {
      const resultsGate = new Promise<void>((resolve) => {
        releaseResults = resolve;
      });
      await page.route(
        (url) => url.pathname === `/api/v1/issues/${issue.issue_id}/results`,
        async (route) => {
          await resultsGate;
          await route.fallback();
        },
      );
    }
    await page.route(
      (u) => u.pathname === activityPath,
      (route) => {
        const query = new URL(route.request().url()).searchParams;
        assert.equal(query.get("view"), "readable");
        return route.fulfill({
          json: {
            items: query.has("since")
              ? Number(query.get("since")) < changedAt
                ? (appended ?? [
                    {
                      ...row,
                      block: {
                        ...readableBlock,
                        items: [{ ...readableBlock.items[0]!, body: currentProse }],
                      },
                    },
                  ])
                : []
              : [{ ...row, block: readableBlock }, ...trailing],
            more: false,
            readable: true,
            event_seq: seq,
          },
        });
      },
    );
    await page.route(
      (u) => u.pathname === processPath && !u.searchParams.has("conversation_id"),
      async (route) => {
        detailRequests++;
        starting();
        await releaseDetail;
        const second = new URL(route.request().url()).searchParams.has("process_after");
        const priorSecond = second && !heldSecond;
        if (priorSecond) {
          heldSecond = true;
          signalSecond();
          await secondGate;
        }
        await route.fulfill({
          json: {
            ...readableBlock,
            process: {
              ...readableBlock.process,
              items: second
                ? [
                    {
                      ...tool,
                      message_id: priorSecond ? "readable-second-tool" : "readable-renewed-tool",
                      seq: processCursor + 1,
                    },
                  ]
                : firstTools,
              more: !second,
              ...(second ? {} : { next: processCursor }),
            },
          },
        });
      },
    );
    try {
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      const discussion = page.locator(`article[data-scroll-id="${row.id}"]`);
      await discussion.getByText(prose, { exact: true }).waitFor();
      await started;
      assert.equal(detailRequests, 1);
      const trigger = discussion.getByRole("button", { name: /查看过程/ });
      if (followEnd) {
        await trigger.scrollIntoViewIfNeeded();
        await trigger.evaluate(
          (control) =>
            new Promise<void>((resolve) => {
              const node = control.closest("[data-issue-scroll]")!;
              const panel = document.getElementById(control.getAttribute("aria-controls")!)!;
              const observer = new MutationObserver(() => {
                if (panel.hidden) return;
                observer.disconnect();
                node.scrollTop = node.scrollHeight;
                resolve();
              });
              observer.observe(panel, { attributeFilter: ["hidden"] });
              (control as HTMLButtonElement).click();
            }),
        );
      } else await trigger.click();
      await discussion.getByText("正在读取公开过程…", { exact: true }).waitFor();
      assert.equal(detailRequests, 1, "opening must reuse the held detail prefetch");
      const lastTool = firstTools.at(-1)!.message_id;
      const scrollport = page.locator("[data-issue-scroll]");
      const initialHeight = await scrollport.evaluate((node, followingEnd) => {
        if (!followingEnd) node.scrollTop = node.scrollHeight;
        return node.scrollHeight;
      }, followEnd);
      await page.evaluate(() => new Promise(requestAnimationFrame));
      if (followEnd) {
        const measureGap = () =>
          scrollport.evaluate((node) => node.scrollHeight - node.scrollTop - node.clientHeight);
        const gapAfterDisclosure = await measureGap();
        assert(
          gapAfterDisclosure <= 2,
          `disclosure moved the reader ${gapAfterDisclosure} px from the end`,
        );
        const resultsCount = page.getByRole("button", { name: "查看全部交付 · 1+ 项 →" });
        assert.equal(await resultsCount.count(), 0, "results must still be pending");
        releaseResults();
        await resultsCount.waitFor({ state: "attached" });
        const gap = await measureGap();
        assert(gap <= 2, `late results moved the reader ${gap} px from the end`);
        appended = trailing.slice(-4).map((entry, index) => {
          const id = `new-readable-tail-${index}`;
          const position = trailing.at(-1)!.position + index + 1;
          return {
            ...entry,
            id,
            position,
            block: {
              ...entry.block,
              position,
              trigger: { ...entry.block.trigger, message_id: id },
              items: [
                {
                  ...entry.block.items[0]!,
                  message_id: `${id}-reply`,
                  reply_to: id,
                  body: `末尾新增 ${index}`,
                },
              ],
            },
          };
        });
        changedAt = seq + 1;
        publish("conversation.message", {
          ...appended.at(-1)!.block.trigger,
          conversation_id: issue.conversation!.conversation_id!,
        });
        await page.getByText("末尾新增 3", { exact: true }).waitFor();
        await page.waitForFunction((height) => {
          const node = document.querySelector("[data-issue-scroll]")!;
          return (
            node.scrollHeight > height + 100 &&
            node.scrollHeight - node.scrollTop - node.clientHeight <= 2
          );
        }, initialHeight);
        const measureEnd = () =>
          scrollport.evaluate((node) => ({
            height: node.scrollHeight,
            gap: node.scrollHeight - node.scrollTop - node.clientHeight,
          }));
        const before = await measureEnd();
        release();
        await discussion.locator(`[data-tool-item="${lastTool}"]`).waitFor();
        await page.evaluate(() => new Promise(requestAnimationFrame));
        const after = await measureEnd();
        assert(
          after.height > before.height && before.gap <= 2 && after.gap <= 2,
          `following the end moved from ${before.gap} px to ${after.gap} px after process details arrived`,
        );
        assert.equal(detailRequests, 1);
        console.log(
          `PASS following the end through late results, activity updates, and process details (${gap} → ${before.gap} → ${after.gap} px)`,
        );
        continue;
      }
      const paragraph = page.getByText("继续阅读 3", { exact: true });
      await page.exposeBinding("releaseActivityDetail", () => release());
      const readingPositions = await paragraph.evaluateHandle(
        (target, toolID) =>
          new Promise<{ stop(): { originalTop: number; positions: number[] } }>((done) => {
            requestAnimationFrame(() => {
              const scrollport = target.closest("[data-issue-scroll]")!;
              const selector = `[data-tool-item="${CSS.escape(toolID)}"]`;
              const positions: number[] = [];
              const capture = () => {
                if (scrollport.querySelector(selector)?.checkVisibility())
                  positions.push(target.getBoundingClientRect().top);
              };
              scrollport.addEventListener("scroll", capture);
              target.scrollIntoView({ block: "center" });
              const originalTop = target.getBoundingClientRect().top;
              // Deliver the response before the next scroll event can update the Issue.
              void (
                window as unknown as { releaseActivityDetail(): Promise<void> }
              ).releaseActivityDetail();
              done({
                stop() {
                  scrollport.removeEventListener("scroll", capture);
                  return { originalTop, positions };
                },
              });
            });
          }),
        lastTool,
      );
      await discussion.locator(`[data-tool-item="${lastTool}"]`).waitFor();
      await page.evaluate(() => new Promise(requestAnimationFrame));
      const { originalTop, positions } = await readingPositions.evaluate((recorder) =>
        recorder.stop(),
      );
      await readingPositions.dispose();
      positions.push((await paragraph.boundingBox())!.y);
      assert(
        positions.every((top) => Math.abs(top - originalTop) <= 2),
        `process rendering moved the reading anchor from ${originalTop} to ${positions.join(", ")}`,
      );
      assert.equal(await discussion.locator("[data-tool-item]:visible").count(), 12);
      assert.equal(detailRequests, 1);
      await discussion.getByText(prose, { exact: true }).waitFor();
      await discussion.getByRole("button", { name: "继续读取公开过程" }).click();
      await secondStarted;
      for (let update = 1; update <= 3; update++) {
        currentProse = `继续流式回复 ${update}`;
        changedAt = seq + 1;
        publish("conversation.message", {
          ...readableBlock.items[0]!,
          body: currentProse,
          conversation_id: issue.conversation!.conversation_id!,
        });
        await discussion.getByText(currentProse, { exact: true }).waitFor();
        assert.equal(detailRequests, 2, "the disclosed page must finish through prose updates");
      }
      releaseSecond();
      const renewed = discussion.locator('[data-tool-item="readable-renewed-tool"]');
      await renewed.waitFor();
      assert.equal(detailRequests, 4, "one refresh must load through the disclosed page");
      assert.equal(await trigger.getAttribute("aria-expanded"), "true");
      await discussion.screenshot({ path: join(output, "issue-readable-process.png") });

      const taskPath = `/tasks/${results.items![0]!.task_id}`;
      const issuePath = `/issues/${issue.issue_id}`;
      const documentStart = await page.evaluate(() => performance.timeOrigin);
      await navigate(page, taskPath);
      await page.getByRole("region", { name: "任务详情", exact: true }).waitFor();
      await discussion.waitFor({ state: "detached" });
      await page.goBack();
      await page.waitForURL((url) => url.pathname === issuePath);
      await renewed.waitFor();
      assert.equal(detailRequests, 6, "returning must restore both pages on the earlier entry");
      assert.equal(await trigger.getAttribute("aria-expanded"), "true");
      assert.equal(await page.evaluate(() => performance.timeOrigin), documentStart);

      await navigate(page, taskPath);
      await discussion.waitFor({ state: "detached" });
      await navigate(page, issuePath);
      await trigger.waitFor();
      assert.equal(await trigger.getAttribute("aria-expanded"), "false");
      await trigger.click();
      await discussion.getByRole("button", { name: "继续读取公开过程" }).waitFor();
      assert.equal(detailRequests, 7, "a new navigation entry starts with only the first page");
      await discussion.getByRole("button", { name: "显示更早 2 步", exact: true }).click();
      assert.equal(await discussion.locator("[data-tool-item]:visible").count(), 14);
      console.log(
        "PASS readable detail reuse, anchor, streamed updates, and navigation page depth",
      );
    } finally {
      releaseResults();
      releaseSecond();
      release();
      await context.close();
    }
  }

  {
    const context = await browser.newContext({ viewport: { width: 1440, height: 844 } });
    try {
      const page = await context.newPage();
      const entry = activity.items![0]!;
      const block = entry.block!;
      const reply = block.items![0]!;
      const updated = { ...reply, body: "在 Issue 元数据之前到达的回复", assistant_revision: 2 };
      let firstSeen = () => {};
      let sendMetadata = () => {};
      const firstActivity = new Promise<void>((resolve) => {
        firstSeen = resolve;
      });
      const metadata = new Promise<void>((resolve) => {
        sendMetadata = resolve;
      });
      let published = false;
      const issuePath = `/api/v1/issues/${issue.issue_id}`;
      const activityPath = `${issuePath}/activity`;
      await page.route(
        (url) => url.pathname === issuePath,
        async (route) => {
          await metadata;
          await route.fulfill({ json: issue });
        },
      );
      await page.route(
        (url) => url.pathname === activityPath,
        async (route) => {
          await route.fulfill({
            json: {
              items: [{ ...entry, block: published ? { ...block, items: [updated] } : block }],
              more: false,
              readable: true,
              event_seq: seq,
            },
          });
          firstSeen();
        },
      );
      const connected = page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/v1/events",
      );
      try {
        await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
        await Promise.all([connected, firstActivity]);
        const delta = page.waitForResponse((response) => {
          const url = new URL(response.url());
          return url.pathname === activityPath && url.searchParams.has("since");
        });
        published = true;
        publish("conversation.message", {
          ...updated,
          conversation_id: issue.conversation!.conversation_id!,
        });
        await delta;
        sendMetadata();
        await page.getByText(updated.body, { exact: true }).waitFor();
        console.log("PASS live reply received before Issue metadata");
      } finally {
        sendMetadata();
      }
    } finally {
      await context.close();
    }
  }

  for (const width of [1440, 320]) {
    const context = await browser.newContext({ viewport: { width, height: 844 } });
    try {
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error" || message.type() === "warning") errors.push(message.text());
      });
      const displayName = width === 320 ? "River Frontend Implementer" : "River";
      await page.route("**/api/v1/hosted-agents", (route) =>
        route.fulfill({
          json: {
            configured: true,
            projection: {
              agents: [
                {
                  identity: { execution_environment_id: "hosted", local_id: "qingzhu" },
                  display_name: displayName,
                  purposes: ["issue_manager"],
                  enabled: true,
                },
              ],
              runtimes: [],
            },
          },
        }),
      );
      const path = `/issues/${issue.issue_id}`;
      await page.goto(`http://127.0.0.1:${address.port}${path}`);
      assert.equal(new URL(page.url()).pathname, path);
      assert.equal(await page.title(), "tend");
      const properties = (await issueSidebar(page)).locator("section").filter({
        has: page.getByRole("heading", { name: "属性", exact: true }),
      });
      const assignee = properties
        .getByRole("button", { name: "交接负责人", exact: true })
        .getByText(`${displayName} · ${issue.assignee.ref}`, { exact: true });
      const runDetails = properties.getByRole("button", { name: "查看运行详情", exact: true });
      const left = await assignee.evaluate(
        (node) => node.closest("dd")!.getBoundingClientRect().left,
      );
      await properties.screenshot({ path: join(output, `issue-properties-${width}.png`) });
      for (const value of [
        assignee,
        properties.getByText("负责人会话运行中", { exact: true }),
        runDetails,
        properties.locator("[data-issue-status]"),
        properties.getByText(issue.plan.project, { exact: true }),
      ]) {
        const offsets = await value.evaluate((node, left) => {
          const range = document.createRange();
          range.selectNodeContents(node);
          return Array.from(range.getClientRects(), (rect) => rect.left - left);
        }, left);
        const label = await value.innerText();
        assert(offsets.length > 0, `${label} must have rendered text`);
        if (value === assignee && width === 320) {
          assert(offsets.length > 1, "the narrow drawer must wrap the assignee text");
        }
        assert(
          offsets.every((offset) => Math.abs(offset) <= 1),
          `${label}: offsets ${offsets}`,
        );
        console.log(
          `Issue properties ${width}px: ${label.replaceAll("\n", " ")} offsets ${offsets}`,
        );
      }
      await runDetails.click();
      await page.waitForURL((url) => url.pathname === `/tasks/${issue.manager!.task_id}`);
      const task = page.getByRole("region", { name: "任务详情", exact: true });
      await task.getByText("负责人执行", { exact: true }).waitFor();
      await task.screenshot({ path: join(output, `issue-manager-details-${width}.png`) });
      assert.deepEqual(errors, []);
    } finally {
      await context.close();
    }
  }

  for (const width of [1440, 390]) {
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push(message.text());
    });
    try {
      const head = "a".repeat(40);
      const next = "b".repeat(40);
      const base = "c".repeat(40);
      const target = "d".repeat(40);
      const ref = {
        delivery_id: "conflict-pr",
        provider: "github",
        repository: "sample/repo",
        number: 343,
      } as const;
      const original: IssueDeliveryView = {
        delivery: ref,
        observation: {
          issue_id: issue.issue_id,
          delivery_id: ref.delivery_id,
          state: "open",
          head_sha: head,
          base_sha: base,
          target_sha: target,
          mergeability: "conflicting",
          observer: "server",
          generation: 1,
          checked_at: now,
          ci: { head_sha: head, state: "pending", checks: [] },
          last_conflict: {
            head_sha: head,
            base_sha: base,
            target_sha: target,
            observer: "server",
            revision: 3,
            checked_at: now,
          },
        },
      };
      let current = original;
      let ownerItems: IssueConversationQueueEntry[] = [];
      const activityState: { urgentOriginal?: IssueTranscriptBlock } = {};
      let backgroundRequested = false;
      let interruptedMessage: string | undefined;
      const issueConversation = issue.conversation!.conversation_id!;
      const backgroundItems: IssueConversationQueueEntry[] = ["processing", "queued", "queued"].map(
        (status, index) => ({
          status,
          position: index + 1,
          revision: 0,
          can_withdraw: false,
          item: {
            message_id: `20-44-background-${index}`,
            seq: index + 1,
            kind: "message",
            author: "system",
            conversation_id: issueConversation,
            body: "后台合并通知",
            notice_type: "background_batch",
            created_at: new Date(now).toISOString(),
          },
        }),
      );
      await page.route("**/api/v1/conversation/queue?*", async (route) => {
        const url = new URL(route.request().url());
        if (url.searchParams.get("conversation_id") !== issueConversation) {
          await route.continue();
          return;
        }
        const category = url.searchParams.get("category");
        if (category === "background") backgroundRequested = true;
        const items =
          category === "owner" ? ownerItems : category === "background" ? backgroundItems : [];
        await route.fulfill({
          json: {
            items,
            total: items.length,
            interruptible_message_id:
              category === "owner" && !interruptedMessage
                ? backgroundItems[0]!.item.message_id
                : undefined,
            event_seq: seq,
            enabled: true,
          },
        });
      });
      const diagnostics: IssueEventNotificationPage = {
        event_id: "review-queue-event",
        notifications: [
          {
            position: 1,
            batch_id: "review-batch",
            message_id: backgroundItems[0]!.item.message_id,
            batch_status: "frozen",
            delivery: "received",
            source: "read",
            started_at: now,
            native_status: "started",
            settlement: "awaiting_clarification",
            reason: "等待原审阅报告关联",
          },
        ],
      };
      let diagnosticsReads = 0;
      await page.route(`**/api/v1/issues/${issue.issue_id}/activity/3/notification`, (route) => {
        diagnosticsReads++;
        return route.fulfill({ json: diagnostics });
      });
      const originalBinding = { ...ref, head_sha: head, base_sha: base, target_sha: target };
      let issueView = {
        ...issue,
        progress: { ...issue.progress!, delivery: originalBinding },
      } as IssueView;
      let savedProgress: UpdateIssueProgressPayload | undefined;
      await page.route(`**/api/v1/issues/${issue.issue_id}`, (route) =>
        route.fulfill({ json: issueView }),
      );
      await page.route("**/api/v1/ops", (route) => {
        const envelope = route.request().postDataJSON() as Envelope;
        if (envelope.operation === "conversation.interrupt_reply") {
          interruptedMessage = envelope.target.id;
          return route.fulfill({ json: { operation: { status: "succeeded" } } });
        }
        assert.equal(envelope.operation, "issues.update_progress");
        savedProgress = envelope.payload as UpdateIssueProgressPayload;
        return route.fulfill({
          json: { operation: { status: "succeeded", result: issueView.progress } },
        });
      });
      let releaseDelivery = () => {};
      const delayedDelivery = new Promise<void>((resolve) => {
        releaseDelivery = resolve;
      });
      await page.route(`**/api/v1/issues/${issue.issue_id}/deliveries`, async (route) => {
        await delayedDelivery;
        await route.fulfill({ json: { issue_id: issue.issue_id, items: [current] } });
      });
      const activityPath = `/api/v1/issues/${issue.issue_id}/activity`;
      await page.route(
        (url) => url.pathname === activityPath,
        (route) =>
          route.fulfill({
            json: {
              ...activity,
              event_seq: seq,
              items: [
                ...activity.items!,
                {
                  id: "review-queue-event",
                  position: 3,
                  kind: "task.completed",
                  author: "tend",
                  title: "任务结果已返回",
                  task_id: "reviewer-task",
                  created_at: now,
                  issue_id: issue.issue_id,
                },
                {
                  id: "old-pr-checks",
                  position: 4,
                  kind: "work_item.delivery_candidate_retired",
                  author: "tend",
                  title: "保留了被替换的候选观察",
                  created_at: now,
                  delivery: original,
                },
                ...(activityState.urgentOriginal
                  ? [
                      {
                        id: "urgent-original",
                        position: 1100,
                        kind: "conversation",
                        created_at: now,
                        block: activityState.urgentOriginal,
                      },
                    ]
                  : []),
              ],
            },
          }),
      );
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      assert.equal(new URL(page.url()).pathname, `/issues/${issue.issue_id}`);
      const progress = page.locator("[data-issue-progress]");
      await progress.getByRole("button", { name: "编辑", exact: true }).click();
      const editor = page.getByRole("dialog", { name: "编辑当前进展", exact: true });
      const chooser = editor.getByLabel("关联候选");
      const firstCandidate = chooser.getByRole("option", {
        name: `sample/repo #343 · ${head.slice(0, 12)}`,
        exact: true,
      });
      const nextStep = editor.getByRole("textbox", { name: "下一步", exact: true });
      assert.equal(await firstCandidate.count(), 0);
      await nextStep.fill("继续验证原候选");
      releaseDelivery();
      await firstCandidate.waitFor({ state: "attached" });
      assert.equal(await chooser.inputValue(), "original");
      assert.equal(await nextStep.inputValue(), "继续验证原候选");
      await editor.getByRole("button", { name: "保存进展", exact: true }).click();
      await editor.waitFor({ state: "hidden" });
      assert.deepEqual(savedProgress?.delivery, originalBinding);

      await progress.getByText("检测到合并冲突", { exact: false }).waitFor();
      await progress.getByText("GitHub · 服务端观察", { exact: false }).waitFor();
      await progress
        .locator("[data-delivery-progress]")
        .locator(`code[title="${target}"]`)
        .waitFor();
      assert.equal(await progress.getByText("已看到冲突", { exact: false }).count(), 0);
      assert.equal(await page.locator("vite-error-overlay").count(), 0);
      await progress.screenshot({ path: join(output, `delivery-conflict-${width}.png`) });

      const composer = page.locator('[data-presentation="issue"]');
      await composer.getByRole("textbox", { name: "消息", exact: true }).waitFor();
      await composer.getByRole("button", { name: "发送", exact: true }).waitFor();
      assert.equal(await composer.getByText(/加入队列/).count(), 0);
      assert.equal(await page.getByText(/后台事项 · 3/).count(), 0);
      assert.equal(backgroundRequested, false);
      const stop = composer.getByRole("button", { name: "停止当前回复", exact: true });
      await stop.waitFor();
      await composer.screenshot({ path: join(output, `owner-queue-background-only-${width}.png`) });

      current = {
        ...original,
        observation: {
          ...original.observation,
          ci: { head_sha: head, state: "failure", checks: [] },
        },
        review: {
          round: 1,
          head_sha: head,
          base_sha: base,
          verdict: "changes_requested",
          recorded: false,
          valid: false,
        },
      };
      publish("work_item.delivery_changed");
      await progress.getByText("独立审阅已返回 · 需要修改", { exact: true }).waitFor();
      await progress.getByText("当前候选的提交 CI · 检查失败", { exact: true }).waitFor();
      await progress.getByText("检测到合并冲突", { exact: false }).waitFor();
      await progress.screenshot({
        path: join(output, `owner-queue-default-progress-${width}.png`),
      });
      assert.equal(diagnosticsReads, 0, "notification reads should be on demand");
      const event = page.locator('[data-scroll-id="review-queue-event"]');
      if ((await event.locator("summary").first().textContent()) !== "查看这条事件的通知诊断")
        await event.locator("summary").first().click();
      const readNotice = event.getByText("查看这条事件的通知诊断", { exact: true });
      await readNotice.click();
      await event.getByText("送达 · Agent 已接收通知", { exact: true }).waitFor();
      await event.getByText("等待原因 · 等待原审阅报告关联", { exact: true }).waitFor();
      await event.getByText("结算 · 等待澄清", { exact: true }).waitFor();
      const eventHeading = await event.locator("[data-activity-event-heading]").boundingBox();
      const openNotice = await readNotice.boundingBox();
      assert(eventHeading && openNotice && openNotice.y >= eventHeading.y + eventHeading.height);
      assert(eventHeading.width >= openNotice.width && eventHeading.width > 200);
      await event.screenshot({ path: join(output, `owner-queue-event-notification-${width}.png`) });

      ownerItems = [
        {
          position: 4,
          revision: 1,
          status: "queued",
          can_withdraw: true,
          item: {
            message_id: "20-44-user-followup",
            seq: 4,
            kind: "message",
            author: "owner",
            conversation_id: issueConversation,
            body: "一条真实用户消息",
            created_at: new Date(now).toISOString(),
          },
        },
      ];
      publish("conversation.message", { ...ownerItems[0]!.item, control: { revision: 1 } });
      const ownerQueue = page.getByRole("region", { name: "待处理队列", exact: true });
      await ownerQueue.getByText("待处理 · 1", { exact: true }).waitFor();
      await ownerQueue.locator("summary").filter({ hasText: "一条真实用户消息" }).waitFor();
      await ownerQueue.getByText(/等待处理 ·/).waitFor();
      assert(await ownerQueue.getByRole("button", { name: "撤回" }).isEnabled());
      await composer.getByRole("button", { name: "加入队列", exact: true }).waitFor();
      await ownerQueue.screenshot({ path: join(output, `owner-queue-mixed-${width}.png`) });
      assert.equal(await page.getByText(/后台事项 · 3/).count(), 0);
      const ordinary = ownerItems[0]!;
      const urgentInput = {
        ...ordinary.item,
        message_id: "urgent-queued-source",
        seq: 5,
        body: "已通过急件读取的原用户消息",
        state: "queued" as const,
      };
      ownerItems = [ordinary, { ...ordinary, position: 5, item: urgentInput }];
      publish("conversation.message", { ...urgentInput, control: { revision: 1 } });
      await ownerQueue.getByText("待处理 · 2", { exact: true }).waitFor();
      assert.equal(await ownerQueue.getByRole("button", { name: "撤回" }).count(), 2);
      const urgentOriginal: IssueTranscriptBlock = {
        position: 5,
        more: false,
        trigger: {
          ...urgentInput,
          state: "consumed",
          consumed_issue_owner_steer: {
            message_id: "urgent-canonical-receipt",
            issue_id: issue.issue_id,
            assignment_epoch: 2,
            task_id: "urgent-target-task",
            run_id: "urgent-target-run",
            thread_id: "urgent-target-thread",
            turn_id: "urgent-target-turn",
            source_message_id: urgentInput.message_id,
            source_revision: 1,
            sender: { kind: "owner" },
            status: "consumed",
            native_item_id: "proof-native-user-item",
          },
        },
        items: [],
      };
      activityState.urgentOriginal = urgentOriginal;
      ownerItems = [ordinary];
      publish("conversation.urgent_consumed", urgentOriginal.trigger);
      await ownerQueue.getByText("待处理 · 1", { exact: true }).waitFor();
      assert.equal(await ownerQueue.getByRole("button", { name: "撤回" }).count(), 1);
      assert.equal(await ownerQueue.getByText(urgentInput.body, { exact: true }).count(), 0);
      const delivered = page.locator('[data-scroll-id="urgent-queued-source"]');
      await delivered.getByText(urgentInput.body, { exact: true }).waitFor();
      await delivered.getByText("负责人已在原生回合读取急件", { exact: true }).waitFor();
      await delivered.getByText("急件回执", { exact: true }).click();
      await delivered.getByText("proof-native-user-item", { exact: true }).waitFor();
      await delivered.screenshot({ path: join(output, `owner-queue-urgent-receipt-${width}.png`) });
      console.log(`PASS urgent consumption receipt and actual owner queue ${width}`);
      await stop.click();
      await stop.waitFor({ state: "hidden" });
      assert.equal(interruptedMessage, backgroundItems[0]!.item.message_id);
      console.log(
        `PASS 20:44 owner-only queue, current progress, event diagnostics and background stop ${width}`,
      );

      for (const [action, label] of [
        ["seen", "已看到冲突"],
        ["working", "已开始处理"],
      ]) {
        current = {
          ...original,
          conflict_progress: {
            ...issue.progress!,
            delivery: {
              ...ref,
              head_sha: head,
              base_sha: base,
              target_sha: target,
              conflict_revision: 3,
              conflict_action: action!,
            },
          },
        };
        publish();
        await progress.getByText(label!, { exact: false }).waitFor();
      }
      current = {
        ...original,
        observation: {
          ...original.observation,
          head_sha: next,
          base_sha: target,
          mergeability: "conflicting",
          previous_head_sha: head,
          last_conflict: {
            head_sha: next,
            base_sha: target,
            target_sha: target,
            observer: "server",
            revision: 4,
            checked_at: now,
          },
          ci: { head_sha: next, state: "running", checks: [] },
        },
      };
      issueView = {
        ...issue,
        progress: { ...issue.progress!, historical: true, historical_reason: "issue_reopened" },
      };
      publish("work_item.delivery_changed");
      await progress.getByText("历史进展", { exact: true }).waitFor();
      await progress.getByText("已推送新候选", { exact: false }).waitFor();
      await progress.locator(`code[title="${next}"]`).waitFor();
      await progress.getByText("检测到合并冲突", { exact: false }).waitFor();
      await progress.getByText("当前候选的提交 CI · 检查进行中").waitFor();
      assert.equal(await progress.getByText("已开始处理", { exact: false }).count(), 0);
      await page
        .locator('[data-scroll-id="delivery-candidate:old-pr-checks"]')
        .getByText("当时 CI · 等待检查")
        .waitFor();
      await progress.screenshot({ path: join(output, `delivery-candidate-${width}.png`) });
      assert.deepEqual(errors, []);
      console.log(`PASS Issue conflict, public actions and candidate history ${width}`);
    } finally {
      await context.close();
    }
  }

  for (const width of [1440, 390]) {
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push(message.text());
    });
    const head = "8".repeat(40);
    const old = "7".repeat(40);
    const next = "9".repeat(40);
    const target = "a".repeat(40);
    const advanced = "b".repeat(40);
    const observation = (
      index: number,
      commit: string,
      { number = 372, previous = "", checkCount = 6, moved = false, failing = false } = {},
    ): NonNullable<IssueActivityPage["items"]>[number] => {
      const delivery = {
        delivery_id: `github:sample/repo#${number}`,
        provider: "github" as const,
        repository: "sample/repo",
        number,
      };
      return {
        id: `pr-observation-${index}`,
        position: index,
        kind: "work_item.delivery_changed",
        author: "tend",
        title: "记录了交付候选观察",
        created_at: now + index,
        issue_id: issue.issue_id,
        delivery: {
          delivery,
          observation: {
            head_sha: commit,
            previous_head_sha: previous,
            base_sha: target,
            target_sha: moved ? advanced : target,
            mergeability: failing ? "conflicting" : "mergeable",
            state: "open",
            checked_at: now + index,
            observer: "server",
            issue_id: issue.issue_id,
            delivery_id: delivery.delivery_id,
            generation: 1,
            ci: {
              head_sha: commit,
              state: "pending",
              checks: Array.from({ length: checkCount }, (_, i) => ({
                name: i === 0 ? "check" : `job ${i}`,
                state: i === 0 && failing ? "completed" : "in_progress",
                ...(i === 0 && failing ? { conclusion: "failure" } : {}),
              })),
            },
          },
        },
      };
    };
    const initialRows = Array.from({ length: 19 }, (_, index) =>
      observation(101 + index, head, { previous: old, checkCount: index === 0 ? 80 : 6 }),
    );
    const olderRows = [
      observation(86, old),
      ...Array.from({ length: 12 }, (_, index) => observation(87 + index, head, { previous: old })),
    ];
    const rows = [observation(99, head, { number: 375 }), ...initialRows];
    const newRows = [
      observation(120, head, { previous: old, checkCount: 8, moved: true }),
      observation(121, head, { previous: old, checkCount: 8, moved: true, failing: true }),
      observation(122, next),
    ];
    let publishUpdates = false;
    let releaseEarlier = () => {};
    const heldEarlier = new Promise<void>((resolve) => {
      releaseEarlier = resolve;
    });
    try {
      await page.route(`**/api/v1/issues/${issue.issue_id}/activity*`, async (route) => {
        const params = new URL(route.request().url()).searchParams;
        if (params.has("before")) {
          assert.equal(params.get("before"), "99");
          await heldEarlier;
          return route.fulfill({
            json: { items: olderRows, more: false, event_seq: 119, readable: true },
          });
        }
        if (params.has("since")) {
          return route.fulfill({
            json: {
              items: publishUpdates ? newRows : [],
              more: false,
              event_seq: publishUpdates ? 122 : 119,
              readable: true,
            },
          });
        }
        return route.fulfill({
          json: { items: rows, more: true, next_before: 99, event_seq: 119, readable: true },
        });
      });
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      assert.equal(new URL(page.url()).pathname, `/issues/${issue.issue_id}`);
      await page.getByRole("region", { name: "Issue 详情", exact: true }).waitFor();
      assert.equal(await page.title(), "tend");
      const cards = page.locator("[data-delivery-candidate]");
      const card = cards
        .filter({ has: page.locator(`code[title="${head}"]`) })
        .filter({ hasText: "sample/repo #372" });
      const summary = card.getByText("最新观察", { exact: false });
      await summary.waitFor();
      await card.getByText("当时 CI · 等待检查 · 6 项检查", { exact: true }).waitFor();
      assert.equal(await cards.count(), 2);
      assert.equal(await cards.getByText("已推送新候选", { exact: false }).count(), 1);
      assert.equal(await card.getByText("无合并冲突", { exact: false }).count(), 1);
      await card.getByText("查看 19 条观察记录", { exact: true }).click();
      await card.locator(":scope > details > details > summary").first().click();
      const check = card.getByText("job 34 · 检查进行中", { exact: true });
      await check.waitFor();

      const scroll = page.locator("[data-issue-scroll]");
      await scroll.evaluate((node) => {
        node.scrollTop = 0;
      });
      await page
        .locator('[data-history-boundary][aria-busy="true"]')
        .waitFor({ state: "attached" });
      const scrollRoot = await scroll.elementHandle();
      assert(scrollRoot);
      const offset = () =>
        check.evaluate(
          (node, root) => node.getBoundingClientRect().top - root.getBoundingClientRect().top,
          scrollRoot,
        );
      await check.evaluate((node, root) => {
        root.scrollTop += node.getBoundingClientRect().top - root.getBoundingClientRect().top - 120;
      }, scrollRoot);
      const settlePaint = () =>
        page.evaluate(
          () => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))),
        );
      await settlePaint();
      const before = await offset();
      assert(Math.abs(before - 120) <= 1, `check at ${before}px before pagination at ${width}px`);
      releaseEarlier();
      await card.getByText("查看 31 条观察记录", { exact: true }).waitFor();
      await settlePaint();
      const movement = (await offset()) - before;
      assert(Math.abs(movement) <= 1, `check shifted ${movement}px after pagination at ${width}px`);
      await check.waitFor();
      assert.equal(await cards.count(), 3);
      console.log(
        `PASS Issue delivery observation reading position ${width} (${movement}px shift)`,
      );

      publishUpdates = true;
      publish("work_item.delivery_changed");
      await card.getByText("查看 33 条观察记录", { exact: true }).waitFor();
      await card.getByText("当时 CI · 等待检查 · 8 项检查", { exact: true }).waitFor();
      await summary.locator(`code[title="${advanced}"]`).waitFor();
      await card.getByText("检测到合并冲突", { exact: false }).waitFor();
      await card.locator('li[data-tone="error"]').getByText("check · 检查失败").waitFor();
      assert.equal(await cards.count(), 4);
      assert.equal(await cards.getByText("已推送新候选", { exact: false }).count(), 2);
      assert.equal(await card.locator("details[open]").count(), 2);
      assert.equal(await card.locator(":scope > details > details").count(), 33);
      await check.waitFor();
      await card.locator('[data-scroll-id="pr-observation-120"] > summary').click();
      await card
        .locator('[data-scroll-id="pr-observation-120"]')
        .getByText("job 7 · 检查进行中", { exact: true })
        .waitFor();
      assert.equal(await page.locator("vite-error-overlay").count(), 0);
      assert.equal(await card.evaluate((node) => node.scrollWidth <= node.clientWidth), true);
      await card.getByText("查看 33 条观察记录", { exact: true }).click();
      await card.screenshot({ path: join(output, `delivery-candidate-group-${width}.png`) });
      assert.deepEqual(errors, []);
      console.log(`PASS Issue delivery candidate groups and observation history ${width}`);
    } finally {
      releaseEarlier();
      await context.close();
    }
  }

  // Read summaries as summaries, then fetch the full report and its original bytes on demand.
  for (const width of [1440, 390]) {
    for (const indexTiming of ["index-first", "historical-first", "while-reading"] as const) {
      const context = await browser.newContext({
        viewport: { width, height: width > 760 ? 1000 : 844 },
        serviceWorkers: "block",
        acceptDownloads: true,
      });
      const page = await context.newPage();
      page.setDefaultTimeout(10_000);
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      page.on("console", (message) => {
        if (
          message.type() === "warning" &&
          message.text() === "Service Worker registration blocked by Playwright"
        )
          return;
        if (["warning", "error"].includes(message.type())) errors.push(message.text());
      });
      const body =
        "# 调查报告\n\n## 原始背景与问题\n\n解释已完成结果仍显示恢复标签的原因。\n\n" +
        Array.from(
          { length: 80 },
          (_, index) =>
            `### 证据段 ${index + 1}\n\n保留实际观察、原始问题和未知，读者无需从聊天拼接结论。\n\n`,
        ).join("") +
        "报告末尾：本次只调查，没有修改业务代码。\n\n" +
        `[查看关联任务](/tasks/${results.items![0]!.task_id}?from=${issue.issue_id})\n\n` +
        `[查看关联问题](/issues/${child.issue_id})`;
      const file = {
        file_id: "file-readable-report",
        name: "report.md",
        size: Buffer.byteLength(body),
        sha256: createHash("sha256").update(body).digest("hex"),
      };
      reportDownload = body;
      let originalReads = 0;
      const beforeWrites = writes.length;
      let holdIndex = indexTiming !== "index-first";
      let releaseIndex = () => {};
      const indexGate = new Promise<void>((resolve) => {
        releaseIndex = resolve;
      });
      let returning = false;
      let returnedIndex = Promise.resolve();
      let indexReceived = () => {};
      await page.route(`**/api/v1/issues/${issue.issue_id}/results*`, async (route) => {
        const returningRequest = returning;
        if (holdIndex) await indexGate;
        await route.fulfill({ json: { ...results, more: true, next: "older" } });
        if (returningRequest) indexReceived();
      });
      await page.route(`**/api/v1/issues/${issue.issue_id}`, async (route) => {
        if (returning) {
          // Reconstruct the opposite, equally legal order from the first historical read.
          await returnedIndex;
          await page.evaluate(
            () =>
              new Promise<void>((resolve) =>
                requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
              ),
          );
        }
        await route.fulfill({
          json: { ...issue, attentions: { items: [decision], more: false } },
        });
      });
      let releaseConversation = () => {};
      let conversationGate = new Promise<void>((resolve) => {
        releaseConversation = resolve;
      });
      const conversationID = issue.conversation?.conversation_id;
      assert(conversationID, "the reader fixture needs its actual Issue conversation");
      const conversationPath = `/api/v1/agent-conversations/${encodeURIComponent(conversationID)}`;
      await page.route(
        (url) => url.pathname === conversationPath,
        async (route) => {
          const gate = conversationGate;
          const response = await route.fetch();
          await gate;
          await route.fulfill({ response });
        },
      );
      const finishConversation = async () => {
        const response = page.waitForResponse(
          (reply) => new URL(reply.url()).pathname === conversationPath,
        );
        releaseConversation();
        await response;
        await page.evaluate(
          () =>
            new Promise<void>((resolve) =>
              requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
            ),
        );
      };
      await page.route("**/api/v1/artifacts/artifact-one", (route) =>
        route.fulfill({
          json: {
            ...results.items![0],
            artifact: {
              ...results.items![0]!.artifact,
              ref: { report: { body }, files: [file] },
            },
          },
        }),
      );
      await page.route(`**/api/v1/files/${file.file_id}`, (route) => {
        originalReads++;
        return route.fulfill({ contentType: "application/octet-stream", body });
      });
      await page.route(`**/api/v1/files/${file.file_id}/download-url`, (route) =>
        route.fulfill({ json: { url: `http://127.0.0.1:${address.port}/report-download` } }),
      );
      try {
        await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
        const card = page.locator('[data-artifact="artifact-one"]').first();
        // Historical originals are readable before the independently fetched current index.
        await page.locator('[data-scroll-id="result:artifact-one"]').scrollIntoViewIfNeeded();
        await card.getByRole("heading", { name: "报告摘要", exact: true }).waitFor();
        if (indexTiming === "index-first")
          await card.getByText(results.items![0]!.title, { exact: true }).waitFor();
        await card.getByRole("button", { name: "阅读完整报告 →", exact: true }).click();
        await card.getByRole("heading", { name: "报告正文", exact: true }).waitFor();
        await card.getByText("报告末尾：本次只调查，没有修改业务代码。", { exact: true }).waitFor();
        assert.equal(originalReads, 0, "report metadata must not fetch original files eagerly");
        assert.equal(await card.getByRole("button", { name: "阅读 report.md" }).isVisible(), false);
        await card
          .locator("summary")
          .filter({ hasText: /^原件与证据/ })
          .click();
        const open = card.getByRole("button", { name: "阅读 report.md", exact: true });
        await open.click();
        const reader = page.getByRole("dialog", { name: "阅读 report.md", exact: true });
        await reader.getByRole("heading", { name: "原始背景与问题", exact: true }).waitFor();
        await reader
          .getByText("报告末尾：本次只调查，没有修改业务代码。", { exact: true })
          .waitFor();
        assert.equal(originalReads, 1);
        if (indexTiming === "while-reading") {
          const previousCard = await card.elementHandle();
          const previousOpen = await open.elementHandle();
          holdIndex = false;
          releaseIndex();
          await card.getByText(results.items![0]!.title, { exact: true }).waitFor();
          assert.equal(
            await reader.isVisible(),
            true,
            "index metadata must retain the open reader",
          );
          assert(await card.evaluate((node, previous) => node === previous, previousCard));
          assert(await open.evaluate((node, previous) => node === previous, previousOpen));
          assert.equal(originalReads, 1, "index arrival must not reread immutable original bytes");
        }
        const beforeConversationCard = await card.elementHandle();
        const beforeConversationOpen = await open.elementHandle();
        await finishConversation();
        assert.equal(
          await reader.isVisible(),
          true,
          "conversation readiness must retain the reader",
        );
        assert(await card.evaluate((node, previous) => node === previous, beforeConversationCard));
        assert(await open.evaluate((node, previous) => node === previous, beforeConversationOpen));
        assert.equal(
          originalReads,
          1,
          "conversation readiness must not reread immutable originals",
        );
        assert(await reader.evaluate((node) => node.scrollWidth <= node.clientWidth + 1));
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
        await page.screenshot({
          path: join(output, `readable-report-${width}-${indexTiming}.png`),
        });
        const pendingDownload = page.waitForEvent("download");
        await reader.getByRole("button", { name: "下载 report.md", exact: true }).click();
        const download = await pendingDownload;
        assert.equal(download.suggestedFilename(), file.name);
        const downloaded = join(output, `readable-report-${width}-${indexTiming}.md`);
        await download.saveAs(downloaded);
        assert.equal(await readFile(downloaded, "utf8"), body);
        await page.keyboard.press("Escape");
        await reader.waitFor({ state: "hidden" });
        assert(await open.evaluate((node) => node === document.activeElement));
        for (const target of [
          { label: "查看关联任务", path: `/tasks/${results.items![0]!.task_id}` },
          { label: "查看关联问题", path: `/issues/${child.issue_id}` },
        ]) {
          await open.click();
          await reader.getByRole("link", { name: target.label, exact: true }).click();
          await page.waitForURL((url) => url.pathname === target.path);
          if (target.path.startsWith("/tasks/"))
            await page.getByRole("region", { name: "任务详情", exact: true }).waitFor();
          else await page.getByRole("heading", { name: child.plan.title, exact: true }).waitFor();
          holdIndex = false;
          releaseIndex();
          returnedIndex = new Promise<void>((resolve) => {
            indexReceived = resolve;
          });
          returning = true;
          conversationGate = new Promise<void>((resolve) => {
            releaseConversation = resolve;
          });
          await page.goBack();
          await page.waitForURL((url) => url.pathname === `/issues/${issue.issue_id}`);
          await card.getByText(results.items![0]!.title, { exact: true }).waitFor();
          await open.waitFor();
          const originals = card
            .locator("summary")
            .filter({ hasText: /^原件与证据/ })
            .locator("..");
          assert(await originals.evaluate((node) => (node as HTMLDetailsElement).open));
          await page.waitForFunction(
            () => document.activeElement?.getAttribute("aria-label") === "阅读 report.md",
          );
          assert(await open.evaluate((node) => node === document.activeElement));
          assert.equal(await page.locator("dialog[open]").count(), 0);
          const restoredOpen = await open.elementHandle();
          await finishConversation();
          assert(await open.evaluate((node, previous) => node === previous, restoredOpen));
          assert(await originals.evaluate((node) => (node as HTMLDetailsElement).open));
          assert(await open.evaluate((node) => node === document.activeElement));
        }
        assert.equal(writes.length, beforeWrites);
        assert.deepEqual(errors, []);
        console.log(
          `PASS ${width}/${indexTiming}: Issue activity → same artifact → original bytes/download → Task and Issue return/focus`,
        );
      } finally {
        releaseIndex();
        indexReceived();
        releaseConversation();
        await context.close();
        reportDownload = undefined;
      }
    }
  }
  // Historical activity loads a full artifact, so its collapsed preview must be bounded too.
  for (const width of [1440, 390]) {
    const context = await browser.newContext({
      viewport: { width, height: width > 760 ? 1000 : 844 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    const body =
      "# 历史调查报告\n\n" +
      Array.from(
        { length: 100 },
        (_, index) => `## 历史章节 ${index}\n\n保留完整分析与观察。\n\n`,
      ).join("") +
      "历史报告的全文末尾。";
    await page.route(`**/api/v1/issues/${issue.issue_id}/results`, (route) =>
      route.fulfill({ json: { items: [], more: false } }),
    );
    await page.route("**/api/v1/artifacts/artifact-one", (route) =>
      route.fulfill({
        json: {
          ...results.items![0],
          artifact: { ...results.items![0]!.artifact, ref: { report: { body } } },
        },
      }),
    );
    try {
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      // The historical card loads on intersection; its activity row can be replaced during startup.
      await page.waitForFunction(() => {
        const row = document.querySelector<HTMLElement>('[data-scroll-id="result:artifact-one"]');
        if (!row?.isConnected) return false;
        row.scrollIntoView({ block: "center" });
        return !!row.querySelector('[data-artifact="artifact-one"]');
      });
      const card = page.locator(
        '[data-scroll-id="result:artifact-one"] [data-artifact="artifact-one"]',
      );
      await card.getByRole("heading", { name: "报告摘要", exact: true }).waitFor();
      assert.equal(await card.getByText("历史报告的全文末尾。", { exact: true }).count(), 0);
      const summary = card.getByRole("region", { name: "交付报告", exact: true });
      assert((await summary.textContent())!.length < 1250, "summary contained the full artifact");
      await card.getByRole("button", { name: "阅读完整报告 →", exact: true }).click();
      await card.getByRole("heading", { name: "报告正文", exact: true }).waitFor();
      await card.getByText("历史报告的全文末尾。", { exact: true }).waitFor();
      await card.getByRole("button", { name: "收起报告", exact: true }).click();
      await card.getByRole("heading", { name: "报告摘要", exact: true }).waitFor();
      assert.equal(await card.getByText("历史报告的全文末尾。", { exact: true }).count(), 0);
      assert((await summary.textContent())!.length < 1250);
      assert(await summary.evaluate((node) => node.scrollHeight <= node.clientHeight + 1));
      await page.screenshot({ path: join(output, `historical-report-collapsed-${width}.png`) });
      console.log(
        `PASS ${width}: full historical artifact has an explicit bounded preview before and after expansion`,
      );
    } finally {
      await context.close();
    }
  }
  // D186: policy editing preserves the agreement; shared review is candidate-bound.
  for (const width of [1440, 390]) {
    const originalPlan = structuredClone(issue.plan);
    const originalRevision = issue.revision;
    const originalReviews = sharedReviews;
    const context = await browser.newContext({
      viewport: { width, height: 1000 },
      isMobile: width < 760,
      hasTouch: width < 760,
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      // This warning is produced by this context's explicit fixture isolation.
      if (
        message.type() === "warning" &&
        message.text() === "Service Worker registration blocked by Playwright"
      )
        return;
      if (message.type() === "error" || message.type() === "warning") errors.push(message.text());
    });
    try {
      issue.plan = {
        ...originalPlan,
        background: [
          { kind: "user_constraint", source: "owner", text: "保留原约束和任务冻结身份" },
        ],
        deliveries: [
          {
            delivery_id: "external-one",
            provider: "github",
            repository: "sample/repo",
            number: 42,
            url: "https://example.invalid/sample/repo/pull/42",
          },
        ],
        pr_policy: { mode: "manual", repository: "sample/repo", reason: "原版本要求明确人工批准" },
      };
      sharedReviews = { items: [], more: false };
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      await detail.getByRole("heading", { name: issue.plan.title, exact: true }).waitFor();
      assert.equal(new URL(page.url()).pathname, `/issues/${issue.issue_id}`);
      assert(await page.title());
      await openIssueDefinition(page);
      await detail.getByRole("heading", { name: "PR 合入授权", exact: true }).waitFor();
      await detail.getByText("合入前需要人工审批 · sample/repo", { exact: true }).waitFor();
      for (const mode of ["auto", "manual"] as const) {
        const baseline = structuredClone(issue.plan);
        const revision = issue.revision;
        const beforeWrites = writes.length;
        const editor = await openIssueAux(page, "编辑定义");
        await editor.waitFor({ state: "visible" });
        await editor.getByRole("combobox", { name: /^合入方式/ }).selectOption(mode);
        await editor.getByLabel("授权仓库", { exact: true }).fill("sample/repo");
        const reason =
          mode === "auto" ? "审阅和检查通过后按原责任自动合入" : "保留当前候选，合入前明确人工批准";
        await editor.getByRole("textbox", { name: /^授权说明/ }).fill(reason);
        await page.screenshot({ path: join(output, `pr-policy-${mode}-${width}.png`) });
        const response = page.waitForResponse(
          (value) => value.url().endsWith("/api/v1/ops") && value.request().method() === "POST",
        );
        await editor.getByRole("button", { name: "保存定义", exact: true }).click();
        await response;
        await editor.waitFor({ state: "hidden" });
        const submitted = writes.slice(beforeWrites);
        assert.equal(submitted.length, 1, "policy editing must submit one owner command");
        const envelope = submitted[0]!;
        assert.equal(envelope.operation, "issues.update");
        assert.equal(envelope.target.id, issue.issue_id);
        assert.equal(envelope.expected_control_revision, revision);
        assert(envelope.request_id, "saved policy lacks its request identity");
        const payload = envelope.payload as UpdateIssuePayload;
        assert.equal(payload.expected_assignment_epoch, issue.assignment_epoch);
        assert.equal(
          "task_dispositions" in payload,
          false,
          "policy editing silently altered task applicability",
        );
        assert.deepEqual(payload.plan, {
          ...baseline,
          pr_policy: { mode, repository: "sample/repo", reason },
        });
        assert.equal(issue.revision, revision + 1);
        await openIssueDefinition(page);
        await detail.getByText(reason, { exact: true }).waitFor();
      }
      await openIssueAux(page, "外部交付");
      const external = page.getByRole("dialog", { name: "跟进与外部交付", exact: true });
      const reviews = external.getByRole("region", { name: "共享 PR 审阅", exact: true });
      await reviews.getByText("暂无共享 PR 审阅记录。", { exact: true }).waitFor();
      sharedReviews = {
        more: false,
        items: [
          {
            review_id: "review-shared-one",
            provider: "github",
            repository: "sample/repo",
            number: 42,
            revision: 3,
            status: "passed",
            valid: false,
            invalid_reason: "requirements changed",
            reason: "原审阅结论保留，当前需求变更仍需负责人核对。",
            head_sha: "a".repeat(40),
            base_sha: "b".repeat(40),
            requirements_digest: "c".repeat(64),
            links: [
              {
                issue_id: issue.issue_id,
                delivery_id: "external-one",
                task_id: results.items![0]!.task_id,
              },
            ],
            rounds: [
              {
                number: 1,
                task_id: results.items![0]!.task_id,
                spec_version: 1,
                head_sha: "a".repeat(40),
                base_sha: "b".repeat(40),
                requirements_digest: "c".repeat(64),
              },
            ],
          },
        ],
      };
      publish();
      await reviews.getByText("原审阅结论待核对当前适用性", { exact: true }).waitFor();
      assert.equal(await reviews.getByText("审阅通过", { exact: true }).count(), 0);
      await reviews.getByText("查看审阅过程与关联", { exact: true }).click();
      await reviews.getByRole("button", { name: "查看关联 Issue", exact: true }).waitFor();
      const beforeReviewReads = writes.length;
      await page.screenshot({ path: join(output, `shared-review-invalid-${width}.png`) });
      assert(await external.evaluate((node) => node.scrollWidth <= node.clientWidth + 1));
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      await reviews.getByRole("button", { name: "查看实际审阅任务", exact: true }).click();
      const task = page.getByRole("region", { name: "任务详情", exact: true });
      await task.getByText("执行任务 A", { exact: true }).waitFor();
      assert(requests.some((url) => url === `/api/v1/tasks/${results.items![0]!.task_id}`));
      assert.equal(
        writes.length,
        beforeReviewReads,
        "reading a review association mutated responsibility",
      );
      assert.deepEqual(errors, []);
      console.log(
        `PASS delivery policy/review ${width}: original plan/CAS preserved, no implicit task disposition, invalid passed result and actual task link`,
      );
    } catch (error) {
      await page.screenshot({ path: join(output, `delivery-maintenance-failure-${width}.png`) });
      throw error;
    } finally {
      await context.close();
      issue.plan = originalPlan;
      issue.revision = originalRevision;
      sharedReviews = originalReviews;
    }
  }
  // S11 hierarchy uses the same real read, pagination and tool controls as other scenarios.
  for (const width of [1440, 390]) {
    const touch = width === 390;
    const original = structuredClone(activity.items);
    const originalPlan = { ...issue.plan };
    const originalProgress = issue.progress!.summary;
    const context = await browser.newContext({
      viewport: { width, height: 844 },
      hasTouch: touch,
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    try {
      issue.plan.title = "创建 多轮故事：手机布局从需求澄清到独立验收";
      issue.plan.intent = "使用独立沙盒数据验证真实界面与交互";
      issue.progress!.summary =
        "已核对最新要求：手机布局、长标题、草稿和返回焦点均已完成这轮核对，保留失败与重试历史。";
      await page.route("**/api/v1/issues?*", async (route) => {
        const url = new URL(route.request().url());
        if (url.searchParams.has("parent_id"))
          await route.fulfill({ json: { items: [], more: false } });
        else await route.continue();
      });
      const block = activity.items![0]!.block!;
      block.items![0]!.associations = {
        message_id: block.items![0]!.message_id,
        facts_reviewed: true,
        owner_locked: false,
        revision: 1,
        content_revision: 1,
        disposition: "linked",
        items: [
          {
            issue_id: issue.issue_id,
            title: "当前 Issue 自引用",
            source: "owner",
            owner_locked: false,
          },
        ],
      };
      block.items!.push(
        ...Array.from({ length: 14 }, (_, index) => ({
          message_id: `hierarchy-tool-${index}`,
          seq: index + 3,
          kind: "message",
          author: "main_agent",
          reply_to: block.trigger.message_id,
          created_at: new Date(now).toISOString(),
          tool: {
            name: "issues_read",
            status: "completed" as const,
            turn_id: "hierarchy",
            updated_at: new Date(now).toISOString(),
            input: "原始参数",
            output: "原始输出",
          },
        })),
      );
      activity.items!.push(
        {
          id: "main-admits-issue",
          position: 996,
          kind: "issue.status_changed",
          created_at: now - 4,
          author: "主 Agent",
          title: "将工作状态从「待安排」移到「待推进」",
          issue_id: issue.issue_id,
          admission: true,
        },
        {
          id: "pine-claims-issue",
          position: 997,
          kind: "issue.manager_claimed",
          created_at: now - 3,
          author: "Pine",
          title: "Pine 已接手",
          issue_id: issue.issue_id,
        },
        {
          id: "manager-start-failed",
          position: 998,
          kind: "run.failed",
          created_at: now - 2,
          author: "主 Agent",
          title: "启动失败",
          issue_id: issue.issue_id,
        },
        {
          id: "manager-reassigned",
          position: 999,
          kind: "issue.assigned",
          created_at: now - 1,
          author: "主 Agent",
          title: "变更了 Issue 负责人",
          issue_id: issue.issue_id,
        },
      );
      activity.items!.push(
        ...["更新了当前进展", "更新了工作状态"].map((title, index) => ({
          id: `hierarchy-event-${index}`,
          position: 1003 + index,
          kind: index ? "issue.status_changed" : "issue.progress_updated",
          created_at: now + index,
          author: "青竹",
          title,
          issue_id: issue.issue_id,
        })),
      );
      activity.items!.unshift({
        id: "hierarchy-background",
        position: 1000,
        kind: "conversation",
        created_at: now - 1,
        author: "tend",
        block: {
          position: 0,
          more: false,
          trigger: {
            message_id: "hierarchy-background-source",
            seq: 0,
            kind: "message",
            author: "system",
            conversation_id: issue.conversation!.conversation_id!,
            notice_type: "background_batch",
            body: "后台来源内容保持可读取。",
            created_at: new Date(now - 1).toISOString(),
            background: {
              batch_id: "hierarchy-batch",
              message_id: "hierarchy-background-source",
              status: "frozen",
              source_count: 3,
              created_at: now - 1,
              presentation: {
                recipient: {
                  identity: { execution_environment_id: "hosted", local_id: "qingsong" },
                  display_name: "青松",
                  status: "starting",
                },
                sources: [
                  {
                    seq: 1,
                    message_id: "hierarchy-original-one",
                    kind: "issue.status_changed",
                    summary: "后台来源内容保持可读取。",
                    object_type: "issue",
                    object_id: child.issue_id,
                  },
                ],
                more: true,
              },
            },
          },
          items: [],
        },
      });
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      assert.equal(new URL(page.url()).pathname, `/issues/${issue.issue_id}`);
      const admission = detail.locator('[data-scroll-id="main-admits-issue"]');
      const transition = admission.getByText("将工作状态从「待安排」移到「待推进」", {
        exact: true,
      });
      await detail.getByText("Pine 已接手", { exact: true }).waitFor();
      await detail.getByText("启动失败", { exact: true }).waitFor();
      await detail.getByText("变更了 Issue 负责人", { exact: true }).waitFor();
      assert.equal(await admission.getAttribute("open"), null);
      assert.equal(await transition.isVisible(), false);
      assert.equal(await admission.getByText("主 Agent", { exact: true }).isVisible(), false);
      const admissionSummary = admission.locator("summary", { hasText: "接收与投递记录" });
      if (touch) await admissionSummary.tap();
      else await admissionSummary.press("Enter");
      await transition.waitFor();
      await admission.getByText("主 Agent", { exact: true }).waitFor();
      await admission.screenshot({ path: join(output, `admission-expanded-${width}.png`) });
      await admissionSummary.click();
      console.log(`PASS admission, claim and failure records ${width}`);
      const trigger = detail.locator("[data-trace-trigger]");
      await trigger.waitFor();
      assert.equal(await trigger.count(), 1, "single status-row trace trigger");
      await detail.locator("[data-issue-scroll]").evaluate((node) => (node.scrollTop = 0));
      const heading = await detail
        .getByRole("heading", { name: "讨论与进展", exact: true })
        .boundingBox();
      assert(
        heading && heading.y + heading.height < 844,
        "story discussion starts in first viewport",
      );
      assert.equal(
        await detail.getByRole("button", { name: "有新进展 · 到最新 ↓", exact: true }).count(),
        0,
      );
      assert.equal(
        await detail.getByRole("button", { name: "查看调度与事件诊断 →", exact: true }).count(),
        0,
        "diagnostics does not occupy a standalone page header",
      );
      const source = detail.locator('[data-scroll-id="hierarchy-background-source"]');
      let sourceReads = 0;
      await page.route(
        "**/api/v1/background/hierarchy-background-source/sources?*",
        async (route) => {
          sourceReads++;
          if (sourceReads === 1) {
            await route.fulfill({ status: 503, json: { error: { code: "unavailable" } } });
            return;
          }
          const after = new URL(route.request().url()).searchParams.get("after");
          await route.fulfill({
            json: {
              batch: { batch_id: "hierarchy-batch" },
              sources: [
                {
                  seq: after === "2" ? 3 : 2,
                  message_id: after === "2" ? "third-original" : "second-original",
                  kind: after === "2" ? "task.completed" : "issue.status_changed",
                  body: after === "2" ? "第三项任务完成原文。" : "工作状态调整的完整原文。",
                },
              ],
              more: after !== "2",
              next: after === "2" ? undefined : 2,
            },
          });
        },
      );
      const owner = detail.locator('[data-scroll-id="owner-one"]');
      const reply = detail.locator('[data-scroll-id="reply-one"]');
      const discussion = owner.locator("xpath=ancestor::article[1]");
      await discussion.getByRole("button", { name: /回复你的消息 #/ }).click();
      assert(await owner.evaluate((node) => document.activeElement?.contains(node)));
      let receiptReads = 0;
      await page.route("**/api/v1/messaging/messages/owner-one/receipts", async (route) => {
        receiptReads++;
        await route.fulfill({
          json: {
            reads: [
              {
                message_id: "owner-one",
                content_revision: 1,
                reader: "agent:issue:fixture",
                endpoint: "agent",
                evidence: "native_input:fixture",
                read_at: now,
              },
            ],
            deliveries: [],
            user_read_capability: "explicit_web_only",
          },
        });
      });
      const receipts = discussion
        .locator("details")
        .filter({ has: page.locator("summary", { hasText: "接收与投递记录" }) });
      assert.equal(receiptReads, 0);
      await receipts.locator("summary").click();
      await receipts.getByText(/Agent 已接收原生输入/).waitFor();
      await receipts.getByText("暂无外部渠道投递记录。", { exact: true }).waitFor();
      await receipts.locator("summary").click();
      assert.equal(receiptReads, 1);
      const alignment = await detail.evaluate((node) => {
        const x = (selector: string) => node.querySelector(selector)!.getBoundingClientRect().left;
        return {
          source: x('[data-scroll-id="hierarchy-background-source"] summary > span:first-child'),
          owner: x('[data-scroll-id="owner-one"]'),
          reply: x('[data-scroll-id="reply-one"]'),
          event: x("[data-event-group] summary > span:first-child"),
          icon: x('[data-scroll-id="conversation-one"] [aria-hidden="true"]'),
        };
      });
      assert(Math.abs(alignment.owner - heading!.x) < 1, "owner body has no history indent");
      assert(Math.abs(alignment.reply - alignment.owner) < 1, "agent body has no history indent");
      assert(Math.abs(alignment.icon - alignment.owner) < 1, "avatar stays inside content lane");
      assert(Math.abs(alignment.source - alignment.event) < 1, "source and event text align");
      assert(Math.abs(alignment.source - alignment.owner) < 1, "background text aligns with prose");
      assert(Math.abs(alignment.event - alignment.reply) < 1, "event text aligns with agent prose");
      assert.equal(
        await detail.getByRole("button", { name: "查看更早记录", exact: true }).count(),
        0,
      );
      assert.equal(await source.getAttribute("open"), null);
      assert((await source.locator("summary").first().innerText()).includes("青松 · 3 项后台变化"));
      assert.equal(sourceReads, 0, "collapsed notifications do not fetch originals");
      await source.locator("summary").first().click();
      await source.getByText("后台来源内容保持可读取。", { exact: true }).waitFor();
      await source.getByText("已安排，等待接收确认", { exact: true }).waitFor();
      await source.getByRole("button", { name: "重新读取", exact: true }).click();
      await source.getByRole("button", { name: "查看更多原始来源", exact: true }).click();
      await source.getByText("任务完成 · 原始来源 3", { exact: true }).click();
      await source.getByText("第三项任务完成原文。", { exact: true }).waitFor();
      assert.equal(sourceReads, 3, "retry and pagination use their original source cursor");
      assert.equal(
        await source.getByRole("link", { name: "查看 Issue", exact: true }).getAttribute("href"),
        `/issues/${child.issue_id}`,
      );
      assert.equal(await source.evaluate((node) => node.scrollWidth > node.clientWidth), false);
      await page.screenshot({ path: join(output, `named-background-${width}.png`) });
      await source.locator("summary").first().click();
      assert.equal(await source.getAttribute("open"), null);
      // A pending batch preserves its message key while sources join or cancel.
      // Exercise the complete page refresh without remounting that disclosure.
      const pendingMessage = "hierarchy-pending-source";
      const original = (seq: number, cancelled = false) => ({
        seq,
        message_id: `pending-original-${seq}`,
        kind: "task.completed",
        body: `Pending original ${seq}`,
        cancelled_at: cancelled ? 1 : 0,
      });
      let pendingOriginals = [original(1)];
      let pendingReads = 0;
      const pendingItem = {
        ...activity.items![0]!,
        id: "hierarchy-pending",
        position: 1001,
        block: {
          ...activity.items![0]!.block!,
          position: 1001,
          trigger: {
            ...activity.items![0]!.block!.trigger,
            message_id: pendingMessage,
            seq: 1001,
            background: {
              batch_id: "pending-batch",
              message_id: pendingMessage,
              status: "pending",
              source_count: 1,
              created_at: now,
              presentation: {
                last_source_seq: 1,
                sources: [] as BackgroundSourcePreview[],
                more: false,
              },
            },
          },
        },
      };
      await page.route(`**/api/v1/background/${pendingMessage}/sources?*`, async (route) => {
        pendingReads++;
        await route.fulfill({
          json: {
            batch: pendingItem.block.trigger.background,
            sources: pendingOriginals,
            more: false,
          },
        });
      });
      activity.items!.unshift(pendingItem);
      publish();
      const pendingNotice = detail.locator(`[data-scroll-id="${pendingMessage}"]`);
      await pendingNotice.waitFor();
      assert.equal(pendingReads, 0, "pending disclosure remains lazy while collapsed");
      await pendingNotice.locator("summary").first().click();
      await pendingNotice.getByText("任务完成 · 原始来源 1", { exact: true }).waitFor();
      await pendingNotice.evaluate((node) => node.setAttribute("data-kept-instance", "same"));
      pendingOriginals = [...pendingOriginals, original(2)];
      pendingItem.block.trigger.background.source_count = 2;
      pendingItem.block.trigger.background.presentation.last_source_seq = 2;
      publish();
      await pendingNotice.getByText("任务完成 · 原始来源 2", { exact: true }).waitFor();
      assert.equal(
        await pendingNotice.getAttribute("data-kept-instance"),
        "same",
        "new sources do not remount the original disclosure",
      );
      assert.equal(pendingReads, 2, "open batch refreshes its no-more cached page once");
      await pendingNotice.locator("summary").first().click();
      pendingOriginals = [original(1, true), original(2), original(3)];
      // Cancel plus append keeps the active count equal; the retained seq changes.
      pendingItem.block.trigger.background.presentation.last_source_seq = 3;
      pendingItem.block.trigger.background.presentation.sources = [
        {
          seq: 3,
          message_id: "pending-original-3",
          kind: "task.completed",
          summary: "最新来源等待展开",
        },
      ];
      publish();
      await pendingNotice
        .getByText("最新来源等待展开", { exact: true })
        .waitFor({ state: "attached" });
      assert.equal(pendingReads, 2, "collapsed metadata updates do not read originals");
      await pendingNotice.locator("summary").first().click();
      await pendingNotice.getByText("任务完成 · 原始来源 3", { exact: true }).waitFor();
      await pendingNotice.getByText("任务完成 · 已撤销 · 原始来源 1", { exact: true }).waitFor();
      assert.equal(pendingReads, 3, "reopening verifies additions and cancellation facts");
      await pendingNotice.locator("summary").first().click();
      activity.items!.shift();
      publish();
      console.log(`PASS pending background source refresh ${width}`);
      await owner.scrollIntoViewIfNeeded();
      assert(Math.abs((await owner.boundingBox())!.x - (await reply.boundingBox())!.x) < 1);
      await detail.locator("[data-issue-scroll]").evaluate((node) => (node.scrollTop = 0));
      await page.screenshot({ path: join(output, `issue-alignment-${width}.png`) });
      assert.equal(await trigger.getAttribute("aria-expanded"), "false");
      assert.equal(await detail.locator("[data-tool-item]:visible").count(), 0);
      assert.equal(await reply.getByText("当前 Issue 自引用").count(), 0);
      const tags = detail.locator('[data-issue-context="true"]').first();
      const tagBounds = await tags.boundingBox();
      const associationBounds = await tags
        .getByRole("button", { name: "消息操作", exact: true })
        .boundingBox();
      assert(tagBounds && associationBounds);
      assert(
        associationBounds.y + associationBounds.height <= tagBounds.y + tagBounds.height + 1,
        "association control stays in flow after filtering the current Issue",
      );
      assert.equal(await detail.getByRole("region", { name: "交付物", exact: true }).count(), 0);
      await trigger.click();
      assert.equal(await detail.locator("[data-tool-item]:visible").count(), 12);
      assert.equal(await detail.getByRole("button", { name: /使用了 .* 个工具/ }).count(), 0);
      await detail.getByRole("button", { name: "显示更早 2 步", exact: true }).click();
      assert.equal(await detail.locator("[data-tool-item]:visible").count(), 14);
      await detail.locator('[data-tool-item="hierarchy-tool-0"] button').click();
      await detail.getByText("原始输出", { exact: true }).waitFor();
      await trigger.click();
      assert.equal(await detail.locator("[data-tool-item]:visible").count(), 0);
      assert(await trigger.evaluate((node) => document.activeElement === node));
      const savedTools = [...block.items!];
      block.items = block.items!.slice(0, 13);
      publish();
      await trigger.getByText("查看过程 · 12 步", { exact: false }).waitFor();
      await trigger.click();
      const firstTool = detail.locator('[data-tool-item="hierarchy-tool-0"] button');
      if ((await firstTool.getAttribute("aria-expanded")) !== "true") await firstTool.click();
      await firstTool.focus();
      block.items.push({ ...savedTools[13]!, message_id: "hierarchy-appended-tool", seq: 16 });
      publish();
      await detail.locator('[data-tool-item="hierarchy-appended-tool"]').waitFor();
      assert(await firstTool.isVisible(), "open first tool remains visible after thirteenth tool");
      assert.equal(await firstTool.getAttribute("aria-expanded"), "true");
      assert(await firstTool.evaluate((node) => document.activeElement === node));
      await trigger.click();
      block.items = savedTools;
      publish();
      // D246: a tool-free historical turn still explains its public process.
      block.items = savedTools.filter((item) => !item.tool);
      publish();
      await trigger.getByText("查看过程", { exact: false }).waitFor();
      await trigger.click();
      await detail
        .getByText("这段历史记录没有保存可查看的执行步骤；已保存的公开回复显示在下方。", {
          exact: true,
        })
        .waitFor();
      await page.screenshot({ path: join(output, `public-process-empty-${width}.png`) });
      await trigger.click();
      const publicReplies = block.items;
      const previousState = block.trigger.state;
      block.items = [];
      block.trigger.state = "processing";
      publish();
      await detail.getByRole("status").filter({ hasText: "处理中" }).waitFor();
      const emptyProcess = detail.locator("[data-trace-trigger]");
      await emptyProcess.click();
      await detail
        .getByText("尚未记录可查看的公开执行步骤，公开回复会显示在下方。", { exact: true })
        .waitFor();
      await page.screenshot({ path: join(output, `public-process-active-${width}.png`) });
      // Same trigger: preserve the user's open disclosure and focus as real items arrive.
      await emptyProcess.focus();
      await emptyProcess.evaluate((node) => node.setAttribute("data-original-process", "true"));
      const streamingTool = {
        ...savedTools.find((item) => item.tool)!,
        message_id: "first-live-tool",
        seq: 2,
        tool: { ...savedTools.find((item) => item.tool)!.tool!, status: "running" as const },
      };
      // Native-producer regressions prove this projection is populated by real
      // events. This browser case checks its rendered, still-unpublished shape.
      block.process = { items: [streamingTool], more: false, started_at: now - 5000 };
      assert.equal(block.items.length, 0);
      publish();
      const liveTool = detail.locator('[data-tool-item="first-live-tool"]');
      await liveTool.waitFor();
      assert.equal(await emptyProcess.getAttribute("data-original-process"), "true");
      assert.equal(await emptyProcess.getAttribute("aria-expanded"), "true");
      assert(await emptyProcess.evaluate((node) => document.activeElement === node));
      assert(await liveTool.isVisible(), "first running tool becomes visible without reopening");
      block.items.push({
        message_id: "first-public-reply",
        seq: 3,
        author: "main_agent",
        kind: "message",
        reply_to: block.trigger.message_id,
        created_at: block.trigger.created_at,
        body: "同一回合的公开进展。",
        assistant_state: "streaming",
        assistant_revision: 1,
      });
      publish();
      await detail.getByText("同一回合的公开进展。", { exact: true }).waitFor();
      assert.equal(await emptyProcess.getAttribute("data-original-process"), "true");
      assert.equal(await emptyProcess.getAttribute("aria-expanded"), "true");
      assert(await liveTool.isVisible());
      await page.screenshot({ path: join(output, `process-first-live-item-${width}.png`) });
      block.process = { items: [], more: false, error: "公开过程读取失败，已保存的讨论仍可阅读。" };
      publish();
      await detail
        .getByText("公开过程读取失败，已保存的讨论仍可阅读。", { exact: false })
        .waitFor();
      assert(await detail.getByText("同一回合的公开进展。", { exact: true }).isVisible());
      await detail
        .getByText("公开过程暂不可读取，请使用上方的重读入口。", { exact: true })
        .waitFor();
      assert.equal(
        await detail
          .getByText("尚未记录可查看的公开执行步骤，公开回复会显示在下方。", { exact: true })
          .count(),
        0,
      );
      block.process = { items: [streamingTool], more: false, started_at: now - 5000 };
      await detail.getByRole("button", { name: "重新读取公开过程", exact: true }).click();
      await liveTool.waitFor();
      await detail
        .getByRole("button", { name: "重新读取公开过程", exact: true })
        .waitFor({ state: "hidden" });
      console.log(`PASS continuous empty → running tool → public reply ${width}`);
      await emptyProcess.click();
      const crossReply: IssueConversationItem = {
        ...publicReplies![0]!,
        message_id: "reply-cross-issue",
        seq: 99,
        body: "第二条普通回复，关联了另一条 Issue。",
        associations: {
          ...publicReplies![0]!.associations!,
          message_id: "reply-cross-issue",
          items: [
            {
              issue_id: child.issue_id,
              title: child.plan.title,
              source: "owner",
              owner_locked: false,
            },
          ],
        },
      };
      block.items = [...publicReplies!, crossReply];
      delete block.process;
      if (previousState === undefined) delete block.trigger.state;
      else block.trigger.state = previousState;
      publish();
      const associationItem = block.items![0]!;
      const crossRow = detail.locator('[data-scroll-id="reply-cross-issue"]');
      await crossRow.getByText(crossReply.body!, { exact: true }).waitFor();
      const link = crossRow.getByRole("link", { name: child.plan.title, exact: true });
      assert.equal(await link.getAttribute("href"), `/issues/${child.issue_id}`);
      for (const row of [reply, crossRow]) {
        assert.equal(
          await row.getByRole("button", { name: "修改 Issue 关联", exact: true }).count(),
          0,
        );
        const trigger = row.getByRole("button", { name: "消息操作", exact: true });
        assert.equal(await trigger.getAttribute("aria-haspopup"), "menu");
      }
      let associationReads = 0;
      await page.route("**/api/v1/messages/*/associations?*", (route) => {
        associationReads++;
        return route.fulfill({
          json:
            associationReads === 1
              ? associationItem.associations
              : {
                  ...associationItem.associations,
                  disposition: "unassigned",
                  items: [],
                  revision: 2,
                  owner_locked: true,
                },
        });
      });
      const associationControl = reply.getByRole("button", { name: "消息操作", exact: true });
      await associationControl.scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(output, `association-trigger-${width}.png`) });
      if (touch) await associationControl.tap();
      else await associationControl.press("ArrowDown");
      const menuEntry = reply.getByRole("menuitem", { name: "修改 Issue 关联", exact: true });
      await menuEntry.waitFor();
      if (touch) await menuEntry.tap();
      else {
        assert(await menuEntry.evaluate((node) => node === document.activeElement));
        await page.keyboard.press("Enter");
      }
      const associationDialog = page.getByRole("dialog", { name: "修改 Issue 关联", exact: true });
      await associationDialog.getByText(/你保存的归属会锁定/).waitFor();
      await associationDialog.getByRole("checkbox", { name: "明确不属于任何 Issue" }).check();
      await page.screenshot({ path: join(output, `message-association-${width}.png`) });
      await associationDialog.getByRole("button", { name: "保存关联", exact: true }).click();
      await associationDialog.waitFor({ state: "hidden" });
      const correction = writes.findLast(
        (write) => write.operation === "conversation.set_issue_associations",
      );
      assert(correction);
      assert.equal(correction.target.id, associationItem.message_id);
      assert.equal(correction.expected_control_revision, 1);
      assert.equal((correction.payload as { disposition: string }).disposition, "unassigned");
      assert.equal(associationReads, 2, "saving refreshes the recorded associations");
      await page.unroute("**/api/v1/messages/*/associations?*");
      if (touch) await link.tap();
      else await link.press("Enter");
      await page.waitForURL((url) => url.pathname === `/issues/${child.issue_id}`);
      await page.getByRole("heading", { name: child.plan.title, exact: true }).waitFor();
      await page.goBack();
      await detail.getByText("Pine 已接手", { exact: true }).waitFor();
      block.items = savedTools;
      publish();
      console.log(`PASS public process and explicit association ${width}`);
      const events = detail.locator("[data-event-group]");
      assert.equal(await events.count(), 1);
      const scrollIds = await detail
        .locator("[data-scroll-id]")
        .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-scroll-id")));
      assert.equal(
        new Set(scrollIds).size,
        scrollIds.length,
        "merged events keep unique scroll identities",
      );
      assert.equal(await events.locator("button:visible").count(), 0);
      await events.locator("summary").click();
      assert.equal(await events.getByText("更新了当前进展", { exact: true }).count(), 1);
      await events.getByRole("button", { name: "查看 →", exact: true }).waitFor();
      const eventSummary = events.locator("summary");
      await eventSummary.focus();
      activity.items!.push({
        ...activity.items!.at(-1)!,
        id: "hierarchy-event-appended",
        position: 1005,
        title: "追加进展",
      });
      publish();
      await events.getByText("追加进展", { exact: true }).waitFor();
      assert.equal(await events.getAttribute("open"), "", "live append keeps merged events open");
      assert(
        await eventSummary.evaluate((node) => document.activeElement === node),
        "live append keeps summary focus",
      );
      activity.items!.splice(activity.items!.length - 3, 0, {
        ...activity.items!.at(-1)!,
        id: "hierarchy-event-earlier",
        created_at: now - 1,
        position: 1002.5,
        title: "前插进展",
      });
      publish();
      await events.getByText("前插进展", { exact: true }).waitFor();
      assert.equal(await events.getAttribute("open"), "", "earlier event keeps merged group open");
      assert(await eventSummary.evaluate((node) => document.activeElement === node));
      activity.items!.push({
        id: "hierarchy-singleton",
        position: 1006,
        kind: "issue.updated",
        created_at: now + 2,
        author: "实时事件作者",
        title: "单条事件",
        issue_id: issue.issue_id,
      });
      publish();
      const singleton = detail.locator('[data-scroll-id="hierarchy-singleton"]');
      await singleton.getByRole("button", { name: "查看 →", exact: true }).focus();
      activity.items!.push({
        ...activity.items!.at(-1)!,
        id: "hierarchy-singleton-appended",
        position: 1007,
        title: "实时成组",
      });
      publish();
      const groupedSummary = singleton.locator("summary");
      await groupedSummary.waitFor();
      assert(
        await groupedSummary.evaluate((node) => document.activeElement === node),
        "singleton focus moves to the new event group summary",
      );
      activity.items!.splice(activity.items!.length - 1, 0, {
        id: "hierarchy-split-event",
        position: 1006.5,
        kind: "issue.updated",
        created_at: now + 2,
        author: "后补事件作者",
        title: "后补事件拆开原分组",
        issue_id: issue.issue_id,
      });
      publish();
      const restoredEvent = singleton.getByRole("button", { name: "查看 →", exact: true });
      await restoredEvent.waitFor();
      assert(
        await restoredEvent.evaluate((node) => document.activeElement === node),
        "split group focus moves back to its visible event control",
      );
      activity.items!.push(
        ...[0, 2].map((offset) => ({
          id: `hierarchy-progress-${offset}`,
          position: 1008 + offset,
          kind: "issue.progress_updated",
          created_at: now + 3 + offset,
          author: "纯进展作者",
          title: `纯进展 ${offset}`,
          issue_id: issue.issue_id,
        })),
      );
      publish();
      const progressGroup = detail.locator('[data-scroll-id="hierarchy-progress-0"]');
      await progressGroup.locator("summary").focus();
      activity.items!.splice(activity.items!.length - 1, 0, {
        id: "hierarchy-progress-interleaved",
        position: 1009,
        kind: "issue.updated",
        created_at: now + 4,
        author: "后补事件作者",
        title: "后补活动拆开纯进展组",
      });
      publish();
      await progressGroup.locator("summary").waitFor({ state: "detached" });
      assert(
        await progressGroup.evaluate((node) => document.activeElement === node),
        "progress-only group restores focus to its standalone event row",
      );
      let earlierStage = 0;
      let boundaryEnabled = false;
      let releaseSecondHistory = () => {};
      const secondHistory = new Promise<void>((resolve) => {
        releaseSecondHistory = resolve;
      });
      const boundaryEvents = [50, 60, 130, 150].map((second) => ({
        id: `boundary-${second}`,
        position: 2000 + second,
        kind: "issue.progress_updated",
        author: "青竹",
        issue_id: issue.issue_id,
        title: `边界进展 ${second}`,
        created_at: now + second * 1000,
      }));
      await page.route("**/api/v1/issues/*/activity?*", async (route) => {
        const cursor = new URL(route.request().url()).searchParams.get("before");
        if (cursor === "2000") await secondHistory;
        if (cursor) earlierStage = cursor === "2050" ? 1 : 2;
        const rows = [...boundaryEvents];
        if (earlierStage >= 1)
          rows.unshift({
            ...boundaryEvents[0]!,
            id: "boundary-0",
            position: 2000,
            title: "边界进展 0",
            created_at: now,
          });
        if (earlierStage >= 2)
          rows.unshift({
            ...boundaryEvents[0]!,
            id: "boundary-negative",
            position: 1950,
            title: "边界进展 更早",
            created_at: now - 50000,
          });
        await route.fulfill({
          json: {
            event_seq: seq,
            items: rows,
            more: boundaryEnabled && earlierStage < 2,
            next_before: earlierStage ? 2000 : 2050,
          },
        });
      });
      await page.reload();
      await detail.getByRole("textbox", { name: "消息", exact: true }).waitFor();
      boundaryEnabled = true;
      publish();
      await detail.locator("[data-history-boundary]").waitFor({ state: "attached" });
      await detail.locator("[data-issue-scroll]").evaluate((node) => {
        node.querySelector("[data-history-boundary]")?.scrollIntoView({ block: "start" });
      });
      for (const count of [5, 6]) {
        if (count === 6) releaseSecondHistory();
        await events
          .locator("summary")
          .filter({ hasText: count === 5 ? "边界进展 0" : "边界进展 更早" })
          .waitFor();
        assert.equal(await events.count(), 1, "earlier pages do not split gap-based event group");
        const ids = await detail
          .locator('[data-scroll-id^="boundary-"]')
          .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-scroll-id")));
        assert.equal(ids.length, count, "each earlier event rendered once");
        assert.equal(
          new Set(ids).size,
          count,
          "successive earlier loads keep unique scroll identities",
        );
      }
      const more = detail.getByRole("button", { name: "更多 Issue 操作", exact: true });
      await more.click();
      const menu = detail.getByRole("menu", { name: "更多 Issue 操作", exact: true });
      await menu.waitFor();
      assert.equal(await menu.getByRole("menuitem").count(), 4);
      assert.equal(await menu.getByRole("separator").count(), 2);
      assert.equal(
        await menu.getByRole("button").count(),
        0,
        "menu entries are actions, not bordered buttons",
      );
      assert(
        await menu
          .getByRole("menuitem", { name: "编辑定义", exact: true })
          .evaluate((node) => document.activeElement === node),
      );
      await page.keyboard.press("ArrowDown");
      assert(
        await menu
          .getByRole("menuitem", { name: "新增子 Issue", exact: true })
          .evaluate((node) => document.activeElement === node),
      );
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "hidden" });
      assert(await more.evaluate((node) => document.activeElement === node));
      await more.click();
      await detail.getByRole("heading", { name: "讨论与进展", exact: true }).click();
      await menu.waitFor({ state: "hidden" });
      const longName = "这是一个有较长显示名称的负责人用于核对手机端属性布局是否仍可正常阅读";
      await page.route("**/api/v1/hosted-agents", async (route) => {
        await route.fulfill({
          json: {
            configured: true,
            projection: {
              agents: [
                {
                  identity: { execution_environment_id: "hosted", local_id: "qingzhu" },
                  display_name: longName,
                  purposes: ["issue_manager"],
                  enabled: true,
                },
              ],
              runtimes: [],
            },
          },
        });
      });
      await page.reload();
      await detail
        .getByRole("button", { name: "更改负责人", exact: true })
        .getByText(longName)
        .waitFor();
      assert(
        await detail
          .locator("[data-issue-scroll]")
          .evaluate((node) => node.scrollWidth <= node.clientWidth + 1),
        "long assignee metadata stays inside the content lane",
      );
      await more.click();
      await page.screenshot({ path: join(output, `issue-menu-${width}.png`) });
      await menu.getByRole("menuitem", { name: "调度与事件诊断", exact: true }).click();
      await page.waitForURL((url) => url.pathname === "/diagnostics");
      assert.equal(new URL(page.url()).searchParams.get("issue"), issue.issue_id);
      assert.equal(new URL(page.url()).searchParams.get("tab"), "events");
      assert.deepEqual(pageErrors, []);
      const mainHistory = (url: URL) =>
        url.pathname === "/api/v1/conversation" &&
        url.searchParams.get("projection") === "chat" &&
        url.searchParams.get("conversation_id") === "main";
      await page.route(mainHistory, (route) =>
        route.fulfill({
          json: { items: [associationItem, crossReply], more: false, event_seq: seq },
        }),
      );
      await navigate(page, "/diagnostics/main");
      // The main conversation can retain a page from before this fixture was installed.
      const history = page.waitForResponse((response) => mainHistory(new URL(response.url())));
      await page.reload();
      assert.equal((await history).status(), 200);
      const chatReply = page.locator('[data-scroll-id="reply-cross-issue"]');
      await chatReply.getByText(crossReply.body!, { exact: true }).waitFor();
      await chatReply.getByRole("button", { name: "修改 Issue 关联", exact: true }).waitFor();
      console.log(
        `PASS S11 hierarchy ${width}: aligned full-width discussion, background disclosure, scoped diagnostics, collapsed trace and stable event history`,
      );
    } finally {
      await context.close();
      activity.items = original;
      issue.plan = originalPlan;
      issue.progress!.summary = originalProgress;
    }
  }
  {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();
    let historyAttempts = 0;
    let allowHistory = false;
    let historyEnabled = false;
    const historyEntries = Array.from({ length: 6 }, (_, index) => ({
      id: `complete-history-${index}`,
      position: index + 1,
      kind: "conversation",
      created_at: now + index,
      author: "你",
      block: {
        position: index + 1,
        more: false,
        trigger: {
          message_id: `complete-owner-${index}`,
          seq: index + 1,
          kind: "message",
          author: "owner",
          body: `完整历史输入 ${index + 1}`,
          state: "replied",
          created_at: new Date(now + index).toISOString(),
        },
        items: [],
      },
    }));
    await page.route("**/api/v1/issues/*/activity**", async (route) => {
      const before = new URL(route.request().url()).searchParams.get("before");
      if (before) {
        historyAttempts++;
        if (!allowHistory) {
          await route.fulfill({ status: 503, json: { error: { code: "unavailable" } } });
          return;
        }
      }
      await route.fulfill({
        json: {
          items: before ? historyEntries.slice(0, 1) : historyEntries.slice(1),
          more: !before && historyEnabled,
          ...(before ? {} : { next_before: 2 }),
          event_seq: seq,
        },
      });
    });
    try {
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      await detail.locator('[data-scroll-id="complete-owner-1"]').waitFor();
      assert.equal(
        await detail.locator('[data-scroll-id^="complete-owner-"]').count(),
        5,
        "all five loaded rounds render without a three-round fold",
      );
      await detail.getByRole("textbox", { name: "消息", exact: true }).waitFor();
      historyEnabled = true;
      publish();
      await detail.locator("[data-history-boundary]").waitFor({ state: "attached" });
      await detail.locator("[data-issue-scroll]").evaluate((node) => {
        node.querySelector("[data-history-boundary]")?.scrollIntoView({ block: "start" });
      });
      const retry = detail.getByRole("button", { name: "重试读取更早记录", exact: true });
      await retry.waitFor();
      assert.equal(
        await detail.getByRole("button", { name: "查看更早记录", exact: true }).count(),
        0,
      );
      assert.equal(historyAttempts, 1);
      await page.waitForTimeout(250);
      assert.equal(historyAttempts, 1, "failed automatic history does not loop");
      allowHistory = true;
      await retry.click();
      await detail.locator('[data-scroll-id="complete-owner-0"]').waitFor();
      assert.equal(await detail.locator('[data-scroll-id^="complete-owner-"]').count(), 6);
      assert.equal(await retry.count(), 0);
      assert.equal(await detail.locator("[data-history-boundary]").count(), 0);
      console.log(
        "PASS complete history: five rounds stay visible, automatic pagination stops on error and retries explicitly",
      );
    } finally {
      await context.close();
    }
  }
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
  // Current handling, immutable origin and human approval are separate facts.
  for (const width of [1440, 390]) {
    const originalDecision = structuredClone(decision);
    const writeCount = writes.length;
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push(message.text());
    });
    try {
      decision = {
        ...decision,
        issue_id: child.issue_id,
        status: "open",
        kind: "decision",
        responsibility: {
          origin_issue_id: child.issue_id,
          origin_assignment_epoch: 1,
          issue_id: issue.issue_id,
          recipient_kind: "owner",
          assignment_epoch: 1,
          revision: 1,
          ancestry: [],
          history: [],
        },
      };
      await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      const humanDecision = detail.getByRole("button", { name: "查看并决定", exact: true });
      await humanDecision.waitFor();
      await humanDecision.click();
      const original = page.getByRole("dialog", { name: "Issue 待处理事项", exact: true });
      await original.getByRole("radio", { name: "小范围灰度", exact: true }).waitFor();
      await original.getByRole("radio", { name: "小范围灰度", exact: true }).check();
      assert(!(await original.getByRole("button", { name: "提交决定", exact: true }).isDisabled()));
      await original.getByRole("button", { name: /^关闭/ }).first().click();

      decision.responsibility = {
        ...decision.responsibility!,
        recipient_kind: "agent",
        recipient_ref: issue.assignee.ref,
        revision: 2,
      };
      additionalAttentions = ["a", "b"].map((suffix) => ({
        ...structuredClone(decision),
        attention_id: `attention-extra-${suffix}`,
        kind: "action",
        title: `当前负责人补充待办 ${suffix}`,
      }));
      pagedResponsibility = true;
      publish();
      const agentAction = detail.getByRole("button", { name: "查看当前待办", exact: true });
      await agentAction.waitFor();
      assert.equal(await humanDecision.count(), 0);
      await detail.getByRole("button", { name: "加载更多当前责任待办", exact: true }).click();
      await detail.locator("summary").filter({ hasText: "另有 2 项" }).click();
      await detail
        .getByRole("button", {
          name: "等待本 Issue 负责人处理 · 当前负责人补充待办 b",
          exact: true,
        })
        .waitFor();
      assert.equal(
        await detail
          .getByRole("button", {
            name: "等待本 Issue 负责人处理 · 当前负责人补充待办 a",
            exact: true,
          })
          .count(),
        1,
      );
      assert.equal(
        await detail.getByRole("button", { name: "加载更多当前责任待办", exact: true }).count(),
        0,
      );
      await agentAction.click();
      await original.getByRole("heading", { name: decision.title, exact: true }).waitFor();
      assert.equal(
        await original.getByRole("button", { name: "提交决定", exact: true }).count(),
        0,
      );
      await original
        .getByRole("button", { name: `原始 Issue · ${child.issue_id}`, exact: true })
        .waitFor();
      await original.getByRole("button", { name: "打开原始事项记录 →", exact: true }).waitFor();
      await page.screenshot({ path: join(output, `detail-current-responsibility-${width}.png`) });
      await original.getByRole("button", { name: /^关闭/ }).first().click();
      decision.responsibility = {
        ...decision.responsibility,
        issue_id: child.issue_id,
        revision: 3,
      };
      additionalAttentions = [];
      publish();
      await agentAction.waitFor({ state: "hidden" });
      assert.equal(await humanDecision.count(), 0);
      assert.equal(decision.issue_id, child.issue_id);
      assert.equal(writes.length, writeCount, "reading responsibility did not answer the question");
      assert.deepEqual(errors, []);
      console.log(
        `PASS current responsibility ${width}: human decision retained; Agent original stays observational; child origin, route movement and unique pagination preserved`,
      );
    } finally {
      decision = originalDecision;
      additionalAttentions = [];
      pagedResponsibility = false;
      await context.close();
    }
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
    if (width < 761)
      await detail
        .locator("[data-issue-progress]")
        .getByRole("button", { name: "展开全文", exact: true })
        .click();
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

    await detail.getByText(/由 (青竹 · hosted\/qingzhu|hosted\/qingzhu) 更新/).waitFor();
    await detail.getByText(/执行 Agent 已交付成果/).waitFor();
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
    await page.waitForFunction(() => document.body.textContent?.includes("已入账"));
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
    assert.equal(await summary.count(), 0, "no main-column deliverables block");
    await openIssueAux(page, "外部交付");
    const external = page.getByRole("dialog", { name: "跟进与外部交付", exact: true });
    await external.getByText("已合并", { exact: true }).waitFor();
    await external.getByRole("button", { name: /^关闭/ }).first().click();
    await detail.locator('[data-artifact="artifact-one"]').waitFor();
    const resultCard = detail.locator('[data-artifact="artifact-one"]');
    await resultCard.getByRole("button", { name: "阅读完整报告 →", exact: true }).click();
    for (const letter of ["b", "c"]) {
      const commit = resultCard.getByText(letter.repeat(7), { exact: true });
      await commit.waitFor();
      assert.equal(await commit.getAttribute("title"), letter.repeat(40));
    }
    const taskLink = resultCard.getByRole("button", { name: "任务与验收记录 →", exact: true });
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
    await page.waitForFunction(() => document.activeElement?.textContent === "任务与验收记录 →");
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
    const traceToggle = traceTurn.locator("[data-trace-trigger]");
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
    await toolToggle.waitFor({ state: "detached" });
    await traceToggle.waitFor();
    if ((await traceToggle.getAttribute("aria-expanded")) === "true") await traceToggle.click();

    // A stable activity ID can grow while streaming without changing ledger order.
    const reply = activity.items![0]!.block!.items![0]!;
    const originalBody = reply.body!;
    reply.body = "流式回复的详细进展。\n\n".repeat(50);
    reply.assistant_revision!++;
    publish();
    const turn = detail.locator('[data-scroll-id="conversation-one"]');
    await turn.getByRole("button", { name: "展开全文", exact: true }).click();
    const scroller = detail.locator("[data-issue-scroll]");
    await scroller.hover();
    await page.mouse.wheel(0, -100000);
    await page.waitForFunction(() => document.querySelector("[data-issue-scroll]")!.scrollTop < 10);
    await page.mouse.wheel(0, 100000);
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
    const handoffDraft = detail.getByRole("textbox", { name: "消息", exact: true });
    await handoffDraft.fill("交接时仍可保存的草稿");
    await handoffDraft.evaluate((element) => {
      const data = new DataTransfer();
      data.items.add(new File(["draft only"], "handoff-draft.txt", { type: "text/plain" }));
      element.dispatchEvent(
        new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }),
      );
    });
    const handoffFiles = detail.getByRole("list", { name: "待发送附件", exact: true });
    await handoffFiles.getByText("handoff-draft.txt", { exact: true }).waitFor();
    assert(await detail.getByRole("button", { name: "发送", exact: true }).isDisabled());
    assert.equal(await handoffDraft.inputValue(), "交接时仍可保存的草稿");
    await handoffFiles.getByRole("button", { name: /移除/ }).click();
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
    await detail
      .getByRole("button", { name: "查看并决定", exact: true })
      .waitFor({ state: "detached" });
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
    const expand = detail
      .locator("[data-issue-progress]")
      .getByRole("button", { name: "展开全文", exact: true });
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
    const expand = detail
      .locator("[data-issue-progress]")
      .getByRole("button", { name: "展开全文", exact: true });
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
    await page
      .locator("article[data-scroll-id]")
      .getByRole("button", { name: "展开全文", exact: true })
      .waitFor();
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
  for (const width of [1440, 390]) {
    const recoveryContext = await browser.newContext({ viewport: { width, height: 1000 } });
    const recovery = await recoveryContext.newPage();
    const recoveryErrors: string[] = [];
    const recoveryConsole: { type: string; text: string; url: string }[] = [];
    recovery.on("pageerror", (error) => recoveryErrors.push(error.message));
    recovery.on("console", (message) => {
      if (["warning", "error"].includes(message.type()))
        recoveryConsole.push({
          type: message.type(),
          text: message.text(),
          url: message.location().url,
        });
    });
    await recovery.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
    assert.match(await recovery.title(), /tend/i);
    const recoveryDetail = recovery.getByRole("region", { name: "Issue 详情", exact: true });
    await recoveryDetail.getByRole("button", { name: "编辑", exact: true }).click();
    const editor = recovery.getByRole("dialog", { name: "编辑当前进展", exact: true });
    await editor.getByRole("textbox", { name: "当前进展", exact: true }).fill("原请求中的进展");
    await editor.getByRole("textbox", { name: "下一步", exact: true }).fill("原请求中的下一步");
    const beforeWrites = writes.length;
    await editor.getByRole("button", { name: "保存进展", exact: true }).click();
    await editor.getByRole("button", { name: "核对原更新请求", exact: true }).waitFor();
    await recovery.screenshot({ path: join(output, `progress-unknown-${width}.png`) });
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
    const beforeReads = requests.length;
    const beforeReconcileWrites = writes.length;
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
      .filter((e) => e.operation === "issues.update_progress");
    assert(recoveredWrites.length > 0);
    assert.equal(
      writes.length,
      beforeReconcileWrites,
      "current-state reconciliation never replays the POST",
    );
    assert.equal(new Set(recoveredWrites.map((envelope) => envelope.request_id)).size, 1);
    for (const envelope of recoveredWrites)
      assert.deepEqual(
        envelope,
        recoveredWrites[0],
        "browser transport retries preserve the complete original envelope",
      );
    const recoveredReads = requests.slice(beforeReads);
    assert(recoveredReads.some((path) => path === `/api/v1/issues/${issue.issue_id}`));
    assert.equal(requests.filter((path) => path.startsWith("/api/v1/requests/")).length, 0);
    assert.equal(issue.progress!.summary, "其他更新已被保存");
    assert.deepEqual(recoveryErrors, []);
    assert(
      recoveryConsole.every(
        (message) =>
          (message.type === "error" &&
            message.url.endsWith("/api/v1/ops") &&
            /net::ERR_/.test(message.text)) ||
          message.text === "Service Worker registration blocked by Playwright",
      ),
      JSON.stringify(recoveryConsole),
    );
    await recovery.screenshot({ path: join(output, `progress-current-recovery-${width}.png`) });
    await writeFile(
      join(output, `progress-recovery-observations-${width}.json`),
      JSON.stringify(
        {
          url: recovery.url(),
          title: await recovery.title(),
          recoveryErrors,
          recoveryConsole,
          original: recoveredWrites[0],
          currentIssueRevision: issue.revision,
          recoveredReads,
          transportAttempts: recoveredWrites.length,
          reconciliationWrites: writes.length - beforeReconcileWrites,
        },
        null,
        2,
      ),
    );
    await recoveryContext.close();
    console.log(
      `PASS ${width}: progress draft survives current Issue CAS recovery without receipt lookup or replay`,
    );
  }
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
  const quietScroll = quietIssue.locator("[data-issue-scroll]");
  await quietIssue.locator('[data-scroll-id="recent-fact-19"]').scrollIntoViewIfNeeded();
  await quietScroll.hover();
  await quietPage.mouse.wheel(0, -10000);
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
  await isolation.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
  await isolatedIssue.locator("[data-event-group] > summary").click();
  await isolatedIssue.locator("[data-history-boundary]").waitFor({ state: "attached" });
  await isolatedIssue.locator("[data-issue-scroll]").evaluate((node) => {
    node.querySelector("[data-history-boundary]")?.scrollIntoView({ block: "start" });
  });
  await startedEarlier;
  await isolatedIssue.locator("[data-issue-scroll]").evaluate((node) => {
    node.scrollTop = 0;
  });
  assert(
    (await isolatedIssue
      .locator("[data-issue-scroll]")
      .evaluate((node) => node.scrollHeight - node.scrollTop - node.clientHeight)) > 100,
    "the delayed-history reader remains above the latest activity",
  );
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
    "PASS 390: missing binding never leaks main's outbox; scrolling reads earlier activity automatically",
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
      await detail
        .locator("article[data-scroll-id]")
        .getByRole("button", { name: "展开全文", exact: true })
        .first()
        .click();
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
        await detail
          .locator("article[data-scroll-id]")
          .getByRole("button", { name: "展开全文", exact: true })
          .first()
          .click();
        if (scenario === "group") await detail.locator("[data-trace-trigger]").click();
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
          const group = detail.locator('[data-scroll-id="new-group-tool"]');
          await group.waitFor();
          await page.waitForTimeout(100);
          assert(
            Math.abs((await group.evaluate((node) => node.getBoundingClientRect().top)) - y) <= 2,
            "pagination did not anchor the newly visible flat tool step",
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
    let releaseConversation = () => {};
    let conversationGate = new Promise<void>((resolve) => {
      releaseConversation = resolve;
    });
    const conversationID = issue.conversation.conversation_id;
    assert(conversationID, "the return fixture needs its actual Issue conversation");
    const conversationPath = `/api/v1/agent-conversations/${encodeURIComponent(conversationID)}`;
    await page.route(
      (url) => url.pathname === conversationPath,
      async (route) => {
        const gate = conversationGate;
        const response = await route.fetch();
        await gate;
        await route.fulfill({ response });
      },
    );
    const finishConversation = async () => {
      const response = page.waitForResponse(
        (reply) => new URL(reply.url()).pathname === conversationPath,
      );
      releaseConversation();
      await response;
      await page
        .getByRole("region", { name: "Issue 详情", exact: true })
        .locator("textarea")
        .waitFor({ state: "visible" });
      // Observe the ready composer and its viewport before locating the reading point.
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
    };
    try {
      await page.goto(`http://127.0.0.1:${address.port}/?issue=${issue.issue_id}`);
      const detail = page.getByRole("region", { name: "Issue 详情", exact: true });
      const more = detail.getByRole("button", { name: "继续读取本轮消息", exact: true });
      await more.click();
      await detail.getByText("第二页内容", { exact: true }).waitFor();
      await more.click();
      await detail.getByText("第三页保留消息", { exact: true }).waitFor();
      const scroller = detail.locator("[data-issue-scroll]");
      const preparingHeight = await scroller.evaluate((node) => node.clientHeight);
      await finishConversation();
      const readyHeight = await scroller.evaluate((node) => node.clientHeight);
      assert(readyHeight < preparingHeight, "the delayed composer did not resize the viewport");
      const taskLink = detail
        .getByRole("button", { name: "任务与验收记录 →", exact: true })
        .first();
      await taskLink.scrollIntoViewIfNeeded();
      const top = await detail.locator("[data-issue-scroll]").evaluate((node) => node.scrollTop);
      await taskLink.click();
      await page.getByRole("region", { name: "任务详情", exact: true }).waitFor();
      conversationGate = new Promise<void>((resolve) => {
        releaseConversation = resolve;
      });
      await page.goBack();
      await detail.getByText("第三页保留消息", { exact: true }).waitFor({ state: "attached" });
      await finishConversation();
      assert.equal(await scroller.evaluate((node) => node.clientHeight), readyHeight);
      await writeFile(
        join(output, `third-page-return-${width}.json`),
        JSON.stringify({
          preparingHeight,
          readyHeight,
          before: top,
          after: await scroller.evaluate((node) => node.scrollTop),
        }),
      );
      assert(
        Math.abs(
          (await detail.locator("[data-issue-scroll]").evaluate((node) => node.scrollTop)) - top,
        ) <= 2,
        "returning from task lost continued-page reading position",
      );
      assert.equal(await more.count(), 0, "returned turn forgot loaded continuation depth");
      await detail.locator("[data-issue-scroll]").evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      await page.setViewportSize({ width, height: 780 });
      await page.waitForFunction(() => {
        const node = document.querySelector("[data-issue-scroll]") as HTMLElement;
        return Math.abs(node.scrollHeight - node.scrollTop - node.clientHeight) < 1;
      });
      await page.setViewportSize({ width, height: 844 });
      await page.waitForFunction(() => {
        const node = document.querySelector("[data-issue-scroll]") as HTMLElement;
        return Math.abs(node.scrollHeight - node.scrollTop - node.clientHeight) < 1;
      });
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
      releaseConversation();
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
