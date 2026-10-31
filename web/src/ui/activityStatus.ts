import type { IssueConversationItem, RunLogView } from "../api/generated/api.ts";
import { toolLabel } from "../conversation/toolLabels.ts";
import { buildSteps, object } from "../rollout/model.ts";

export interface ActivityStatus {
  active: boolean;
  label: string;
  steps: number;
  startedAt: number | null;
  endedAt: number | null;
  tone: "normal" | "error" | "warning";
}

/** Labels describe the latest meaningful activity, never a fabricated tool. */
export function stageLabel(type: string, name = ""): string {
  const key = name.replaceAll(".", "_").toLowerCase();
  if (type === "text" || type === "agentMessage") return "正在回复";
  if (type === "reasoning") return "思考中";
  if (type === "commandExecution" || /(^|_)(bash|exec|exec_command|shell|terminal)$/.test(key))
    return "正在执行命令";
  if (type === "webSearch" || /web_?search|search_web/.test(key)) return "正在搜索网页";
  if (/^(read|glob)$|read_file|files_read|download_file/.test(key)) return "正在读取文件";
  if (/^(grep|rg)$|search_code|code_search/.test(key)) return "正在搜索代码";
  if (type === "fileChange" || /^(edit|write|multi_edit)$|apply_patch/.test(key))
    return "正在修改文件";
  return name ? `正在调用 ${toolLabel(name)}` : "思考中";
}

export function elapsedLabel(milliseconds: number): string {
  const seconds = Math.floor(Math.max(0, Number.isFinite(milliseconds) ? milliseconds : 0) / 1000);
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

function timestamp(value: string | undefined): number | null {
  const time = Date.parse(value ?? "");
  return Number.isFinite(time) ? time : null;
}

export function terminalLabel(state: string): string {
  if (state === "failed") return "执行失败";
  if (["cancelled", "stopped", "interrupted"].includes(state)) return "已停止";
  if (state === "unknown") return "结果待确认";
  if (["completed", "finished", "replied", "processed"].includes(state)) return "已完成";
  return "等待处理";
}

export function activitySummary(status: ActivityStatus, now: number): string {
  const endedAt = status.endedAt ?? (Number.isFinite(now) ? now : null);
  const elapsed =
    status.startedAt === null || endedAt === null
      ? " · 用时未知"
      : ` · 用时 ${elapsedLabel(endedAt - status.startedAt)}`;
  return `${status.label} · ${status.steps} 个步骤${elapsed}`;
}

export function conversationStatus(
  items: readonly IssueConversationItem[],
  state?: string,
  startedAt: number | null = null,
  endedAt: number | null = null,
): ActivityStatus {
  const latest = items.at(-1);
  const active =
    state === "processing" ||
    (!state &&
      items.some(
        (item) => item.assistant_state === "streaming" || item.tool?.status === "running",
      ));
  const terminal =
    state ??
    items.findLast((item) => item.assistant_state && item.assistant_state !== "completed")
      ?.assistant_state ??
    "completed";
  return {
    active,
    label: active
      ? stageLabel(latest?.tool ? "tool" : latest?.body ? "text" : "reasoning", latest?.tool?.name)
      : terminalLabel(terminal),
    steps: items.filter((item) => item.tool).length,
    startedAt: startedAt ?? timestamp(items[0]?.created_at),
    endedAt,
    tone: active
      ? "normal"
      : terminal === "failed"
        ? "error"
        : ["unknown", "interrupted", "stopped", "cancelled"].includes(terminal)
          ? "warning"
          : "normal",
  };
}

/** Run timestamps come from durable records, not the time the view was mounted. */
export function runActivityStatus(logs: readonly RunLogView[], state: string): ActivityStatus {
  const steps = buildSteps(logs);
  const latest = steps.toSorted((a, b) => a.lastSeq - b.lastSeq).at(-1);
  const row = latest?.rows.at(-1);
  const envelope = object(row?.body);
  const data = object(envelope.data ?? row?.body);
  const type = typeof data.type === "string" ? data.type : (latest?.kind ?? "");
  const name = typeof data.tool === "string" ? data.tool : "";
  const active = ["running", "starting"].includes(state);
  const start = logs.find((row) => /^(?:native[._])?turn[._]started$/.test(row.kind));
  const end = logs.findLast((row) => /^(?:native[._])?turn[._]completed$/.test(row.kind));
  return {
    active,
    label: active ? stageLabel(type, name) : terminalLabel(state),
    steps: steps.filter((step) => !/turn[._](started|completed)$/.test(step.rows[0]?.kind ?? ""))
      .length,
    startedAt: timestamp(start?.created_at),
    endedAt: active ? null : timestamp(end?.created_at),
    tone:
      state === "failed"
        ? "error"
        : ["stopped", "interrupted", "unknown", "cancelled"].includes(state)
          ? "warning"
          : "normal",
  };
}
