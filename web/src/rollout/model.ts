import type { RunLogView, RunRolloutPage } from "../api/generated/api.ts";

export interface Step {
  key: string;
  seq: number;
  lastSeq: number;
  rows: RunLogView[];
  kind: "text" | "tool" | "reasoning" | "event";
  title: string;
  summary: string;
  text: string;
  input: string;
  output: string;
  status: string;
  durationMs: number | null;
  pending: boolean;
  failed: boolean;
  truncated: boolean;
  thread: string;
  turn: string;
  nativeID: string;
  gap: boolean;
}

export function object(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}
function string(value: unknown): string {
  return typeof value === "string" ? value : "";
}
function content(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  const parts = object(value).content;
  if (Array.isArray(parts)) {
    const text = parts.map((part) => string(object(part).text)).filter(Boolean);
    if (text.length) return text.join("\n");
  }
  return JSON.stringify(value, null, 2);
}
export function firstLine(text: string): string {
  return (
    text
      .trim()
      .split("\n")
      .find((line) => line.trim())
      ?.trim() ?? ""
  );
}

const titles: Record<string, string> = {
  agentMessage: "Agent 回复",
  commandExecution: "执行命令",
  mcpToolCall: "调用工具",
  dynamicToolCall: "调用工具",
  webSearch: "搜索",
  imageView: "查看图片",
  fileChange: "修改文件",
  collabAgentToolCall: "子 Agent",
  contextCompaction: "整理上下文",
  userMessage: "任务输入",
  reasoning: "思考",
};

/** Project durable records, pairing only explicit identities within one native scope. */
export function buildSteps(logs: readonly RunLogView[]): Step[] {
  const steps: Step[] = [];
  const items = new Map<string, Step>();
  let previous = 0;
  let gap = false;
  for (const row of logs) {
    gap ||= previous > 0 && row.seq > previous + 1;
    previous = row.seq;
    const envelope = object(row.body);
    const data = object(envelope.data ?? row.body);
    const event = string(envelope.kind) || row.kind;
    // Consumption facts are inspected with the ledger; they are not agent actions.
    if (event === "usage.request") continue;
    const orcaReply = /turn[._]completed$/.test(event) ? string(data.reply) : "";
    const type = string(data.type) || (orcaReply ? "agentMessage" : event);
    const thread = string(envelope.thread_id);
    const turn = string(envelope.turn_id);
    const nativeID = string(envelope.item_id) || string(data.id) || row.native_id || "";
    const isItem = /item[._](started|completed)$/.test(event);
    const identity =
      isItem && nativeID && thread && turn ? JSON.stringify([thread, turn, nativeID]) : "";
    let step = identity ? items.get(identity) : undefined;
    const started = /item[._]started$/.test(event);
    // A late/replayed start must not replace the completed payload.
    if (step && started && !step.pending) {
      step.rows.push(row);
      step.lastSeq = row.seq;
      step.gap ||= gap;
      gap = false;
      continue;
    }
    const prior = step;
    const text =
      string(data.text) ||
      orcaReply ||
      (type === "reasoning" && Array.isArray(data.summary)
        ? data.summary.map(content).join("\n")
        : "");
    const command = string(data.command);
    const tool = string(data.tool);
    const title =
      tool ||
      titles[type] ||
      (/turn[._]started$/.test(event)
        ? "开始执行"
        : /turn[._]completed$/.test(event)
          ? "本轮结束"
          : "执行记录");
    const input =
      command ||
      content(data.arguments) ||
      string(data.query) ||
      string(data.path) ||
      string(data.prompt) ||
      content(data.changes) ||
      content(data.action);
    const output =
      content(data.aggregatedOutput) ||
      content(data.result) ||
      content(data.contentItems) ||
      content(data.error);
    const status = string(data.status);
    const failed =
      status === "failed" ||
      status === "declined" ||
      data.success === false ||
      object(data.result).isError === true ||
      (data.error !== null && data.error !== undefined) ||
      (typeof data.exitCode === "number" && data.exitCode !== 0);
    const pending =
      started || status === "inProgress" || status === "running" || status === "backgrounded";
    const value: Step = {
      key: prior?.key ?? (identity ? `item-${encodeURIComponent(identity)}` : `step-${row.seq}`),
      seq: prior?.seq ?? row.seq,
      lastSeq: row.seq,
      rows: [...(prior?.rows ?? []), row],
      kind:
        type === "agentMessage"
          ? "text"
          : type === "reasoning"
            ? "reasoning"
            : titles[type] && !["userMessage", "contextCompaction"].includes(type)
              ? "tool"
              : "event",
      title,
      summary:
        firstLine(
          command ||
            string(object(data.arguments).path) ||
            string(object(data.arguments).query) ||
            text ||
            input ||
            output,
        ) ||
        prior?.summary ||
        title,
      text: text || prior?.text || "",
      input: input || prior?.input || "",
      output,
      status,
      durationMs:
        typeof data.durationMs === "number" &&
        Number.isFinite(data.durationMs) &&
        data.durationMs >= 0
          ? data.durationMs
          : (prior?.durationMs ?? null),
      pending,
      failed,
      truncated: data.truncated === true || prior?.truncated === true,
      thread,
      turn,
      nativeID,
      gap: gap || prior?.gap === true,
    };
    if (prior) Object.assign(prior, value);
    else {
      step = value;
      steps.push(step);
      if (identity) items.set(identity, step);
    }
    gap = false;
  }
  return steps;
}

/** Keep a bounded, ordered window without turning an empty poll into a lost cursor. */
export function mergePage(
  old: RunRolloutPage | null,
  next: RunRolloutPage,
  mode: "replace" | "before" | "after",
): RunRolloutPage {
  if (!old || mode === "replace") return next;
  const merged = [
    ...new Map([...(old.logs ?? []), ...(next.logs ?? [])].map((row) => [row.seq, row])).values(),
  ].sort((a, b) => a.seq - b.seq);
  const logs = mode === "before" ? merged.slice(0, 400) : merged.slice(-400);
  return {
    ...next,
    logs,
    before: logs[0]?.seq ?? old.before,
    after: logs.at(-1)?.seq ?? old.after,
    more_before:
      mode === "before" ? next.more_before : old.more_before || merged.length > logs.length,
    more_after: mode === "before" ? old.more_after || merged.length > logs.length : next.more_after,
  };
}

export function statusLabel(status: string): string {
  return (
    (
      {
        running: "执行中",
        starting: "启动中",
        completed: "已结束",
        finished: "已结束",
        interrupted: "已中断",
        waiting: "等待处理",
        failed: "执行失败",
        stopped: "已停止",
        unknown: "状态待确认",
      } as Record<string, string>
    )[status] ?? "等待执行"
  );
}
