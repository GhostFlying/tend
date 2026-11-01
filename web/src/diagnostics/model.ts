import type { TriggerEvent, TriggerSchedule } from "../api/generated/api.ts";
import type { Tone } from "../ui/Surfaces.tsx";
export const labels: Record<string, string> = {
  followup: "定时与条件跟进",
  observation: "交付观察",
  webhook: "Webhook",
  at: "指定时间",
  every: "固定间隔",
  cron: "日历定时",
  condition: "条件",
  active: "等待触发",
  pending: "等待分发",
  queued: "待处理",
  processing: "处理中",
  frozen: "已领取",
  claimed: "已领取",
  completed: "处理结束",
  finished: "安排结束",
  paused: "已暂停",
  failed: "失败",
  unknown: "待核对",
  cancelled: "已取消",
  not_dispatched: "未分发",
  received: "事件已接收",
  triggered: "触发已记录",
  routed: "已路由到 Issue",
  started: "处理已开始",
  starting: "等待确认开始",
  interrupted: "已中断",
  rejected: "未被接收",
};
export function tone(status: string): Tone {
  if (["failed", "interrupted"].includes(status)) return "err";
  if (["unknown", "paused"].includes(status)) return "warn";
  if (["completed", "finished"].includes(status)) return "ok";
  if (["processing", "queued", "pending", "claimed"].includes(status)) return "run";
  return "neutral";
}
export function timeLabel(ms?: number): string {
  return ms ? new Date(ms).toLocaleString("zh-CN", { hour12: false }) : "未记录";
}
export function scheduleLabel(row: TriggerSchedule): string {
  if (row.kind === "cron") return `${row.rule?.cron ?? ""} · ${row.rule?.timezone ?? ""}`;
  if (row.kind === "every") return `每 ${Math.round((row.rule?.every_seconds ?? 0) / 60)} 分钟`;
  return labels[row.kind] ?? row.kind;
}
export function reasonLabel(reason?: string): string {
  return (
    (
      {
        manual: "手动检查",
        every: "周期到期",
        at: "指定时间到期",
        cron: "日历时间到期",
        expired: "已到期限",
      } as Record<string, string>
    )[reason ?? ""] ??
    reason ??
    "未记录"
  );
}
export function issueStateLabel(state: string): string {
  return (
    (
      {
        active: "进行中",
        paused: "已暂停",
        cancelling: "取消中",
        cancelled: "已取消",
        closed: "已关闭",
      } as Record<string, string>
    )[state] ?? state
  );
}
export function eventSummary(row: TriggerEvent): string {
  if (row.status === "pending") return "触发已记录，等待 Issue 负责人可消费";

  return (
    reasonLabel(row.reason) ||
    (row.status === "pending" ? "等待 Issue 可分发或由 Agent 领取责任" : "展开查看关联记录")
  );
}
