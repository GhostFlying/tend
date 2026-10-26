import type { ExecutionCounts, TaskSummary } from "../api/generated/api.ts";
import type { Tone } from "../shell/StatusDot.tsx";

export function executionLook(execution: NonNullable<TaskSummary["execution"]>): {
  label: string;
  tone: Tone;
} {
  switch (execution.state) {
    case "queued":
      return { label: execution.role === "accept" ? "排队中 · 待验收" : "排队中", tone: "warn" };
    case "starting":
      return { label: execution.role === "accept" ? "正在启动验收" : "正在启动", tone: "run" };
    case "recovering":
      return {
        label: execution.recovery?.phase === "running" ? "恢复中" : "异常处理中",
        tone: "warn",
      };
    case "running":
      return { label: "执行中", tone: "run" };
    case "accepting":
      return { label: "验收中", tone: "run" };
    case "waiting_owner":
      return { label: "等待用户", tone: "warn" };
    case "paused":
      return { label: "已暂停", tone: "idle" };
    case "unknown":
      return { label: "执行待核对", tone: "warn" };
    case "completed":
      return { label: "已完成", tone: "ok" };
    case "cancelled":
      return { label: "已取消", tone: "idle" };
    default:
      return { label: "当前未执行", tone: "idle" };
  }
}
const countLabels = [
  ["starting", "正在启动"],
  ["recovering", "恢复或异常处理中"],
  ["running", "执行中"],
  ["accepting", "验收中"],
  ["queued", "排队中"],
  ["waiting_owner", "等待用户"],
  ["paused", "已暂停"],
  ["unknown", "执行待核对"],
  ["idle", "当前未执行"],
  ["completed", "已完成"],
  ["cancelled", "已取消"],
] as const;

/** Counts are server facts, never inferred from active plan stages or task lifecycle. */
export function executionCounts(counts: ExecutionCounts): { state: string; text: string }[] {
  return countLabels.flatMap(([state, label]) =>
    (counts[state] ?? 0) > 0 ? [{ state, text: `${counts[state]} ${label}` }] : [],
  );
}

const waitLabels = {
  waiting_decision: "等待用户决定",
  waiting_approval: "等待批准",
  acceptance_env_unavailable: "等待验收环境恢复",
  budget_exhausted: "等待调整实现次数",
  workspace_unavailable: "等待工作区恢复",
  waiting_resource: "等待资源",
  none: "等待任务条件满足",
};

/** Translate the server's chosen constraint; do not repeat scheduler predicates here. */
export function queueReasonText(task: Pick<TaskSummary, "execution" | "block_reason">): string {
  const startup = task.execution?.startup;
  if (startup && startup.phase !== "started") {
    const labels: Record<string, string> = {
      readiness_timeout: "等待 CLI 就绪超时",
      interactive_prompt: "CLI 等待交互处理",
      process_exited: "CLI 在启动时退出",
      write_failed: "提示写入失败",
      prompt_write_failed: "提示写入失败",
      delivery_unknown: "提示投递结果待核对",
    };
    const reason = labels[startup.code ?? ""] ?? "Worker 启动未完成";
    return `${reason}${startup.phase === "retry_pending" ? " · 正在确认旧终端停止后重试" : ""}${startup.attempts ? ` · 已自动重试 ${startup.attempts} 次` : ""}`;
  }
  const recovery = task.execution?.recovery;
  if (task.execution?.state === "recovering" && recovery) {
    const phase =
      recovery.phase === "running"
        ? "已启动恢复会话"
        : recovery.phase === "pending"
          ? "正在确认旧会话及后台操作已停止"
          : recovery.phase === "failed"
            ? "自动恢复失败，等待处理"
            : "模型轮次异常，正在核对执行状态";
    return `${phase}${recovery.attempts ? ` · 第 ${recovery.attempts} 次恢复` : ""}${recovery.code ? ` · ${recovery.code}` : ""}`;
  }
  const reason = task.execution?.queue_reason;
  if (!reason) {
    return task.execution?.state === "waiting_owner" && task.block_reason !== "none"
      ? waitLabels[task.block_reason]
      : "";
  }
  switch (reason.code) {
    case "ready":
      return "等待执行主机领取";
    case "workspace_busy":
      return "等待工作区释放";
    case "dependency":
      return "等待前序任务";
    case "host_capacity":
      return "等待主机空闲槽位";
    case "host_unavailable":
      return "等待执行主机恢复可用";
    case "host_requirements":
      return reason.message === "runner does not support scoped worker observation"
        ? "等待支持现场核验的执行主机"
        : "等待满足要求的执行主机";
    case "task_blocked":
      return waitLabels[task.block_reason] ?? reason.message;
    default:
      return reason.message;
  }
}
