import { navigate, fixtureRoot, editIssueFilters } from "./navigation.ts";
import { issueBoardFixture } from "./issue-fixture.ts";
/// <reference lib="dom" />
// Approved Issue/Inbox flows against isolated API fixtures, never a live service.
import { issueSidebar, openIssueAux, closeIssue } from "./issue-detail-helpers.ts";
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { chromium } from "playwright";
import type {
  AttentionView,
  Envelope,
  IssueView,
  IssueSummary,
  IssuePlan,
  TaskDetail,
} from "../src/api/generated/api.ts";
const catalogOnly = process.argv.includes("--catalog-only");
let hostedUnconfigured = false;
let holdTaskFeed = false;
const output = process.env.TEND_LAYOUT_OUTPUT;
assert(output, "Set an isolated screenshot output directory");
await mkdir(output, { recursive: true });
const root = fixtureRoot();
const now = Date.now();
const assignee = { kind: "agent", ref: "hosted/qingzhu" } as const;
const issue: IssueView = {
  manager: { task_id: "task-manager", category_id: "issue-manager", state: "queued" },
  issue_id: "issue-root",
  state: "active",
  assignee,
  assignment_epoch: 1,
  assignment_state: "active",
  revision: 1,
  plan: {
    scope: "发布与回滚文档",
    completion_criteria: ["步骤可核对"],
    title: "发布流程整理",
    project: "sample",
    intent: "让发布步骤、验收证据与回滚入口清晰可追踪。",
    stages: [],
  },
  depends_on: [],
  created_at: now,
  updated_at: now,
  tasks: [],
};
const activeTask: TaskDetail = {
  task_id: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV",
  title: "核对发布入口",
  project: "sample",
  lifecycle: "running",
  block_reason: "none",
  control_mode: "managed",
  control_revision: 7,
  spec_version: 2,
  rating: "p1",
  workspace: "",
  next_implement_generation: 4,
  actions: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
  spec: { goal: "核对当前发布入口", acceptance: ["独立验收可重复核对"] },
  execution: { state: "running", run_id: "run-current", role: "implement" },
  runs: [
    {
      run_id: "run-unrecorded",
      role: "implement",
      generation: 1,
      host: "runner-a",
      observed: "finished",
    },
    {
      run_id: "run-current",
      role: "implement",
      generation: 3,
      host: "runner-a",
      spec_version: 2,
      observed: "running",
    },
  ],
};
issue.tasks = [activeTask];
let runMessages: Record<string, unknown>[] = [];
let supplementRequest = "",
  supplementReady = false;
let takeRequest = "",
  takeReceiptReady = false;
const summary = (id: string, title: string, parent = ""): IssueSummary => ({
  issue_id: id,
  title,
  project: "sample",
  parent_issue_id: parent,
  state: "active",
  assignee,
  assignment_epoch: 1,
  assignment_state: "active",
  revision: 1,
});
const issueRows = [
  summary("issue-root", "发布流程整理"),
  summary("issue-child", "补齐回滚文档", "issue-root"),
  summary("issue-grandchild", "补齐失败示例", "issue-child"),
];
let messageAssociations = {
  message_id: "owner-plan",
  revision: 3,
  content_revision: 1,
  disposition: "linked",
  owner_locked: true,
  facts_reviewed: true,
  items: [{ issue_id: "issue-root", title: "发布流程整理", source: "owner", owner_locked: true }],
};
const baseAttention = {
  latest_action_reminders: false,
  issue_id: "issue-root",
  revision: 1,
  status: "open",
  source_message_id: "attention-source-note",
  raised_by: { kind: "main_agent", subject: "qingzhu" },
  created_at: now,
  updated_at: now,
} as const;
let decision: AttentionView = {
  ...baseAttention,
  attention_id: "attention-decision",
  kind: "decision",
  title: "确认灰度发布范围",
  question_version: 3,
  question_options: ["小范围灰度", "完整灰度范围"],
};
let action: AttentionView = {
  ...baseAttention,
  attention_id: "attention-action",
  kind: "action",
  title: "提供验收设备清单",
};
const information: AttentionView = {
  ...baseAttention,
  attention_id: "attention-information",
  kind: "information",
  title: "发布进度已更新（仅通知）",
};
const streams = new Set<ServerResponse>();
const writes: Envelope[] = [];
let lostID = "";
let releaseReceipt = false;
let seq = 1;
const sourceReceipts = new Map<string, unknown>();
let uploadGate = Promise.resolve();
let uploadCount = 0,
  sourceCount = 0;
let sourceRequest = "",
  sourceReceiptReady = false;
let consultationID = "";
let consultationTitle = "发布方式咨询";
let inboxMore = false;
let replyBody = "我会统筹目标与下一步。当前需要你确认发布范围；相关任务按约定实施与独立验收。";
let replyRevision = 1;
let importedIssue: IssueView | undefined;
let taskQueued = false;
let sourceContentRevision = 7;
let issueCreateRequest = "";
let issueCreateReady = false;
let rejectSource = false;
let handoffRequest = "",
  handoffReceiptReady = false;
const manualSources = new Map<string, object>();
const archiveSummary = {
  archive_id: "archive-2026",
  state: "ready",
  metadata: {
    created_at: "2026-10-28T08:00:00Z",
    deployment_id: "previous-deployment",
    format_version: 1,
    schema: 57,
    record_kinds: ["work_item", "task", "message"],
    original_count: 1,
    resource_count: 1,
  },
};
const publish = () => {
  for (const stream of streams)
    stream.write(
      `id: ${++seq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: seq, object_type: "attention", object_id: action.attention_id, kind: "attention.updated", created_at: new Date().toISOString() })}\n\n`,
    );
};
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://localhost");
  const p = u.pathname;
  res.setHeader("Cache-Control", "no-store");
  const json = (data: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(issueBoardFixture(res.req.url, data)));
  };
  if (p === "/api/v1/session")
    return json({
      token: "fixture-token",
      login: "owner",
      client_id: "ui-fixture",
      scope: "owner",
      deployment_id: "issue-surfaces-fixture",
    });
  if (p === "/api/v1/snapshot")
    return json({
      event_seq: seq,
      tasks: [],
      hosts: [
        {
          host_id: "runner-a",
          online: true,
          slots: 8,
          open_runs: 1,
          worker: "orca",
          os: "linux",
          arch: "amd64",
          labels: [],
        },
        {
          host_id: "runner-b",
          online: false,
          slots: 8,
          open_runs: 0,
          worker: "orca",
          os: "linux",
          arch: "amd64",
          labels: [],
        },
      ],
      projects: [],
      queue: [],
      decisions: [],
    });
  if (p === "/api/v1/conversation")
    return json({
      items: [
        {
          message_id: "assistant-example",
          seq: 1,
          kind: "message",
          author: "main_agent",
          body: "我会统筹目标与下一步。当前需要你确认发布范围。",
          created_at: new Date().toISOString(),
          state: "replied",
        },
      ],
      more: false,
    });
  if (p === "/api/v1/conversation/settings")
    return json({
      enabled: true,
      available: true,
      revision: 1,
      model: "gpt-6.1-sol",
      effort: "high",
      models: [
        {
          id: "gpt-6.1-sol",
          label: "GPT-6.1-Sol",
          supported_efforts: ["high"],
          default_effort: "high",
        },
      ],
    });
  if (p === "/api/v1/issue-notification-settings") return json({ revision: 1, delay_minutes: 3 });
  if (p === "/api/v1/token-usage") {
    const totals = {
      requests: 1,
      final: 1,
      input: 8000,
      input_known: 1,
      output: 1000,
      output_known: 1,
      total: 9000,
      total_known: 1,
      cache_read: 1000,
      cache_read_known: 1,
      cache_write: 0,
      cache_write_known: 1,
    };
    return json({
      trend: [],
      revision: 1,
      as_of: now,
      timezone: "Asia/Shanghai",
      window: u.searchParams.get("window") ?? "total",
      start: null,
      end: now,
      totals,
      unattributed: { ...totals, requests: 0 },
      models: [{ name: "fixture-model", ...totals }],
      roles: [{ name: "implement", ...totals }],
      records: [],
      next: 0,
      more: false,
      conflicts: 0,
      coverage: "隔离示例请求；不表示真实消费。",
    });
  }
  if (p === "/api/v1/conversation/handoff")
    return json({ enabled: false, revision: 1, idle_minutes: 30 });
  if (p === "/api/v1/conversation/transcript")
    return json({
      blocks: [
        {
          position: 1,
          trigger: {
            message_id: "owner-plan",
            seq: 1,
            kind: "message",
            author: "owner",
            body: "请帮我梳理发布与回滚流程。",
            created_at: new Date().toISOString(),
            state: "replied",
            associations: messageAssociations,
          },
          items: [
            {
              message_id: "reply-plan",
              seq: 2,
              kind: "message",
              author: "main_agent",
              body: replyBody,
              assistant_revision: replyRevision,
              associations: { ...messageAssociations, message_id: "reply-plan" },
              created_at: new Date().toISOString(),
              reply_to: "owner-plan",
              state: "replied",
            },
          ],
          more: false,
        },
      ],
      event_seq: seq,
      more: false,
    });
  if (p === "/api/v1/conversation/queue")
    return json({ items: [], enabled: true, event_seq: seq, total: 0 });
  if (p.endsWith("/usage"))
    return json({
      available: false,
      compact_supported: false,
      measurement: "unavailable",
      sampled_at: now,
      updated_at: now,
    });
  if (p.endsWith("/compactions")) return json({ items: [] });
  if (p === "/api/v1/agent-conversations")
    return json({
      items: consultationID
        ? [
            {
              conversation_id: consultationID,
              title: consultationTitle,
              kind: "consultation",
              status: "idle",
              model: "gpt-6.1-sol",
              effort: "high",
              revision: 1,
              last_message_at: now,
              last_message_seq: 0,
              preview: "",
            },
          ]
        : [],
      runtime_available: true,
    });
  if (p === "/api/v1/agent-conversations/work-item%3Aissue-root")
    return json({
      conversation_id: "work-item:issue-root",
      issue_id: issue.issue_id,
      agent_ref: issue.assignee.ref,
      title: issue.plan.title,
      kind: "work_item",
      status: "idle",
      revision: 1,
      model: "gpt-6.1-sol",
      effort: "high",
      last_message_at: now,
      last_message_seq: 0,
      preview: "",
    });
  if (p.startsWith("/api/v1/agent-conversations/"))
    return json({
      conversation_id: consultationID,
      title: consultationTitle,
      kind: "consultation",
      agent_ref: "hosted/qingzhu",
      status: "idle",
      model: "gpt-6.1-sol",
      effort: "high",
      revision: 1,
      last_message_at: now,
      last_message_seq: 0,
      preview: "",
    });
  if (p === "/api/v1/categories")
    return json({
      items: [
        {
          category_id: "issue-manager",
          title: "Issue manager",
          description: "Coordination",
          revision: 1,
          state: "active",
        },
        {
          category_id: "custom-coordinator",
          title: "Custom coordinator",
          description: "Custom coordination",
          revision: 1,
          state: "active",
        },
        {
          category_id: "implementer",
          title: "实施",
          description: "实施任务",
          revision: 1,
          state: "active",
        },
        {
          category_id: "reviewer",
          title: "独立验收",
          description: "验收任务",
          revision: 1,
          state: "active",
        },
      ],
      more: false,
    });
  if (p === "/api/v1/repos") return json({ repos: [{ repo: "sample", retired: false }] });
  if (p === "/api/v1/tasks") {
    if (holdTaskFeed && u.searchParams.get("limit") !== "5") return;
    return json({ tasks: [] });
  }
  if (p === "/api/v1/tasks/task-manager")
    return json({
      ...activeTask,
      task_id: "task-manager",
      title: "Issue manager responsibility",
      kind: "issue_manager",
      lifecycle: "queued",
      runs: [],
      execution: { state: "queued" },
      spec: { reason: "Retain issue coordination until handoff or closure" },
      manager: {
        issue_id: "issue-root",
        category_id: "issue-manager",
        conversation_id: "issue:issue-root",
        assignment_epoch: 1,
      },
    });
  if (p === `/api/v1/tasks/${activeTask.task_id}`)
    return json(
      taskQueued
        ? { ...activeTask, lifecycle: "queued", execution: undefined, runs: [] }
        : activeTask,
    );
  if (p.startsWith("/api/v1/runs/") && p.endsWith("/rollout"))
    return json({
      run_id: p.split("/")[4],
      observed: "running",
      logs: [],
      watermark: 0,
      after: 0,
      before: 0,
      more_after: false,
      more_before: false,
    });
  if (p.startsWith("/api/v1/runs/") && p.endsWith("/messages"))
    return json({ run_id: p.split("/")[4], messages: runMessages, more: false });
  if (p.startsWith("/api/v1/runs/") && p.endsWith("/native-entry"))
    return json({
      run_id: p.split("/")[4],
      task_id: activeTask.task_id,
      host_id: "runner-a",
      provider: "orca",
      adapter_implemented: true,
      session_recorded: true,
      workspace_recorded: true,
      control_mode: activeTask.control_mode,
      control_revision: activeTask.control_revision,
      run_observed: "running",
      controller_observation: "unknown",
      reason: "",
      identity: {
        provider: "orca",
        session_id: "actual-session",
        terminal: "terminal-7",
        pane: "pane-2",
        state: "recorded",
        controller_observation: "unknown",
      },
      execution: {
        agent_revision: 1,
        backend_id: "orca",
        identity: { execution_environment_id: "runner-a", local_id: "songfeng" },
        inventory_revision: 1,
        profile: { model: "gpt-6.1-sol", effort: "high" },
        public_sha256: "publicfixture",
        runtime: { runtime_id: "orca-execution", revision: 1 },
        snapshot_id: "snapshot-1",
        worker: "orca",
      },
    });
  if (p === "/api/v1/work-items") return json({ items: [], more: false });
  if (p === "/api/v1/issues")
    return json({
      items: issueRows.filter(
        (row) =>
          !u.searchParams.has("parent_id") ||
          (row.parent_issue_id ?? "") === u.searchParams.get("parent_id"),
      ),
      more: false,
    });
  if (p.endsWith("/followups"))
    return json({
      enabled: true,
      items: [
        {
          rule_id: "followup-manual",
          issue_id: issue.issue_id,
          revision: 1,
          assignment_epoch: issue.assignment_epoch,
          purpose: "核对发布证据",
          instruction: "整理证据后再核对",
          status: "active",
          schedule: { kind: "manual" },
          created_at: now,
          updated_at: now,
        },
        {
          rule_id: "followup-deadline",
          issue_id: issue.issue_id,
          revision: 1,
          deadline_id: "deadline-1",
          purpose: "发布约定核对",
          instruction: "按时间约定核对进度",
          status: "active",
          schedule: { kind: "manual" },
          created_at: now,
          updated_at: now,
        },
      ],
      more: false,
    });
  if (p.endsWith("/deliveries")) return json({ issue_id: issue.issue_id, items: [] });
  if (p.includes("/followups/") && p.endsWith("/history"))
    return json({ occurrences: [], revisions: [], more: false });
  if (p.endsWith("/activity")) return json({ items: [], more: false, event_seq: seq });
  if (p.endsWith("/results")) return json({ items: [], more: false });
  if (p.startsWith("/api/v1/issues/"))
    return json(
      p.endsWith("/context")
        ? {
            issue_id: issue.issue_id,
            revision: 1,
            prepared: true,
            coverage: "",
            gaps: "",
            dispatch_ready: true,
            coordination_state: "ready",
            dispatch_blockers: [],
            sync_status: "current",
            watermark: 1,
            facts: [],
            sources: [],
            more: false,
          }
        : p.endsWith("issue-imported")
          ? importedIssue
          : {
              ...issue,
              conversation: {
                conversation_id: "work-item:issue-root",
                can_send: issue.assignment_state === "active",
              },
            },
    );
  if (p === "/api/v1/next-actions")
    return json({
      items: [
        {
          entry_id: "next-decision",
          revision: 1,
          kind: "user_action",
          issue_id: issue.issue_id,
          attention_id: decision.attention_id,
          title: decision.title,
          responsible: "owner",
          rank: 1,
          status: decision.status === "open" ? "open" : "done",
          reason: "发布前确认",
          expected_at: now + 3600_000,
          updated_at: now,
        },
        {
          entry_id: "risk",
          revision: 1,
          kind: "issue_priority",
          issue_id: issue.issue_id,
          title: "验收等待实施交付",
          responsible: "qingzhu",
          rank: 2,
          status: "blocked",
          reason: "等待实施证据",
          updated_at: now,
        },
      ],
      deadlines: [],
    });
  if (p === "/api/v1/inbox") {
    let rows = [information, decision, action].filter(
      (row) => !row.archived_at || u.searchParams.get("include_archived") === "true",
    );
    const kind = u.searchParams.get("kind");
    const status = u.searchParams.get("status");
    if (kind) rows = rows.filter((row) => row.kind === kind);
    if (status) rows = rows.filter((row) => row.status === status);
    return json({ items: rows, more: inboxMore });
  }
  if (p.startsWith("/api/v1/attentions/"))
    return json(
      p.endsWith(information.attention_id)
        ? information
        : p.endsWith(decision.attention_id)
          ? decision
          : action,
    );
  if (p === "/api/v1/hosted-agents" && hostedUnconfigured)
    return json({
      configuration_revision: 0,
      public_sha256: "",
      main_agent_id: "",
      accepted_request_id: "",
      state: "unconfigured",
      projection: {
        execution_environment_id: "",
        format_version: 1,
        source_kind: "inline",
        runtimes: [],
        agents: [],
      },
    });
  if (p === "/api/v1/hosted-agents")
    return json({
      configuration_revision: 1,
      public_sha256: "fixture",
      main_agent_id: "qingzhu",
      accepted_request_id: "fixture",
      state: "registered_staged",
      projection: {
        execution_environment_id: "hosted",
        format_version: 1,
        source_kind: "local_file",
        runtimes: [{ runtime_id: "hosted", revision: 1, kind: "hosted-manager", cli: "traex" }],
        agents: [
          {
            identity: { execution_environment_id: "hosted", local_id: "qingzhu" },
            revision: 1,
            display_name: "青竹",
            runtime_binding_ref: { runtime_id: "hosted", revision: 1 },
            categories: ["planner"],
            purposes: ["main", "issue_manager", "consultation"],
            requested_model: "gpt-6.1-sol",
            requested_effort: "high",
            enabled: true,
            registration: "registered_staged",
            availability: { available: false, reason: "not_observed" },
          },
        ],
      },
    });
  if (/^\/api\/v1\/hosts\/[^/]+\/agents$/.test(p)) {
    const hostID = p.split("/")[4]!;
    return json({
      host_id: hostID,
      online: hostID === "runner-a",
      last_heartbeat_at_ms: now,
      projection: {
        execution_environment_id: hostID,
        format_version: 1,
        source_kind: "local_file",
        runtimes: [
          { runtime_id: "orca-execution", revision: 1, kind: "orca-worker", cli: "traex" },
        ],
        agents: [
          {
            identity: { execution_environment_id: hostID, local_id: "songfeng" },
            revision: 1,
            display_name: "松风",
            runtime_binding_ref: { runtime_id: "orca-execution", revision: 1 },
            categories: ["implementer", "reviewer"],
            purposes: ["worker"],
            requested_model: "gpt-6.1-sol",
            requested_effort: "high",
            enabled: true,
            registration: "staged",
            availability: { available: false, reason: "not_observed" },
          },
        ],
      },
      executors: [
        {
          available: true,
          pin: {
            identity: { execution_environment_id: hostID, local_id: "songfeng" },
            inventory_revision: 2,
          },
        },
      ],
    });
  }
  if (/^\/api\/v1\/messages\/manual-source-\d+\/associations$/.test(p))
    return json({
      message_id: p.split("/")[4],
      revision: 1,
      content_revision: sourceContentRevision,
      disposition: "unassigned",
      items: [],
    });
  if (p === "/api/v1/messages/owner-plan/associations") return json(messageAssociations);
  if (p === "/api/v1/messages/attention-source-note")
    return json({
      message_id: "attention-source-note",
      seq: 5,
      kind: "message",
      author: "main_agent",
      body: "请核对发布范围与设备信息，保留原始证据后继续。",
      created_at: new Date(now).toISOString(),
    });
  if (p.startsWith("/api/v1/sources/"))
    return json(
      manualSources.get(p.split("/").at(-1)!) ?? {
        message_id: p.split("/").at(-1),
        text: "原始说明",
        metadata: {},
        created_at: now,
      },
    );
  if (p === "/api/v1/archives")
    return json({
      items: [
        archiveSummary,
        { archive_id: "earlier-archive", state: "unsupported", reason: "该格式暂不支持" },
      ],
    });
  if (p === "/api/v1/archives/archive-2026") return json(archiveSummary);
  if (p === "/api/v1/archives/archive-2026/records/work_item")
    return json({
      archive_id: "archive-2026",
      schema: 57,
      kind: "work_item",
      items: [
        {
          work_item_id: "old-issue",
          plan_json: JSON.stringify({ title: "历史发布验证", intent: "保留历史事实" }),
          status: "closed",
        },
      ],
    });
  if (p.startsWith("/api/v1/archives/archive-2026/records/"))
    return json({ archive_id: "archive-2026", schema: 57, kind: p.split("/").at(-1), items: [] });
  if (p === "/api/v1/archives/archive-2026/originals/deliverable")
    return json({
      archive_id: "archive-2026",
      items: [
        {
          kind: "deliverable",
          id: "old-report",
          name: "验收报告.md",
          media_type: "text/markdown",
          size: 19,
          sha256: "fixture-report",
        },
      ],
    });
  if (p === "/api/v1/archives/archive-2026/originals/deliverable/old-report/download-url")
    return json({ url: "/fixture/archive-original" });
  if (p === "/fixture/archive-original") {
    res.setHeader("Content-Type", "text/markdown");
    return res.end("original file bytes");
  }
  if (p === "/api/v1/archives/archive-2026/resources")
    return json({
      archive_id: "archive-2026",
      items: [
        {
          id: "native-missing",
          kind: "native_session",
          scope: "run",
          run_id: "old-run",
          status: "missing",
          reason: "未收录原生会话目录",
          files: [],
        },
      ],
    });
  if (p === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`event: start\ndata: {"event_seq":${seq}}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (p === "/api/v1/conversation/attachments" && req.method === "POST") {
    const id = u.searchParams.get("request_id")!;
    let bytes = Buffer.alloc(0);
    for await (const chunk of req) bytes = Buffer.concat([bytes, chunk]);
    assert.equal(bytes.toString(), "original file bytes");
    await uploadGate;
    uploadCount++;
    sourceReceipts.set(id, {
      request_id: id,
      operation: "conversation.upload_attachment",
      status: "succeeded",
      result: {
        file_id: "source-file",
        name: u.searchParams.get("name") ?? "evidence.txt",
        media_type: u.searchParams.get("media_type") ?? "text/plain",
        sha256: "fixturehash",
        size: bytes.length,
      },
    });
    return json({ operation: { request_id: id, status: "unknown" } });
  }
  if (p === "/api/v1/ops") {
    let data = "";
    for await (const chunk of req) data += String(chunk);
    const envelope = JSON.parse(data) as Envelope;
    writes.push(envelope);
    if (envelope.operation === "conversation.inject_source") {
      sourceCount++;
      sourceRequest = envelope.request_id;
      const payload = envelope.payload as {
        conversation_id?: string;
        text: string;
        metadata: object;
        attachments?: string[];
      };
      manualSources.set(`manual-source-${sourceCount}`, {
        message_id: `manual-source-${sourceCount}`,
        conversation_id: payload.conversation_id || "main",
        text: payload.text,
        metadata: payload.metadata,
        created_at: now,
        attachments: (payload.attachments ?? []).map((id) => ({
          file_id: id,
          name: "evidence.txt",
          size: 19,
          media_type: "text/plain",
          sha256: "fixturehash",
        })),
      });
      sourceReceipts.set(envelope.request_id, {
        request_id: envelope.request_id,
        operation: envelope.operation,
        status: "succeeded",
        result: {
          message_id: `manual-source-${sourceCount}`,
          conversation_id: "main",
          metadata: { author: "文档作者" },
        },
      });
      if (rejectSource) {
        const refused = {
          request_id: envelope.request_id,
          operation: envelope.operation,
          status: "failed",
          error: { code: "invalid_request", message: "fixture rejected source" },
        };
        sourceReceipts.set(envelope.request_id, refused);
        return json({ operation: refused });
      }
      return json({
        operation:
          sourceCount > 1
            ? sourceReceipts.get(envelope.request_id)
            : { request_id: envelope.request_id, status: "unknown" },
      });
    }
    if (envelope.operation === "issues.create") {
      const payload = envelope.payload as {
        plan: IssuePlan;
        source_message_id?: string;
        expected_source_revision?: number;
        assignee?: { kind: string; ref: string };
        manager?: { category_id: string; expected_assignment_epoch: number };
      };
      // Mirror actual owner API validation instead of accepting impossible input.
      if (payload.source_message_id && payload.expected_source_revision !== sourceContentRevision)
        return json({
          operation: {
            request_id: envelope.request_id,
            status: "failed",
            error: {
              code: "invalid_request",
              message: "source requires exact message and content revision",
            },
          },
        });
      assert(
        payload.assignee?.kind === "owner" || payload.manager?.category_id === "custom-coordinator",
      );
      assert(!(payload.assignee && payload.manager));
      if (payload.manager) assert.equal(payload.manager.expected_assignment_epoch, 1);
      importedIssue = {
        ...issue,
        issue_id: "issue-imported",
        plan: payload.plan,
        tasks: [],
        assignee: payload.assignee ?? { kind: "owner", ref: "owner" },
        manager: payload.manager
          ? {
              task_id: "task-imported-manager",
              category_id: payload.manager.category_id,
              state: "queued",
            }
          : undefined,
      } as IssueView;
      issueCreateRequest = envelope.request_id;
      sourceReceipts.set(envelope.request_id, {
        request_id: envelope.request_id,
        status: "succeeded",
        result: importedIssue,
      });
      return json({ operation: { request_id: envelope.request_id, status: "unknown" } });
    }
    if (envelope.operation === "conversation.set_issue_associations") {
      const payload = envelope.payload as { issue_ids: string[]; disposition: string };
      messageAssociations = {
        ...messageAssociations,
        revision: messageAssociations.revision + 1,
        disposition: payload.disposition,
        items: payload.issue_ids.map((id) => ({
          issue_id: id,
          title: issueRows.find((row) => row.issue_id === id)?.title || id,
          source: "owner",
          owner_locked: true,
        })),
      };
      return json({
        operation: {
          request_id: envelope.request_id,
          status: "succeeded",
          result: messageAssociations,
        },
      });
    }
    if (envelope.operation === "conversation.consult") {
      consultationID = "consultation:fixture";
      return json({
        operation: {
          request_id: envelope.request_id,
          status: "succeeded",
          result: {
            conversation_id: consultationID,
            title: consultationTitle,
            kind: "consultation",
          },
        },
      });
    }
    if (envelope.operation === "issues.create_task")
      return json({
        operation: {
          request_id: envelope.request_id,
          status: "succeeded",
          result: { task_id: "task-created" },
        },
      });
    if (envelope.operation === "runs.send_message") {
      const payload = envelope.payload as { text: string };
      runMessages = [
        {
          message_id: "supplement-one",
          run_id: envelope.target.id,
          text: payload.text,
          status: "accepted",
          created_at: now,
        },
      ];
      supplementRequest = envelope.request_id;
      return json({ operation: { request_id: envelope.request_id, status: "unknown" } });
    }
    if (envelope.operation === "control.set_mode") {
      const payload = envelope.payload as { mode: TaskDetail["control_mode"] };
      if (payload.mode === "human_control") {
        takeRequest = envelope.request_id;
        return json({ operation: { request_id: envelope.request_id, status: "unknown" } });
      }
      activeTask.control_mode = payload.mode;
      activeTask.control_revision++;
      return json({
        operation: { request_id: envelope.request_id, status: "succeeded", result: activeTask },
      });
    }
    if (envelope.operation === "issues.assign") {
      handoffRequest = envelope.request_id;
      return json({ operation: { request_id: handoffRequest, status: "unknown" } });
    }
    if (envelope.operation === "issues.finish_assignment") {
      issue.assignee = issue.pending_assignee!;
      delete issue.pending_assignee;
      delete issue.pending_handoff_id;
      issue.assignment_state = "active";
      issue.assignment_epoch++;
      issue.revision++;
      return json({
        operation: { request_id: envelope.request_id, status: "succeeded", result: issue },
      });
    }
    if (envelope.operation === "issues.update") {
      issue.plan = (envelope.payload as { plan: IssuePlan }).plan;
      issue.revision++;
      return json({
        operation: { request_id: envelope.request_id, status: "succeeded", result: issue },
      });
    }
    if (
      envelope.operation === "attentions.present" &&
      envelope.target.id === information.attention_id
    ) {
      const payload = envelope.payload as { archived?: boolean };
      if (payload.archived) information.archived_at = now;
      publish();
      return json({
        operation: { request_id: envelope.request_id, status: "succeeded", result: information },
      });
    }
    if (envelope.operation === "attentions.confirm") {
      const payload = envelope.payload as { summary: string; source_message_id?: string };
      action.resolution = payload.summary;
      if (payload.source_message_id) action.resolution_source_id = payload.source_message_id;
      lostID = envelope.request_id;
      return json(
        { error: { code: "internal", message: "Expected fixture lost acknowledgement" } },
        503,
      );
    }
    if (envelope.operation === "attentions.respond") {
      const payload = envelope.payload as { version: number; option: string; text: string };
      decision = {
        ...decision,
        status: "resolved",
        revision: decision.revision + 1,
        resolution: [payload.option, payload.text].filter(Boolean).join(" · "),
      };
      publish();
      return json({ operation: { status: "succeeded", result: decision } });
    }
    return json({ operation: { status: "succeeded", result: {} } });
  }
  if (p.startsWith("/api/v1/requests/")) {
    const id = decodeURIComponent(p.split("/").at(-1)!);
    if (id === supplementRequest)
      return json({
        request_id: id,
        status: supplementReady ? "succeeded" : "unknown",
        result: runMessages[0],
      });
    if (id === handoffRequest) {
      if (!handoffReceiptReady) return json({ request_id: id, status: "unknown" });
      if (!issue.pending_handoff_id) {
        issue.pending_handoff_id = "handoff-one";
        issue.pending_assignee = { kind: "owner", ref: "owner" };
        issue.assignment_state = "draining";
        issue.revision++;
      }
      return json({ request_id: id, status: "succeeded", result: issue });
    }
    if (id === takeRequest) {
      if (!takeReceiptReady) return json({ request_id: id, status: "unknown" });
      if (activeTask.control_mode !== "human_control") {
        activeTask.control_mode = "human_control";
        activeTask.control_revision++;
      }
      return json({ request_id: id, status: "succeeded", result: activeTask });
    }
    if (id === issueCreateRequest && !issueCreateReady)
      return json({ request_id: id, status: "unknown" });
    if (sourceReceipts.has(id))
      return json(
        id === sourceRequest && !sourceReceiptReady
          ? { ...(sourceReceipts.get(id) as object), status: "unknown" }
          : sourceReceipts.get(id),
      );
    if (p.endsWith(lostID) && releaseReceipt) {
      action = { ...action, status: "resolved", revision: action.revision + 1 };
      publish();
      return json({ request_id: lostID, status: "succeeded", result: action });
    }
    return json({ error: { code: "internal", message: "Expected fixture receipt outage" } }, 503);
  }
  if (p.startsWith("/api/"))
    return json({ error: { code: "not_found", message: "Not in fixture" } }, 404);
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
async function captureS4() {
  assert(output);
  assert(address && typeof address !== "string");
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    const context = await browser.newContext({ viewport, serviceWorkers: "block" });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${address.port}/`);
    await page.getByRole("textbox", { name: "消息", exact: true }).waitFor();
    for (const colorScheme of viewport.width === 1440
      ? (["light", "dark"] as const)
      : (["light"] as const)) {
      await page.emulateMedia({ colorScheme });
      for (const [name, path] of [
        ["board", "/issues"],
        ["hierarchy", "/issues?view=tree"],
        ["issue-active", "/issues/issue-root"],
        ["issue-closed", "/issues/issue-root"],
        ["inbox", "/inbox"],
        ["inbox-detail", "/inbox/attention-decision"],
        ["settings", "/settings"],
        ["tasks", "/tasks"],
        ["usage", "/usage"],
        ["repos", "/repos"],
        ["hosts", "/hosts"],
        ["archive", "/archive"],
        ["agents", "/settings/agents"],
      ]) {
        issue.state = name === "issue-closed" ? "closed" : "active";
        if (name === "issue-closed") await navigate(page, "/issues");
        await navigate(page, path!);
        await page.locator("main h1:visible").waitFor();
        if (
          [
            "board",
            "hierarchy",
            "settings",
            "tasks",
            "usage",
            "repos",
            "hosts",
            ...(viewport.width === 1440 ? ["inbox", "inbox-detail"] : []),
          ].includes(name!)
        ) {
          assert.equal(
            await page
              .locator("main")
              .getByRole("button", { name: /^(← 返回|返回对话|返回Issue)$/ })
              .filter({ visible: true })
              .count(),
            0,
            "top-level pages have no back controls",
          );
        }
        if (name === "usage") await page.getByText("9,000", { exact: true }).first().waitFor();
        if (name === "repos") await page.getByText("已读取登记。", { exact: true }).waitFor();
        if (name === "settings") await page.getByLabel("空闲分钟", { exact: true }).waitFor();
        if (name === "agents") await page.getByRole("button", { name: /青竹 planner/ }).waitFor();
        // Wait for actual fixture content rather than a title-only loading commit.
        if (name!.startsWith("issue-"))
          await page.getByRole("heading", { name: issue.plan.title, exact: true }).waitFor();
        if (name === "board")
          await page.getByRole("button", { name: "发布流程整理", exact: true }).waitFor();
        if (name === "hierarchy")
          await page.getByText("发布流程整理", { exact: true }).last().waitFor();
        if (name!.startsWith("inbox"))
          await page
            .locator("main")
            .getByText(decision.title, { exact: true })
            .filter({ visible: true })
            .first()
            .waitFor();
        if (name === "board") {
          if (viewport.width === 390) {
            const create = (await page
              .getByRole("button", { name: "新建 Issue", exact: true })
              .boundingBox())!;
            const filter = (await page
              .getByRole("button", { name: "筛选", exact: true })
              .boundingBox())!;
            assert.equal(create.height, filter.height, "phone toolbar buttons use one height");
            assert.equal(create.y, filter.y, "phone toolbar actions share a row");
          }
          assert(
            await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
            "board has no horizontal page overflow",
          );
          if (viewport.width === 1440) {
            const last = await page
              .getByRole("region", { name: "已取消列", exact: true })
              .boundingBox();
            assert(
              last && last.x + last.width <= viewport.width,
              "last board column is fully visible",
            );
            const toggle = page.getByRole("button", { name: /已取消/ }).filter({ visible: true });
            assert.equal(await toggle.getAttribute("aria-expanded"), "false");
            await toggle.click();
            const expanded = await page
              .getByRole("region", { name: "已取消列", exact: true })
              .boundingBox();
            assert(
              expanded && expanded.x + expanded.width <= viewport.width,
              "expanded last column is fully visible",
            );
            await page.screenshot({
              path: join(output, `board-expanded-${viewport.width}-${colorScheme}.png`),
            });
            await toggle.click();
          } else {
            const selectedTab = page.getByRole("tab", { selected: true });
            const tab = await selectedTab.boundingBox();
            assert(
              tab && tab.x >= 0 && tab.x + tab.width <= viewport.width,
              "active phone status tab is fully visible",
            );
          }
        }
        if (name === "inbox") {
          if (viewport.width > 760)
            await page.waitForURL((url) => url.pathname === `/inbox/${information.attention_id}`);
          else assert.equal(new URL(page.url()).pathname, "/inbox", "phone keeps the inbox list");
        }
        if (name!.startsWith("issue-")) {
          const auxiliary = await issueSidebar(page);
          assert.equal(
            await auxiliary
              .locator("dt")
              .filter({ hasText: /^状态$/ })
              .count(),
            1,
            "one issue status row",
          );
          assert.equal(await auxiliary.getByText("看板状态", { exact: true }).count(), 0);
          const status = (await auxiliary.locator("[data-issue-status]").innerText()).trim();
          assert(
            await page.locator("main p").filter({ hasText: status }).count(),
            "header and sidebar share the issue status label",
          );
          if (viewport.width <= 760)
            await page
              .getByRole("dialog", { name: "Issue辅助信息", exact: true })
              .getByRole("button", { name: /^关闭/ })
              .first()
              .click();
        }
        await page.evaluate(() => document.fonts.ready);
        await page.mouse.move(0, 0);
        await page.screenshot({
          path: join(output, `${name}-${viewport.width}-${colorScheme}.png`),
        });
        if (name === "board" && viewport.width === 390) {
          await page.getByRole("button", { name: "筛选", exact: true }).click();
          const sheet = page.getByRole("dialog", { name: "筛选 Issue", exact: true });
          assert(
            await sheet.evaluate((element) => element.scrollWidth <= element.clientWidth),
            "filter Sheet does not overflow",
          );
          const checkbox = await sheet.getByRole("checkbox").boundingBox();
          assert(checkbox && checkbox.width < 30, "checkbox remains a compact control");
          await page.screenshot({ path: join(output, "board-filters-390-light.png") });
          await sheet.getByRole("button", { name: "完成", exact: true }).click();
        }
      }
    }
    await context.close();
  }
  issue.state = "active";
}

async function checkNavigation() {
  assert(output);
  assert(address && typeof address !== "string");
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 900, height: 800 },
    { width: 761, height: 800 },
    { width: 760, height: 844 },
    { width: 390, height: 844 },
  ]) {
    const context = await browser.newContext({ viewport, serviceWorkers: "block" });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    await page.goto(`http://127.0.0.1:${address.port}/`);
    await page.getByRole("textbox", { name: "消息", exact: true }).waitFor();
    const sidebar = page.locator("aside").first();
    const tabs = page.getByRole("navigation", { name: "视图", exact: true });
    const mobile = viewport.width <= 760;
    const width = mobile ? 0 : (await sidebar.boundingBox())!.width;
    assert.equal(await sidebar.isVisible(), !mobile);
    assert.equal(await tabs.isVisible(), mobile);
    if (!mobile) {
      assert.equal(width, viewport.width < 1024 ? 64 : 288);
      assert.equal(await sidebar.getByRole("link").count(), 8);
      assert.equal(await sidebar.getByRole("region", { name: "需要你处理" }).count(), 0);
      assert.equal(await sidebar.getByRole("region", { name: "持续跟进" }).count(), 0);
      assert.equal(await sidebar.getByRole("region", { name: "执行主机占用" }).count(), 0);
      const issues = sidebar.getByRole("link", { name: "Issues", exact: true });
      assert.equal(await issues.getAttribute("title"), "Issues");
      if (viewport.width < 1024) {
        const label = await issues.locator("span").first().boundingBox();
        assert(label && label.width <= 1, "rail labels stay visually hidden");
      }
    } else assert.equal(await tabs.getByRole("link").count(), 4);
    const nav = mobile ? tabs : page.getByRole("navigation", { name: "主导航", exact: true });
    await nav
      .getByRole("link", { name: "收件箱", exact: true })
      .locator("span")
      .last()
      .getByText("2", { exact: true })
      .waitFor();
    assert.equal(
      (
        await nav
          .getByRole("link", { name: "收件箱", exact: true })
          .locator("span")
          .last()
          .innerText()
      ).trim(),
      "2",
    );
    const inboxLink = nav.getByRole("link", { name: "收件箱", exact: true });
    assert.equal(await inboxLink.getAttribute("aria-description"), "2 项待处理");
    inboxMore = true;
    publish();
    await page.waitForFunction(() =>
      Array.from(document.querySelectorAll('nav a[href="/inbox"]')).every(
        (link) => link.getAttribute("aria-description") === "2+ 项待处理",
      ),
    );
    assert.equal(await inboxLink.getAttribute("aria-description"), "2+ 项待处理");
    await page.screenshot({ path: join(output, `inbox-more-${viewport.width}.png`) });
    inboxMore = false;
    publish();
    await page.waitForFunction(() =>
      Array.from(document.querySelectorAll('nav a[href="/inbox"]')).every(
        (link) => link.getAttribute("aria-description") === "2 项待处理",
      ),
    );
    if (!mobile) {
      assert.notEqual(
        await sidebar.getByRole("link", { name: "Issues", exact: true }).locator("svg").innerHTML(),
        await sidebar
          .getByRole("link", { name: "任务执行", exact: true })
          .locator("svg")
          .innerHTML(),
      );
      await sidebar.getByRole("status", { name: "已连接", exact: true }).waitFor();
    }
    for (const path of [
      "/",
      "/issues",
      "/issues/issue-root",
      "/settings",
      "/inbox",
      "/inbox/attention-decision",
      `/tasks/${activeTask.task_id}`,
      "/more",
    ]) {
      if (path === "/more" && !mobile) {
        await page.evaluate(() =>
          (window as unknown as { tendNavigate(path: string): void }).tendNavigate("/more"),
        );
        await page.waitForURL((url) => url.pathname === "/");
      } else await navigate(page, path);
      await page.locator("main h1:visible").waitFor();
      if (path === "/inbox" && !mobile)
        await page.waitForURL((url) => url.pathname === `/inbox/${information.attention_id}`);
      if (path === "/issues/issue-root") {
        await issueSidebar(page);
        const auxiliary = page.getByRole("dialog", { name: "Issue辅助信息", exact: true });
        if (await auxiliary.isVisible())
          await auxiliary.getByRole("button", { name: /^关闭/ }).first().click();
      }
      const detail = [
        "/issues/issue-root",
        "/inbox/attention-decision",
        `/tasks/${activeTask.task_id}`,
      ].includes(path);
      assert.equal(await tabs.isVisible(), mobile && !detail, `tabs ${viewport.width} ${path}`);
      if (!mobile) {
        assert.equal((await sidebar.boundingBox())!.width, width, `sidebar width ${path}`);
        if (path === "/issues/issue-root")
          assert.equal(
            await sidebar
              .getByRole("link", { name: "Issues", exact: true })
              .getAttribute("aria-current"),
            "page",
          );
      } else if (!detail) {
        const main = (await page.locator("main").boundingBox())!;
        const bar = (await tabs.boundingBox())!;
        assert(main.y + main.height <= bar.y + 1, `content avoids tabs ${path}`);
      }
      if (path === "/more" && mobile) {
        const more = page.getByRole("navigation", { name: "更多导航", exact: true });
        assert.equal(await more.getByRole("link").count(), 5);
        await more.getByRole("link", { name: "设置", exact: true }).click();
        await page.getByRole("radiogroup", { name: "主题", exact: true }).waitFor();
      }
    }
    await navigate(page, "/settings");
    const theme = page.getByRole("radiogroup", { name: "主题", exact: true });
    await theme.getByRole("radio", { name: "深色", exact: true }).click();
    assert(await page.locator("body").evaluate((el) => el.hasAttribute("data-dark")));
    await page.reload();
    await theme.getByRole("radio", { name: "深色", exact: true }).waitFor();
    assert.equal(
      await theme.getByRole("radio", { name: "深色", exact: true }).getAttribute("aria-checked"),
      "true",
    );
    await theme.getByRole("radio", { name: "跟随系统", exact: true }).click();
    assert.equal(await page.evaluate(() => localStorage.getItem("tend.theme")), null);
    await navigate(page, "/");
    const head = page.locator("main header:visible").first();
    if (mobile) assert((await head.boundingBox())!.height <= 60, "phone chat header stays one row");
    assert.equal(
      await head
        .locator("h1")
        .getByRole("button", { name: /^切换会话：/ })
        .count(),
      1,
    );
    assert.equal(
      await head.getByText("主 Agent", { exact: true }).count(),
      1,
      "conversation title is not duplicated",
    );
    if (mobile) {
      const status = head.getByRole("status", { name: "已连接", exact: true });
      await status.waitFor();
      const name = (await head.locator("h1").boundingBox())!;
      const dot = (await status.boundingBox())!;
      assert(
        dot.x >= name.x + name.width && dot.x - name.x - name.width <= 8,
        "connection dot sits beside title",
      );
    }
    assert.equal(await head.getByRole("button", { name: "跳到最新", exact: true }).count(), 0);
    await head.getByRole("button", { name: "更多", exact: true }).click();
    const menu = page.getByRole("menu", { name: "更多", exact: true });
    await menu.getByRole("menuitem", { name: "补充来源", exact: true }).waitFor();
    assert.equal(await menu.getByRole("menuitem").count(), 2);
    await navigate(page, "/issues");
    await menu.waitFor({ state: "hidden" });
    await navigate(page, "/");
    assert.equal(
      await head.getByRole("button", { name: "更多", exact: true }).getAttribute("aria-expanded"),
      "false",
    );

    if (viewport.width === 390) {
      for (const [index, title] of [
        "发布流程和回滚文档整体设计咨询",
        "发布流程和回滚文档整体设计咨询：验收证据与后续执行策略的完整说明",
      ].entries()) {
        consultationID = `consultation:header-${index}`;
        consultationTitle = title;
        await navigate(page, `/chat/${encodeURIComponent(consultationID)}`);
        const trigger = head.getByRole("button", { name: `切换会话：${title}`, exact: true });
        await trigger.waitFor();
        const geometry = await trigger.evaluate((button) => {
          const label = button.querySelector("span")!;
          const chevron = button.querySelector("svg")!;
          const more = button.closest("header")!.querySelector('[aria-label="更多"]')!;
          const b = button.getBoundingClientRect(),
            l = label.getBoundingClientRect(),
            c = chevron.getBoundingClientRect(),
            m = more.getBoundingClientRect();
          return {
            display: getComputedStyle(button).display,
            minWidth: getComputedStyle(label).minWidth,
            ellipsis: getComputedStyle(label).textOverflow,
            clipped: label.scrollWidth > label.clientWidth,
            buttonRight: b.right,
            moreLeft: m.left,
            chevronRight: c.right,
            centreDelta: Math.abs((l.top + l.bottom - c.top - c.bottom) / 2),
            overflow: document.documentElement.scrollWidth > innerWidth,
          };
        });
        assert.equal(geometry.display, "inline-flex");
        assert.equal(geometry.minWidth, "0px");
        assert.equal(geometry.ellipsis, "ellipsis");
        if (index === 1) assert(geometry.clipped, "very long title is ellipsized");
        assert(geometry.buttonRight <= geometry.moreLeft, "title does not overlap More");
        assert(
          geometry.chevronRight <= geometry.buttonRight && geometry.centreDelta <= 2,
          "chevron stays in the same centred row",
        );
        assert.equal(geometry.overflow, false, "long consultation cannot overflow phone width");
        for (const colorScheme of ["light", "dark"] as const) {
          await page.emulateMedia({ colorScheme });
          await page.screenshot({
            path: join(output, `consultation-${index === 1 ? "long-" : ""}390-${colorScheme}.png`),
          });
        }
      }
      consultationID = "";
      consultationTitle = "发布方式咨询";
    }
    await context.close();
    console.log(`PASS grouped navigation ${viewport.width}`);
  }
}

try {
  const tourOnly = process.argv.includes("--s4-tour-only");
  if (tourOnly || !process.argv.includes("--navigation-only")) await captureS4();
  const navigationOnly = process.argv.includes("--navigation-only");
  if (navigationOnly) await checkNavigation();
  for (const viewport of navigationOnly || tourOnly
    ? []
    : [
        { width: 1440, height: 1000 },
        { width: 390, height: 844 },
      ]) {
    messageAssociations = {
      ...messageAssociations,
      revision: 3,
      disposition: "linked",
      items: [
        { issue_id: "issue-root", title: "发布流程整理", source: "owner", owner_locked: true },
      ],
    };
    decision = {
      ...baseAttention,
      attention_id: "attention-decision",
      kind: "decision",
      title: "确认灰度发布范围",
      question_version: 3,
      question_options: ["小范围灰度", "完整灰度范围"],
    };
    action = {
      ...baseAttention,
      attention_id: "attention-action",
      kind: "action",
      title: "提供验收设备清单",
    };
    manualSources.clear();
    replyBody = "我会统筹目标与下一步。当前需要你确认发布范围；相关任务按约定实施与独立验收。";
    replyRevision = 1;
    lostID = "";
    releaseReceipt = false;
    writes.length = 0;
    importedIssue = undefined;
    taskQueued = false;
    sourceContentRevision = 7;
    issueCreateRequest = "";
    issueCreateReady = false;
    delete information.archived_at;
    activeTask.control_mode = "managed";
    activeTask.control_revision = 7;
    runMessages = [];
    supplementRequest = "";
    supplementReady = false;
    issue.plan = {
      title: "发布流程整理",
      project: "sample",
      intent: "让发布步骤、验收证据与回滚入口清晰可追踪。",
      scope: "发布与回滚文档",
      completion_criteria: ["步骤可核对"],
      stages: [],
    };
    takeRequest = "";
    takeReceiptReady = false;
    sourceReceipts.clear();
    uploadCount = 0;
    sourceCount = 0;
    sourceRequest = "";
    sourceReceiptReady = false;
    consultationID = "";
    rejectSource = false;
    issue.assignee = assignee;
    issue.manager = { task_id: "task-manager", category_id: "issue-manager", state: "queued" };
    delete issue.pending_handoff_id;
    delete issue.pending_assignee;
    issue.assignment_state = "active";
    handoffRequest = "";
    handoffReceiptReady = false;
    const consoleErrors: string[] = [];
    const httpErrors: { path: string; status: number }[] = [];
    const context = await browser.newContext({
      viewport,
      isMobile: viewport.width < 760,
      hasTouch: viewport.width < 760,
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (message) => {
      if (message.type() === "error") consoleErrors.push(message.text());
    });
    page.on("response", (response) => {
      if (response.status() >= 400)
        httpErrors.push({ path: new URL(response.url()).pathname, status: response.status() });
    });
    hostedUnconfigured = true;
    await page.goto(`http://127.0.0.1:${address.port}`);
    await page.getByRole("textbox", { name: "消息", exact: true }).waitFor();
    const mobile = viewport.width < 760;
    await page.getByRole("button", { name: /^切换会话：/ }).click();
    await page
      .getByRole("dialog", { name: "会话", exact: true })
      .getByRole("button", { name: "Agent 与类别", exact: true })
      .click();
    const emptyCatalog = page.getByRole("region", { name: "Agent 与类别", exact: true });
    await emptyCatalog.getByText("尚未配置托管Agent", { exact: true }).waitFor();
    assert.equal(await emptyCatalog.getByText(/部分登记暂时无法读取/).count(), 0);
    await page.screenshot({ path: join(output, `catalog-unconfigured-${viewport.width}.png`) });
    await emptyCatalog.getByRole("button", { name: "← 返回", exact: true }).click();
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "Issues", exact: true })
        .click();
    else await page.getByRole("link", { name: "Issues", exact: true }).click();
    const emptyRead = page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/api/v1/categories",
    );
    await page.getByRole("button", { name: "新建 Issue", exact: true }).click();
    await emptyRead;
    const createForm = page.getByRole("region", { name: "新建Issue", exact: true });
    const managerSelect = createForm.getByRole("combobox", { name: "负责人", exact: true });
    assert.equal(await managerSelect.locator('option[value="category:issue-manager"]').count(), 1);
    assert.equal(await managerSelect.locator('option[value="hosted/qingzhu"]').count(), 0);
    assert.equal(await managerSelect.inputValue(), "owner");
    await page.screenshot({ path: join(output, `create-owner-only-${viewport.width}.png`) });
    await createForm.getByRole("button", { name: "返回", exact: true }).click();
    await navigate(page, "/");
    hostedUnconfigured = false;
    await page.getByRole("button", { name: /^切换会话：/ }).click();
    await page
      .getByRole("dialog", { name: "会话", exact: true })
      .getByRole("button", { name: "Agent 与类别", exact: true })
      .click();
    await emptyCatalog.getByRole("button", { name: /青竹 planner/ }).waitFor();
    assert.equal(await emptyCatalog.getByText("尚未配置托管Agent", { exact: true }).count(), 0);
    await page.screenshot({ path: join(output, `catalog-configured-${viewport.width}.png`) });
    await emptyCatalog.getByRole("button", { name: "← 返回", exact: true }).click();
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "Issues", exact: true })
        .click();
    else await page.getByRole("link", { name: "Issues", exact: true }).click();
    await page.getByRole("button", { name: "新建 Issue", exact: true }).click();
    await managerSelect
      .locator('option[value="category:custom-coordinator"]')
      .waitFor({ state: "attached" });
    await managerSelect.selectOption("category:custom-coordinator");
    assert.equal(await managerSelect.inputValue(), "category:custom-coordinator");
    await createForm.getByRole("button", { name: "返回", exact: true }).click();
    await navigate(page, "/");
    if (catalogOnly) {
      assert.deepEqual(errors, []);
      assert.deepEqual(httpErrors, []);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      await writeFile(
        join(output, `catalog-observations-${viewport.width}.json`),
        JSON.stringify({ consoleErrors, httpErrors }, null, 2),
      );
      await context.close();
      console.log(
        `PASS ${viewport.width}: unconfigured hosted catalog; owner-only Issue form; configured catalog and manager selection`,
      );
      continue;
    }
    await page
      .getByRole("complementary", { name: "当前待处理", exact: true })
      .getByText("确认灰度发布范围", { exact: true })
      .waitFor();
    await page
      .locator('[data-scroll-id="owner-plan"]')
      .getByRole("button", { name: "发布流程整理", exact: true })
      .waitFor();
    assert.equal(
      await page
        .getByRole("complementary", { name: "当前待处理" })
        .getByText(information.title, { exact: true })
        .count(),
      0,
    );
    assert.equal(
      await page
        .getByRole("complementary", { name: "当前待处理", exact: true })
        .getByText(information.title, { exact: true })
        .count(),
      0,
    );
    assert.match(
      await page
        .getByRole("navigation", { name: mobile ? "视图" : "主导航", exact: true })
        .getByRole("link", { name: "收件箱", exact: true })
        .innerText(),
      /2/,
    );
    await page.screenshot({ path: join(output, `main-${viewport.width}.png`) });
    if (mobile) {
      await navigate(page, "/inbox");
      const list = page.getByRole("region", { name: "待处理事项", exact: true });
      const opener = list.getByRole("button", { name: /发布进度已更新/ });
      await opener.click();
      await page.waitForURL((url) => url.pathname === `/inbox/${information.attention_id}`);
      await list.getByRole("button", { name: "← 返回", exact: true }).click();
      await page.waitForURL((url) => url.pathname === "/inbox");
      await page.waitForFunction(() => {
        const active = document.activeElement;
        return active?.tagName === "BUTTON" && active.textContent?.includes("发布进度已更新");
      });
      assert(
        await opener.evaluate((button) => button === document.activeElement),
        "phone Inbox back restores the originating row focus",
      );
      await page.goBack();
      await page.waitForURL((url) => url.pathname === "/");
      assert.equal(new URL(page.url()).pathname, "/", "next back leaves Inbox, not reopens detail");
    } else {
      await navigate(page, "/inbox");
      const list = page.getByRole("region", { name: "待处理事项", exact: true });
      await list.getByRole("button", { name: "查看所属 Issue →", exact: true }).click();
      await page.waitForURL((url) => url.pathname === `/issues/${issue.issue_id}`);
      await page
        .getByRole("region", { name: "Issue详情", exact: true })
        .getByRole("navigation", { name: "Issue路径", exact: true })
        .getByRole("button", { name: "Issues", exact: true })
        .click();
      await page.waitForURL((url) => url.pathname === "/issues");
      assert.equal(
        new URL(page.url()).pathname,
        "/issues",
        "root crumb goes to Issue list, not Inbox",
      );
      await navigate(page, "/");
    }
    await page
      .getByRole("complementary", { name: "当前待处理", exact: true })
      .getByRole("link", { name: "查看并处理", exact: true })
      .click();
    await page.getByRole("button", { name: "稍后处理", exact: true }).click();
    await page.waitForURL((url) => url.pathname === "/inbox");
    assert.equal(new URL(page.url()).pathname, "/inbox");
    await page
      .getByRole("region", { name: "待处理事项", exact: true })
      .getByRole("button", { name: /发布进度已更新/ })
      .click();
    await page.getByRole("button", { name: "稍后处理", exact: true }).click();
    await page.waitForURL((url) => url.pathname === "/inbox");
    assert.equal(new URL(page.url()).pathname, "/inbox", "defer always opens the list");
    await page.goBack();
    await page.waitForURL((url) => url.pathname.startsWith("/inbox/"));
    await page.goForward();
    await page.waitForURL((url) => url.pathname === "/inbox");
    const inboxList = page.getByRole("region", { name: "待处理事项", exact: true });
    await inboxList.getByRole("button", { name: /发布进度已更新/ }).waitFor();
    assert.equal(
      await inboxList.getAttribute("data-selected"),
      "false",
      "forward preserves deferral",
    );
    await navigate(page, "/settings");
    await page.getByLabel("空闲分钟", { exact: true }).waitFor();
    await page.goBack();
    await page.waitForURL((url) => url.pathname === "/inbox");
    await inboxList.getByRole("button", { name: /发布进度已更新/ }).waitFor();
    assert.equal(
      await inboxList.getAttribute("data-selected"),
      "false",
      "return preserves deferral",
    );
    await navigate(page, "/");
    replyBody = "实际流式回复：先核对证据，再安排下一步。";
    replyRevision++;
    for (const stream of streams)
      stream.write(
        `id: ${++seq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: seq, object_type: "message", object_id: "reply-plan", kind: "conversation.assistant_delta", created_at: new Date().toISOString(), item: { message_id: "reply-plan", seq: 2, kind: "message", author: "main_agent", conversation_id: "main", reply_to: "owner-plan", body: replyBody, assistant_revision: replyRevision, created_at: new Date(now).toISOString(), associations: { message_id: "reply-plan", revision: 4, content_revision: 1, disposition: "linked", facts_reviewed: true, owner_locked: false, items: [{ work_item_id: "legacy-only", title: "不应出现的历史标签", source: "agent", owner_locked: false }] } } })}\n\n`,
      );
    await page.getByText(replyBody, { exact: true }).waitFor();
    await page
      .locator('[data-scroll-id="reply-plan"]')
      .getByRole("button", { name: "发布流程整理", exact: true })
      .waitFor();
    assert.equal(
      await page.getByRole("button", { name: "不应出现的历史标签", exact: true }).count(),
      0,
    );
    const reconnected = page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/api/v1/events",
    );
    for (const stream of [...streams]) stream.end();
    await reconnected;
    await page
      .locator('[data-scroll-id="reply-plan"]')
      .getByRole("button", { name: "发布流程整理", exact: true })
      .waitFor();
    const ownerBubble = page.locator('[data-scroll-id="owner-plan"]');
    await ownerBubble.getByRole("button", { name: "修改Issue关联", exact: true }).click();
    const associationEditor = page.getByRole("dialog", { name: "修改Issue关联", exact: true });
    await associationEditor.getByRole("checkbox", { name: "补齐回滚文档", exact: true }).check();
    await associationEditor.getByRole("button", { name: "保存关联", exact: true }).click();
    await ownerBubble.getByRole("button", { name: "补齐回滚文档", exact: true }).waitFor();
    const associationWrite = writes.find(
      (w) => w.operation === "conversation.set_issue_associations",
    )!;
    assert.equal(associationWrite.expected_control_revision, 3);
    assert.deepEqual((associationWrite.payload as { issue_ids: string[] }).issue_ids, [
      "issue-root",
      "issue-child",
    ]);
    await ownerBubble.getByRole("button", { name: "发布流程整理", exact: true }).click();
    assert.equal(
      decodeURIComponent(new URL(page.url()).pathname.slice("/issues/".length)),
      "issue-root",
    );
    const pendingSide = await issueSidebar(page);
    await pendingSide.getByText(/等待 runner 领取/).waitFor();
    await pendingSide.getByRole("button", { name: "issue-manager", exact: true }).waitFor();
    await page.screenshot({ path: join(output, `manager-queued-${viewport.width}.png`) });
    const pendingManagerDialog = await openIssueAux(page, "交接负责人");
    await pendingManagerDialog
      .getByRole("combobox", { name: "交接给", exact: true })
      .selectOption("owner");
    await pendingManagerDialog
      .getByRole("textbox", { name: "交接说明", exact: true })
      .fill("撤回尚未领取的 manager 责任");
    assert(
      await pendingManagerDialog.getByRole("button", { name: "发起交接", exact: true }).isEnabled(),
    );
    await pendingManagerDialog.getByRole("button", { name: "返回Issue", exact: true }).click();
    await (
      await issueSidebar(page)
    )
      .getByRole("button", { name: "issue-manager", exact: true })
      .click();
    const managerDialog = page.getByRole("region", { name: "任务详情", exact: true });
    await managerDialog.getByText(/Issue 协调责任/).waitFor();
    await managerDialog.getByText("责任要求", { exact: true }).waitFor();
    assert.equal(
      await managerDialog.getByRole("button", { name: "交付与验收", exact: true }).count(),
      0,
    );
    assert.equal(
      await managerDialog.getByRole("button", { name: "工作区", exact: true }).count(),
      0,
    );
    await page.screenshot({ path: join(output, `manager-task-${viewport.width}.png`) });
    await managerDialog.getByRole("button", { name: /← 返回/ }).click();
    await closeIssue(page);
    await ownerBubble.getByRole("button", { name: "发布流程整理", exact: true }).click();
    // Subsequent owner-handoff scenarios use an already claimed manager.
    issue.manager!.host_id = "hosted";
    issue.manager!.state = "waiting";
    await closeIssue(page);
    writes.length = 0;
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "Issues", exact: true })
        .click();
    else await page.getByRole("link", { name: "Issues", exact: true }).click();
    await editIssueFilters(page, () =>
      page.getByRole("radio", { name: "层级", exact: true }).click(),
    );
    await page.getByRole("button", { name: "展开 发布流程整理", exact: true }).click();
    await page.getByRole("button", { name: "展开 补齐回滚文档", exact: true }).click();
    await page.getByRole("button", { name: "补齐失败示例", exact: true }).waitFor();
    await page.getByRole("button", { name: "发布流程整理", exact: true }).first().click();
    const detail = page;
    await detail.getByRole("heading", { name: "发布流程整理", exact: true }).waitFor();
    await page.screenshot({ path: join(output, `issue-${viewport.width}.png`) });
    const writesBeforeChat = writes.length;
    await page
      .getByRole("region", { name: "Issue详情", exact: true })
      .getByRole("textbox", { name: "消息", exact: true })
      .waitFor();
    assert.equal(writes.length, writesBeforeChat);
    await page.screenshot({ path: join(output, `issue-conversation-${viewport.width}.png`) });
    await openIssueAux(page, "编辑定义");
    await detail
      .getByRole("textbox", { name: "范围", exact: true })
      .fill("只整理发布与回滚文档，不改运行中的任务约定。");
    await detail
      .getByRole("textbox", { name: "完成条件 1", exact: true })
      .fill("发布步骤可重复核对");
    await detail.getByRole("button", { name: "＋ 添加完成条件", exact: true }).click();
    await detail.getByRole("textbox", { name: "完成条件 2", exact: true }).fill("回滚入口可定位");
    await detail
      .getByRole("textbox", { name: "约束（每行一项）", exact: true })
      .fill("保留全部原始证据");
    await page.screenshot({ path: join(output, `definition-criteria-${viewport.width}.png`) });
    await detail.getByRole("textbox", { name: "标题", exact: true }).scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(output, `definition-${viewport.width}.png`) });
    await detail.getByRole("button", { name: "保存定义", exact: true }).click();
    await page
      .getByRole("dialog", { name: "编辑Issue定义", exact: true })
      .waitFor({ state: "hidden" });
    await page
      .getByRole("region", { name: "Issue详情", exact: true })
      .getByRole("heading", { name: "发布流程整理", exact: true })
      .waitFor();
    const update = writes.find((w) => w.operation === "issues.update")!;
    assert.equal(
      (update.payload as { plan: IssuePlan }).plan.scope,
      "只整理发布与回滚文档，不改运行中的任务约定。",
    );
    assert.deepEqual((update.payload as { plan: IssuePlan }).plan.completion_criteria, [
      "发布步骤可重复核对",
      "回滚入口可定位",
    ]);
    assert.deepEqual((update.payload as { plan: IssuePlan }).plan.stages, []);
    assert.equal(update.expected_control_revision, issue.revision - 1);
    await openIssueAux(page, "跟进与交付");
    await detail.getByText("此安排由时间约定管理。", { exact: true }).waitFor();
    assert.equal(await detail.getByRole("button", { name: "现在检查", exact: true }).count(), 1);
    const followupResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/ops") &&
        response.request().postData()?.includes("issues.followups_trigger") === true,
    );
    await detail.getByRole("button", { name: "现在检查", exact: true }).click();
    await followupResponse;
    const followupWrite = writes.find((w) => w.operation === "issues.followups_trigger")!;
    assert.equal(followupWrite.target.id, "followup-manual");
    assert.equal(followupWrite.expected_control_revision, 1);
    assert.equal(
      (followupWrite.payload as { expected_assignment_epoch: number }).expected_assignment_epoch,
      issue.assignment_epoch,
    );
    await page.screenshot({ path: join(output, `issue-followups-${viewport.width}.png`) });
    await openIssueAux(page, "创建任务");
    await detail.getByRole("textbox", { name: "标题", exact: true }).fill("补齐回滚入口");
    await detail.getByRole("textbox", { name: "目标", exact: true }).fill("记录可核对的回滚步骤。");
    await detail
      .getByRole("textbox", { name: "完成条件（每行一项）", exact: true })
      .fill("独立验收确认步骤可用");
    await detail.getByRole("combobox", { name: /仓库/ }).selectOption("sample");
    await detail.getByRole("heading", { name: "执行要求", exact: true }).scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(output, `task-requirements-${viewport.width}.png`) });
    await detail.getByRole("button", { name: "创建任务", exact: true }).click();
    await detail.getByRole("heading", { name: "任务已创建", exact: true }).waitFor();
    assert.equal(
      await page
        .getByRole("dialog", { name: "创建任务", exact: true })
        .getByRole("textbox")
        .count(),
      0,
    );
    const taskWrite = writes.find((w) => w.operation === "issues.create_task")!;
    assert.deepEqual(
      (taskWrite.payload as { spec: { agent_selection: unknown } }).spec.agent_selection,
      { implementation: { category_id: "implementer" }, acceptance: { category_id: "reviewer" } },
    );
    await page.screenshot({ path: join(output, `task-receipt-${viewport.width}.png`) });
    writes.length = 0;
    await detail.getByRole("button", { name: "返回Issue", exact: true }).click();
    taskQueued = true;
    await (
      await issueSidebar(page)
    )
      .getByRole("button", { name: new RegExp(activeTask.title) })
      .first()
      .click();
    const taskDialog = page.getByRole("region", { name: "任务详情", exact: true });
    await taskDialog.getByRole("button", { name: "补充", exact: true }).click();
    await taskDialog.getByText("尚无执行记录。", { exact: true }).waitFor();
    taskQueued = false;
    for (const stream of streams)
      stream.write(
        `id: ${++seq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: seq, object_type: "task", object_id: activeTask.task_id, kind: "task.updated", created_at: new Date().toISOString() })}\n\n`,
      );
    await taskDialog.getByRole("textbox", { name: "补充给当前运行", exact: true }).waitFor();
    assert.equal(
      await taskDialog.getByRole("combobox", { name: "执行记录", exact: true }).inputValue(),
      "run-current",
    );
    await page.screenshot({ path: join(output, `queued-first-run-${viewport.width}.png`) });

    await taskDialog
      .getByRole("combobox", { name: "执行记录", exact: true })
      .selectOption("run-unrecorded");
    await taskDialog.getByText("运行版本未记录，不能发送精确补充。", { exact: true }).waitFor();
    for (const stream of streams)
      stream.write(
        `id: ${++seq}\nevent: ledger\ndata: ${JSON.stringify({ event_seq: seq, object_type: "task", object_id: activeTask.task_id, kind: "task.updated", created_at: new Date().toISOString() })}\n\n`,
      );
    await page.waitForTimeout(50);
    assert.equal(
      await taskDialog.getByRole("combobox", { name: "执行记录", exact: true }).inputValue(),
      "run-unrecorded",
    );

    assert(
      await taskDialog.getByRole("button", { name: "发送给当前run", exact: true }).isDisabled(),
    );
    await taskDialog
      .getByRole("combobox", { name: "执行记录", exact: true })
      .selectOption("run-current");
    const operationsBeforeView = writes.length;
    await taskDialog.getByRole("button", { name: "查看会话信息", exact: true }).click();
    await taskDialog.getByText("actual-session", { exact: true }).waitFor();
    assert.equal(writes.length, operationsBeforeView);
    await page.screenshot({ path: join(output, `run-native-${viewport.width}.png`) });
    await taskDialog
      .getByRole("textbox", { name: "补充给当前运行", exact: true })
      .fill("请保留原有回滚说明。");
    await taskDialog.getByRole("button", { name: "发送给当前run", exact: true }).click();
    await taskDialog.getByRole("button", { name: "核实原消息状态", exact: true }).waitFor();
    await page.screenshot({ path: join(output, `run-supplement-unknown-${viewport.width}.png`) });
    await taskDialog.getByRole("button", { name: "执行与日志", exact: true }).click();
    await taskDialog.getByRole("button", { name: "补充", exact: true }).click();
    await taskDialog.getByRole("button", { name: "核实原消息状态", exact: true }).waitFor();
    assert.equal(
      await taskDialog.getByRole("textbox", { name: "补充给当前运行", exact: true }).inputValue(),
      "请保留原有回滚说明。",
    );
    assert(
      await taskDialog.getByRole("textbox", { name: "补充给当前运行", exact: true }).isDisabled(),
    );
    supplementReady = true;
    await taskDialog.getByRole("button", { name: "核实原消息状态", exact: true }).click();
    await taskDialog.getByText("补充请求已记录。", { exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "runs.send_message").length, 1);
    const supplement = writes.find((w) => w.operation === "runs.send_message")!;
    assert.equal(supplement.target.id, "run-current");
    assert.equal(supplement.expected_control_revision, 7);
    assert.deepEqual(supplement.payload, {
      task_id: activeTask.task_id,
      spec_version: 2,
      generation: 3,
      text: "请保留原有回滚说明。",
    });
    await taskDialog.getByRole("button", { name: "接管", exact: true }).click();
    await taskDialog.getByRole("button", { name: "核实原管理操作", exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "control.set_mode").length, 1);
    await page.screenshot({ path: join(output, `run-control-unknown-${viewport.width}.png`) });
    takeReceiptReady = true;
    await taskDialog.getByRole("button", { name: "核实原管理操作", exact: true }).click();
    await taskDialog.getByRole("button", { name: "交还给 tend", exact: true }).waitFor();
    assert(
      await taskDialog.getByRole("textbox", { name: "补充给当前运行", exact: true }).isDisabled(),
    );
    await taskDialog.getByRole("button", { name: "交还给 tend", exact: true }).click();
    await taskDialog.getByRole("button", { name: "接管", exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "control.set_mode").length, 2);
    writes.length = 0;
    await taskDialog.getByRole("button", { name: /← 返回/ }).click();
    await closeIssue(page);
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "收件箱", exact: true })
        .click();
    else {
      if (await page.getByRole("button", { name: "返回对话", exact: true }).isVisible())
        await navigate(page, "/");
      await page
        .getByRole("navigation", { name: "主导航", exact: true })
        .getByRole("link", { name: "收件箱", exact: true })
        .click();
    }
    const inbox = page.getByRole("region", { name: "待处理事项", exact: true });
    await inbox.getByRole("tab", { name: "通知", exact: true }).click();
    await inbox.getByRole("button", { name: /发布进度已更新/ }).waitFor();
    await page.screenshot({ path: join(output, `information-filter-${viewport.width}.png`) });
    await inbox.getByRole("tab", { name: "全部", exact: true }).click();
    await inbox.getByRole("button", { name: /发布进度已更新/ }).waitFor();

    await inbox.getByRole("tab", { name: "待行动", exact: true }).click();
    await inbox.getByRole("button", { name: /提供验收设备清单/ }).click();
    await inbox.getByRole("textbox").fill("设备A：Android 14，明天上午可用。");
    await inbox.getByRole("button", { name: "提交处理结果", exact: true }).click();
    await inbox.getByText("提交结果待确认，请勿重复提交。", { exact: false }).waitFor();
    assert.equal(writes.length, 1);
    const requestID = writes[0]!.request_id;
    await page.screenshot({ path: join(output, `inbox-unknown-${viewport.width}.png`) });
    await inbox.getByRole("button", { name: mobile ? "← 返回" : "稍后处理", exact: true }).click();
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "收件箱", exact: true })
        .click();
    else {
      await page
        .getByRole("navigation", { name: "主导航", exact: true })
        .getByRole("link", { name: "收件箱", exact: true })
        .click();
    }
    await inbox.getByRole("tab", { name: "待行动", exact: true }).click();
    await inbox.getByRole("button", { name: /提供验收设备清单/ }).click();
    await inbox.getByRole("button", { name: "核实原提交结果", exact: true }).waitFor();
    assert.equal(
      await inbox.getByRole("textbox").inputValue(),
      "设备A：Android 14，明天上午可用。",
    );
    assert(await inbox.getByRole("textbox").isDisabled());
    releaseReceipt = true;
    await inbox.getByRole("button", { name: "核实原提交结果", exact: true }).click();
    await inbox
      .locator("p")
      .filter({ hasText: /^待行动 · 已处理$/ })
      .waitFor();
    assert.equal(writes.length, 1);
    assert.equal(writes[0]!.request_id, requestID);
    await inbox.getByRole("button", { name: mobile ? "← 返回" : "稍后处理", exact: true }).click();
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "对话", exact: true })
        .click();
    if (await page.getByRole("button", { name: "主 Agent", exact: true }).first().isVisible())
      await page.getByRole("button", { name: "主 Agent", exact: true }).first().click();
    await navigate(page, "/");
    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("menuitem", { name: "补充来源", exact: true }).click();
    let source = page.getByRole("dialog", { name: "补充来源", exact: true });
    await source
      .getByRole("textbox", { name: "原文", exact: true })
      .fill("原始文档内容，不是执行指令。");
    await source.getByRole("textbox", { name: "作者", exact: true }).fill("文档作者");
    await source.getByLabel("选择原文件", { exact: true }).setInputFiles({
      name: "evidence.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("original file bytes"),
    });
    let releaseUpload!: () => void;
    uploadGate = new Promise<void>((done) => {
      releaseUpload = done;
    });
    await source.getByRole("button", { name: "保存来源", exact: true }).click();
    const sourceReceipt = source.getByRole("button", { name: "核实原来源提交", exact: true });
    await sourceReceipt.waitFor();
    // The label changes when enqueued, before the upload and its receipt settle.
    assert(await sourceReceipt.isDisabled());
    assert.equal(uploadCount, 0);
    releaseUpload();
    await source
      .locator("button:enabled")
      .filter({ hasText: /^核实原来源提交$/ })
      .waitFor();
    assert.equal(uploadCount, 1);
    assert.equal(sourceCount, 0);
    await page.reload();
    await page.getByRole("textbox", { name: "消息", exact: true }).waitFor();
    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("menuitem", { name: "补充来源", exact: true }).click();
    source = page.getByRole("dialog", { name: "补充来源", exact: true });
    await source.getByRole("textbox", { name: "原文", exact: true }).waitFor();
    await page.waitForFunction(
      () =>
        document.querySelector('[aria-label="手动补充来源"] textarea')?.getAttribute("disabled") !==
        null,
    );
    await source.getByRole("button", { name: "核实原来源提交", exact: true }).waitFor();
    // Reopening resolves the original upload, then the exact source request.
    await source.getByText("evidence.txt", { exact: true }).waitFor();
    sourceReceiptReady = true;
    await source.getByRole("button", { name: "核实原来源提交", exact: true }).click();
    await source.getByText("来源已保存。原件与来源信息会保留供核对。", { exact: true }).waitFor();
    assert.equal(uploadCount, 1);
    assert.equal(sourceCount, 1);
    const sourceWrite = writes.find((w) => w.operation === "conversation.inject_source")!;
    assert.deepEqual(sourceWrite.payload, {
      text: "原始文档内容，不是执行指令。",
      metadata: { author: "文档作者" },
      attachments: ["source-file"],
    });
    assert.equal(writes.filter((w) => w.operation === "conversation.submit_message").length, 0);
    await page.screenshot({ path: join(output, `source-receipt-${viewport.width}.png`) });
    await source.getByRole("button", { name: "返回会话", exact: true }).click();
    await page.getByRole("button", { name: /^切换会话：/ }).click();
    const drawer = page.getByRole("dialog", { name: "会话", exact: true });
    await drawer.getByRole("button", { name: "新建咨询", exact: true }).click();
    await drawer.getByRole("combobox", { name: /咨询Agent/ }).selectOption("hosted/qingzhu");
    await drawer.getByLabel("标题", { exact: true }).fill("发布方式咨询");
    await drawer.getByRole("button", { name: "开始咨询", exact: true }).click();
    await page.getByRole("heading", { name: "发布方式咨询", exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "conversation.consult").length, 1);
    assert.equal(writes.filter((w) => w.operation.startsWith("issues.")).length, 0);
    await page.screenshot({ path: join(output, `consultation-${viewport.width}.png`) });
    await page
      .getByRole("button", { name: /^切换会话：/ })
      .last()
      .click();
    await drawer.getByRole("button", { name: "历史归档", exact: true }).click();
    const archives = page.getByRole("region", { name: "历史归档", exact: true });
    await archives.getByText("历史发布验证", { exact: true }).first().waitFor();
    await archives.getByRole("button", { name: "原生历史与缺口", exact: true }).click();
    await archives.getByText("未收录 · 未收录原生会话目录", { exact: true }).waitFor();
    await page.screenshot({ path: join(output, `archive-resources-${viewport.width}.png`) });
    await archives.getByRole("button", { name: "原文件与报告", exact: true }).click();
    await archives.getByRole("checkbox", { name: "选择原件 验收报告.md", exact: true }).check();
    await archives.getByRole("button", { name: "查看原文", exact: true }).click();
    await archives
      .getByRole("region", { name: "验收报告.md 原文", exact: true })
      .getByText("original file bytes", { exact: true })
      .waitFor();
    await page.screenshot({ path: join(output, `archive-originals-${viewport.width}.png`) });
    await archives.getByRole("button", { name: "从所选材料新建Issue（1）", exact: true }).click();
    await archives.getByRole("button", { name: "保存所选材料并继续", exact: true }).click();
    await archives.getByRole("button", { name: "核实原来源并继续", exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "issues.create").length, 0);
    await page.screenshot({ path: join(output, `archive-source-unknown-${viewport.width}.png`) });
    await page.reload();
    await page.getByRole("heading", { name: "历史归档", exact: true }).waitFor();
    assert.equal(new URL(page.url()).pathname, "/archive");
    await archives.getByRole("button", { name: "核实未完成的材料提交", exact: true }).click();
    await archives.getByRole("button", { name: "核实原来源并继续", exact: true }).click();
    await archives.getByRole("heading", { name: "新建Issue", exact: true }).waitFor();
    await archives.getByRole("textbox", { name: "标题", exact: true }).fill("从历史证据推进新发布");
    await archives
      .getByRole("textbox", { name: "目标", exact: true })
      .fill("以选中报告作为背景，重新定义当前发布目标。");
    await archives
      .getByRole("textbox", { name: "完成条件 1", exact: true })
      .fill("当前任务独立验收通过");
    await archives
      .getByRole("combobox", { name: "负责人", exact: true })
      .selectOption("category:custom-coordinator");
    await page.screenshot({ path: join(output, `archive-new-issue-${viewport.width}.png`) });
    await archives.getByRole("button", { name: "创建Issue", exact: true }).click();
    await archives.getByRole("button", { name: "核实原保存请求", exact: true }).waitFor();
    const originalCreate = writes.find((w) => w.operation === "issues.create")!;
    sourceContentRevision = 8;
    await page.reload();
    await page.getByRole("heading", { name: "历史归档", exact: true }).waitFor();
    assert.equal(new URL(page.url()).pathname, "/archive");
    await archives
      .getByRole("button", { name: "继续使用已保存材料新建Issue", exact: true })
      .click();
    // The persisted source can hydrate before this click after a physical reload.
    await archives.getByRole("button", { name: /^(保存所选材料并继续|核实原来源并继续)$/ }).click();
    await archives.getByRole("button", { name: "核实原保存请求", exact: true }).waitFor();
    assert.equal(
      await archives.getByRole("combobox", { name: "负责人", exact: true }).inputValue(),
      "category:custom-coordinator",
    );
    issueCreateReady = true;
    await archives.getByRole("button", { name: "核实原保存请求", exact: true }).click();
    assert.equal(writes.filter((w) => w.operation === "issues.create").length, 1);
    assert.deepEqual(
      writes.find((w) => w.operation === "issues.create"),
      originalCreate,
    );

    await page
      .getByRole("region", { name: "Issue详情", exact: true })
      .getByRole("heading", { name: "从历史证据推进新发布", exact: true })
      .waitFor();
    await page.screenshot({ path: join(output, `archive-created-issue-${viewport.width}.png`) });
    assert.equal(uploadCount, 2);
    assert.equal(sourceCount, 2);
    const newIssueWrite = writes.find((w) => w.operation === "issues.create")!;
    assert.equal(
      (newIssueWrite.payload as { source_message_id: string }).source_message_id,
      "manual-source-2",
    );
    assert.equal(
      (newIssueWrite.payload as { expected_source_revision: number }).expected_source_revision,
      7,
    );
    assert.equal((newIssueWrite.payload as { assignee?: unknown }).assignee, undefined);
    assert.equal(
      (newIssueWrite.payload as { manager: { category_id: string } }).manager.category_id,
      "custom-coordinator",
    );
    assert(!JSON.stringify(newIssueWrite.payload).includes('"source_message_id":"old-report"'));
    assert.equal(writes.filter((w) => w.operation.startsWith("archive")).length, 0);
    await closeIssue(page);
    await navigate(page, "/");
    await page.getByRole("heading", { name: "主 Agent", exact: true }).waitFor();
    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("menuitem", { name: "补充来源", exact: true }).click();
    let rejected = page.getByRole("dialog", { name: "补充来源", exact: true });
    await rejected
      .getByRole("textbox", { name: "原文", exact: true })
      .fill("需要修改后重提的原始来源");
    await rejected.getByLabel("选择原文件", { exact: true }).setInputFiles({
      name: "evidence.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("original file bytes"),
    });
    rejectSource = true;
    await rejected.getByRole("button", { name: "保存来源", exact: true }).click();
    await rejected.getByRole("button", { name: "核实原来源提交", exact: true }).click();
    await rejected.getByText("fixture rejected source", { exact: true }).last().waitFor();
    const rejectedRequest = writes
      .filter((w) => w.operation === "conversation.inject_source")
      .at(-1)!.request_id;
    await rejected.getByRole("button", { name: /^关闭/ }).first().click();
    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("menuitem", { name: "补充来源", exact: true }).click();
    rejected = page.getByRole("dialog", { name: "补充来源", exact: true });
    await rejected.getByRole("button", { name: "取回原输入后修改", exact: true }).waitFor();
    assert.equal(
      await rejected.getByRole("textbox", { name: "原文", exact: true }).inputValue(),
      "需要修改后重提的原始来源",
    );
    await rejected.getByRole("button", { name: "取回原输入后修改", exact: true }).click();
    await rejected.getByRole("button", { name: "保存来源", exact: true }).waitFor();
    await rejected.getByRole("button", { name: /^关闭/ }).first().click();
    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("menuitem", { name: "补充来源", exact: true }).click();
    rejected = page.getByRole("dialog", { name: "补充来源", exact: true });
    await page.waitForFunction(() => {
      const input = document.querySelector(
        '[aria-label="手动补充来源"] textarea',
      ) as HTMLTextAreaElement | null;
      return input?.value === "需要修改后重提的原始来源" && !input.disabled;
    });
    assert.equal(
      await rejected.getByRole("textbox", { name: "原文", exact: true }).inputValue(),
      "需要修改后重提的原始来源",
    );
    await rejected.getByRole("textbox", { name: "原文", exact: true }).fill("修改后的真实来源");
    const uploadsBeforeResubmit = uploadCount;
    rejectSource = false;
    await rejected.getByRole("button", { name: "保存来源", exact: true }).click();
    await rejected.getByText("来源已保存。原件与来源信息会保留供核对。", { exact: true }).waitFor();
    assert.equal(uploadCount, uploadsBeforeResubmit);
    assert.notEqual(
      writes.filter((w) => w.operation === "conversation.inject_source").at(-1)!.request_id,
      rejectedRequest,
    );
    await rejected.getByRole("button", { name: "返回会话", exact: true }).click();
    await navigate(page, "/settings/agents");
    const catalogDialog = page.getByRole("region", { name: "Agent 与类别", exact: true });
    await catalogDialog
      .getByRole("button", { name: /松风 implementer/ })
      .first()
      .click();
    await catalogDialog.getByText("本地ID：songfeng", { exact: true }).waitFor();
    await catalogDialog.getByText("未观测", { exact: true }).waitFor();
    assert.equal(await catalogDialog.getByText("最近上报可用", { exact: true }).count(), 2);
    assert.equal(await catalogDialog.getByText("离线", { exact: true }).count(), 1);
    await page.screenshot({ path: join(output, `catalog-${viewport.width}.png`) });
    await catalogDialog.getByRole("button", { name: "实施 · implementer", exact: true }).click();
    await catalogDialog
      .getByRole("textbox", { name: "类别说明", exact: true })
      .fill("按任务冻结约定实施");
    await catalogDialog.getByRole("button", { name: "保存类别", exact: true }).click();
    await catalogDialog.getByText("类别已保存。", { exact: true }).waitFor();
    const categoryWrite = writes.find((w) => w.operation === "categories.put")!;
    assert.equal(categoryWrite.target.id, "implementer");
    assert.equal(categoryWrite.expected_control_revision, 1);
    await catalogDialog.getByRole("button", { name: "← 返回", exact: true }).click();
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "Issues", exact: true })
        .click();
    else await page.getByRole("link", { name: "Issues", exact: true }).click();
    await page.getByRole("button", { name: "发布流程整理", exact: true }).first().click();
    const handoffDialog = await openIssueAux(page, "交接负责人");
    const customCategory = handoffDialog
      .getByRole("combobox", { name: "交接给", exact: true })
      .getByRole("option", { name: /custom-coordinator/ });
    await customCategory.waitFor({ state: "attached" });
    await handoffDialog
      .getByRole("combobox", { name: "交接给", exact: true })
      .selectOption("category:custom-coordinator");
    assert.equal(
      await handoffDialog.getByRole("combobox", { name: "交接给", exact: true }).inputValue(),
      "category:custom-coordinator",
    );
    await handoffDialog
      .getByRole("combobox", { name: "交接给", exact: true })
      .selectOption("owner");
    await handoffDialog
      .getByRole("textbox", { name: "交接说明", exact: true })
      .fill("我来核对发布范围，保留已有任务与原件。");
    await handoffDialog.getByRole("button", { name: "发起交接", exact: true }).click();
    await handoffDialog.getByRole("button", { name: "核实原交接请求", exact: true }).waitFor();
    const originalHandoff = writes.find((w) => w.operation === "issues.assign")!;
    assert.deepEqual((originalHandoff.payload as { assignee: unknown }).assignee, {
      kind: "owner",
      ref: "owner",
    });
    await page.screenshot({ path: join(output, `handoff-unknown-${viewport.width}.png`) });
    await handoffDialog.getByRole("button", { name: "返回Issue", exact: true }).click();
    await openIssueAux(page, "交接负责人");
    await handoffDialog.getByRole("button", { name: "核实原交接请求", exact: true }).waitFor();
    assert.equal(
      await handoffDialog.getByRole("textbox", { name: "交接说明", exact: true }).inputValue(),
      "我来核对发布范围，保留已有任务与原件。",
    );
    handoffReceiptReady = true;
    await handoffDialog.getByRole("button", { name: "核实原交接请求", exact: true }).click();
    await handoffDialog.getByRole("button", { name: "继续交接", exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "issues.assign").length, 1);
    await page.reload();
    await openIssueAux(page, "交接负责人");
    assert.equal(
      await handoffDialog.getByRole("combobox", { name: "交接给", exact: true }).inputValue(),
      "owner",
    );
    await handoffDialog
      .getByRole("textbox", { name: "继续交接说明", exact: true })
      .fill("已核对旧操作，继续交给我负责。");
    await page.screenshot({ path: join(output, `handoff-pending-reload-${viewport.width}.png`) });
    await handoffDialog.getByRole("button", { name: "继续交接", exact: true }).click();
    await handoffDialog.getByRole("button", { name: "返回Issue", exact: true }).click();
    await (await issueSidebar(page)).getByText("你", { exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "issues.finish_assignment").length, 1);

    await closeIssue(page);
    action = {
      ...baseAttention,
      attention_id: "attention-action-file",
      kind: "action",
      title: "补充验收原件",
      question_version: 1,
    };
    publish();
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "收件箱", exact: true })
        .click();
    else {
      if (await page.getByRole("button", { name: "返回对话", exact: true }).isVisible())
        await navigate(page, "/");
      await page
        .getByRole("navigation", { name: "主导航", exact: true })
        .getByRole("link", { name: "收件箱", exact: true })
        .click();
    }
    const finalInbox = page.getByRole("region", { name: "待处理事项", exact: true });
    await finalInbox.getByRole("tab", { name: "待行动", exact: true }).click();
    await finalInbox.getByRole("button", { name: /补充验收原件/ }).click();
    await finalInbox
      .getByRole("textbox", { name: "处理结果", exact: true })
      .fill("设备A已完成核对，保留原始说明。");
    await finalInbox.getByLabel("补充原文件", { exact: true }).setInputFiles({
      name: "evidence.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("original file bytes"),
    });
    releaseReceipt = false;
    const beforeConfirm = writes.filter((w) => w.operation === "attentions.confirm").length;
    await finalInbox.getByRole("button", { name: "提交处理结果", exact: true }).click();
    await finalInbox.getByText("原件保存结果待核实；处理事项尚未解决。", { exact: true }).waitFor();
    assert.equal(writes.filter((w) => w.operation === "attentions.confirm").length, beforeConfirm);
    await finalInbox.getByRole("button", { name: "提交处理结果", exact: true }).click();
    await finalInbox.getByRole("button", { name: "核实原提交结果", exact: true }).waitFor();
    const fileConfirm = writes.filter((w) => w.operation === "attentions.confirm").at(-1)!;
    const sourceID = (fileConfirm.payload as { source_message_id: string }).source_message_id;
    assert(sourceID && manualSources.has(sourceID));
    releaseReceipt = true;
    await finalInbox.getByRole("button", { name: "核实原提交结果", exact: true }).click();
    await finalInbox.getByText("查看原件与来源信息", { exact: true }).click();
    await finalInbox.getByText("标注作者：未知 · 原始时间：未知", { exact: true }).waitFor();
    await finalInbox.getByText(`来源记录：${sourceID}`, { exact: false }).waitFor();
    await page.screenshot({ path: join(output, `inbox-action-original-${viewport.width}.png`) });
    if (mobile) await finalInbox.getByRole("button", { name: "← 返回", exact: true }).click();
    await finalInbox.getByRole("tab", { name: "待决策", exact: true }).click();
    await finalInbox.getByRole("button", { name: /确认灰度发布范围/ }).click();
    await finalInbox.getByRole("radio", { name: "小范围灰度", exact: true }).check();
    await finalInbox
      .getByRole("textbox", { name: "补充说明（可选）", exact: true })
      .fill("先验证回滚证据。");
    await page.screenshot({ path: join(output, `inbox-decision-${viewport.width}.png`) });
    await finalInbox.getByRole("button", { name: "提交决定", exact: true }).click();
    await finalInbox
      .locator("p")
      .filter({ hasText: /^待决策 · 已处理$/ })
      .waitFor();
    const answer = writes.filter((w) => w.operation === "attentions.respond").at(-1)!;
    assert.equal((answer.payload as { version: number }).version, 3);
    assert.equal(answer.target.id, decision.attention_id);
    if (mobile) await finalInbox.getByRole("button", { name: "← 返回", exact: true }).click();
    await finalInbox.getByRole("tab", { name: "通知", exact: true }).click();
    await finalInbox.getByRole("button", { name: /发布进度已更新/ }).click();
    await finalInbox.getByRole("button", { name: "收起", exact: true }).click();
    if (mobile) await finalInbox.getByRole("button", { name: "← 返回", exact: true }).click();
    await finalInbox
      .getByText("暂无此类事项。新的决定、行动与通知会显示在这里。", { exact: true })
      .waitFor();
    assert.equal(information.status, "open", "archiving information must not resolve it");
    await page.screenshot({ path: join(output, `inbox-empty-${viewport.width}.png`) });
    await navigate(page, "/archive");
    const archiveState = page.getByRole("region", { name: "历史归档", exact: true });
    await archiveState
      .getByRole("combobox", { name: "选择归档", exact: true })
      .selectOption("earlier-archive");
    await archiveState.getByText("当前格式暂不支持浏览，原档案仍保留。", { exact: true }).waitFor();
    await page.screenshot({ path: join(output, `archive-unsupported-${viewport.width}.png`) });
    const routeSmoke = await context.newPage();
    await routeSmoke.setViewportSize({
      width: viewport.width,
      height: viewport.width === 1440 ? 900 : 844,
    });
    const fixtureOrigin = new URL(page.url()).origin;
    routeSmoke.on("pageerror", (error) => errors.push(error.message));
    routeSmoke.on("console", (message) => {
      if (message.type() === "error") consoleErrors.push(message.text());
    });
    for (const [path, heading, name] of [
      ["/", "主 Agent", "main"],
      ["/issues", "所有 Issue", "issues"],
      ["/inbox", "待处理", "inbox"],
      ["/settings", "设置", "settings"],
      [`/issues/${encodeURIComponent(issue.issue_id)}`, issue.plan.title, "issue"],
      [`/tasks/${encodeURIComponent(activeTask.task_id)}`, "任务详情", "task"],
    ]) {
      await routeSmoke.goto(`${fixtureOrigin}${path}`);
      await routeSmoke
        .getByRole("heading", { name: heading!, exact: true, level: 1 })
        .waitFor()
        .catch(async (error: unknown) => {
          await writeFile(
            join(output, `route-failure-${viewport.width}.json`),
            JSON.stringify({
              path,
              errors,
              consoleErrors,
              body: await routeSmoke.locator("body").innerText(),
            }),
          );
          await routeSmoke.screenshot({
            path: join(output, `route-failure-${viewport.width}.png`),
          });
          throw error;
        });
      assert.equal(await routeSmoke.getByRole("heading", { level: 1 }).count(), 1);
      if (name === "inbox") {
        const description = routeSmoke.getByText("需要你决定或亲自推进的事项。", { exact: true });
        const padding = await description.evaluate(
          (element) => getComputedStyle(element).paddingLeft,
        );
        assert.equal(padding, viewport.width > 760 ? "24px" : "16px");
      }
      if (name === "main") await routeSmoke.locator('[data-scroll-id="owner-plan"]').waitFor();
      await routeSmoke.screenshot({ path: join(output, `s2-${name}-${viewport.width}.png`) });
      await routeSmoke.reload();
      await routeSmoke.getByRole("heading", { name: heading!, exact: true, level: 1 }).waitFor();
      const expectedPath =
        name === "inbox" && !mobile
          ? `/inbox/${[information, decision, action].find((item) => !item.archived_at)!.attention_id}`
          : path;
      await routeSmoke.waitForURL((url) => url.pathname === expectedPath);
      assert.equal(new URL(routeSmoke.url()).pathname, expectedPath);
      await routeSmoke.keyboard.press("Escape");
      assert.equal(
        new URL(routeSmoke.url()).pathname,
        expectedPath,
        "pages do not require Escape to leave",
      );
    }
    holdTaskFeed = true;
    const feedRequest = routeSmoke.waitForRequest((request) => {
      const url = new URL(request.url());
      return url.pathname === "/api/v1/tasks" && url.searchParams.get("limit") !== "5";
    });
    await navigate(routeSmoke, "/tasks");
    const requestedFeed = await feedRequest;
    const cancelledFeed = routeSmoke.waitForEvent("requestfailed", {
      predicate: (request) => request === requestedFeed,
    });
    await navigate(routeSmoke, "/");
    await cancelledFeed;
    holdTaskFeed = false;
    await routeSmoke.goto(`${fixtureOrigin}/?issue=${issue.issue_id}`);
    await routeSmoke
      .getByRole("heading", { name: issue.plan.title, exact: true, level: 1 })
      .waitFor();
    assert.equal(new URL(routeSmoke.url()).pathname, `/issues/${issue.issue_id}`);
    await routeSmoke.goto(`${fixtureOrigin}/?issue=${issue.issue_id}&task=${activeTask.task_id}`);
    await routeSmoke.getByRole("heading", { name: "任务详情", exact: true, level: 1 }).waitFor();
    assert.equal(new URL(routeSmoke.url()).pathname, `/tasks/${activeTask.task_id}`);
    assert.equal(new URL(routeSmoke.url()).searchParams.get("from"), issue.issue_id);
    await routeSmoke.goto(`${fixtureOrigin}/inbox/${action.attention_id}`);
    await routeSmoke.getByRole("heading", { name: "待处理", exact: true, level: 1 }).waitFor();
    await routeSmoke
      .getByRole("button", { name: mobile ? "← 返回" : "稍后处理", exact: true })
      .click();
    await routeSmoke.waitForURL(`${fixtureOrigin}/inbox`);
    await routeSmoke.close();
    assert(
      httpErrors.every(
        (error) =>
          error.status === 503 &&
          (error.path === "/api/v1/ops" || error.path.startsWith("/api/v1/requests/")),
      ),
    );
    await writeFile(
      join(output, `browser-observations-${viewport.width}.json`),
      JSON.stringify({ consoleErrors, httpErrors }, null, 2),
    );
    assert.deepEqual(errors, []);
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await context.close();
    console.log(
      `PASS ${viewport.width}: canonical message links; hierarchy/definition/task requirements; exact run/native view/control; Inbox decision/action originals; source unknown/refusal recovery; consultation; archive import; catalog/category; handoff unknown and physical reload`,
    );
  }
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
