import type {
  DeliveryObservation,
  WorkItemDelivery,
  WorkItemStage,
  ExecutionCounts,
  WorkItemView,
} from "../api/generated/api.ts";

export const columns = [
  { id: "unplanned", title: "待安排" },
  { id: "active", title: "推进中" },
  { id: "checking", title: "待检查" },
  { id: "attention", title: "待处理" },
  { id: "snoozed", title: "暂缓" },
  { id: "closed", title: "已结束" },
] as const;
export const columnLabel = (id: string) =>
  id === "historical" ? "历史计划" : (columns.find((c) => c.id === id)?.title ?? id);
export const stageLabels: Record<string, string> = {
  planned: "待推进",
  active: "进行中",
  done: "已完成",
  cancelled: "决定不做",
  retired: "已调整",
};
export const followOnLabels: Record<string, string> = {
  commitment: "承诺",
  problem: "问题",
  suggestion: "建议",
  open: "未处理",
  resolved: "已处理",
  transferred: "已承接",
};
export const closureLabels: Record<string, string> = {
  completed: "已完成",
  declined: "决定不做",
  handed_off: "已交接",
  superseded: "被替代",
};
export const factLabels: Record<string, string> = {
  goal: "目标",
  observation: "现象",
  user_constraint: "用户约束",
  decision: "已确认决定",
  input: "原始输入",
  fact: "事实",
};
export const deliveryLabels: Record<string, string> = {
  open: "开放",
  merged: "已合入",
  closed: "已关闭",
  unknown: "尚未确认",
};
const evidenceLabels: Record<string, string> = {
  acceptance: "独立验收",
  delivery: "交付核验",
  owner_decision: "用户决定",
};
const observationErrorLabels: Record<string, string> = {
  invalid_reference: "交付引用无效",
  unsupported_provider: "暂不支持此交付平台",
  cli_unavailable: "执行主机缺少查询工具",
  query_failed: "查询失败",
  invalid_response: "返回内容无法识别",
  not_found: "未找到交付对象",
  authentication_required: "需要登录或访问权限",
  unknown_state: "暂时无法确认交付状态",
  timeout: "查询超时",
};
export const evidenceLabel = (kind: string) => evidenceLabels[kind] ?? `其他依据（${kind}）`;
export const observationErrorLabel = (kind: string) =>
  observationErrorLabels[kind] ?? `未知错误（${kind}）`;

export function stageRows(
  stages: readonly WorkItemStage[],
): { stage: WorkItemStage; depth: number }[] {
  const result: { stage: WorkItemStage; depth: number }[] = [];
  const seen = new Set<string>();
  const byId = new Set(stages.map((s) => s.stage_id));
  const visit = (stage: WorkItemStage, depth: number) => {
    if (seen.has(stage.stage_id)) return;
    seen.add(stage.stage_id);
    result.push({ stage, depth });
    for (const child of stages) if (child.parent_id === stage.stage_id) visit(child, depth + 1);
  };
  for (const stage of stages) if (!stage.parent_id || !byId.has(stage.parent_id)) visit(stage, 0);
  for (const stage of stages) visit(stage, 0);
  return result;
}
export function parallelStages(stages: readonly WorkItemStage[]): ReadonlySet<string> {
  const byId = new Map(stages.map((stage) => [stage.stage_id, stage]));
  const available = stages.filter((stage) => {
    const visited = new Set<string>();
    let current: WorkItemStage | undefined = stage;
    while (current) {
      if (visited.has(current.stage_id)) return false;
      visited.add(current.stage_id);
      if (current.status !== "planned" && current.status !== "active") return false;
      if (!(current.depends_on ?? []).every((id) => byId.get(id)?.status === "done")) return false;
      current = current.parent_id ? byId.get(current.parent_id) : undefined;
    }
    return true;
  });
  return new Set(
    available
      .filter((s) =>
        available.some(
          (other) =>
            other.stage_id !== s.stage_id &&
            other.parent_id === s.parent_id &&
            !(other.depends_on ?? []).includes(s.stage_id) &&
            !(s.depends_on ?? []).includes(other.stage_id),
        ),
      )
      .map((s) => s.stage_id),
  );
}
export function concern(item: WorkItemView): string {
  const issue = item.follow_ons?.find((next) => next.status === "open" && next.kind === "problem");
  if (issue) return issue.text;
  const commitment = item.follow_ons?.find(
    (next) => next.status === "open" && next.kind === "commitment",
  );
  if (commitment) return commitment.text;
  if (item.status === "closed") return item.closure?.reason ?? "已结束跟进";
  if (item.status === "snoozed") return "已暂缓新领取；运行中的任务仍按原约定推进";
  const active = item.stages?.find((stage) => stage.status === "active");
  if (active) return active.title;
  const planned = item.stages?.find((stage) => stage.status === "planned");
  if (planned) return planned.title;
  return "本轮已结束，后续尚未安排";
}
export type WorkItemIntention = "discuss" | "close" | "snooze" | "resume" | "reopen";
export function intentionText(item: WorkItemView, intention: WorkItemIntention): string {
  const ref = `工作项 ${item.work_item_id}（${item.title}）`;
  switch (intention) {
    case "discuss":
      return `继续讨论${ref}。\n\n我想补充或调整：\n`;
    case "close":
      return `我想结束跟进${ref}。请先检查未处理承诺、运行任务和未确认操作，讨论收尾依据与遗留去向，再按账本规则处理。\n\n我的结束依据：\n`;
    case "snooze":
      return `我想暂缓${ref}的新领取安排。运行中的任务继续按原约定推进，不停止 run；请核对影响后再更新工作项。\n\n暂缓原因：\n`;
    case "resume":
      return `我想恢复${ref}的后续安排。请先核对最新事实和已有承诺，再讨论下一步。\n`;
    case "reopen":
      return `我想重新跟进${ref}。请保留原收尾记录和任务终态，讨论重开原因及新的责任范围。\n\n重开原因：\n`;
  }
}
export function splitWorkItemIds(text: string): (string | { workItemId: string })[] {
  const pattern = /(?<![0-9A-Za-z_])wi_[0-7][0-9A-HJKMNP-TV-Z]{25}(?![0-9A-Za-z_])/g;
  const out: (string | { workItemId: string })[] = [];
  let last = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > last) out.push(text.slice(last, match.index));
    out.push({ workItemId: match[0] });
    last = match.index + match[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Facts captured before a manual refresh whose transport result was unknown. */
export interface RefreshBaseline {
  target: string;
  generation: number;
  checkedAt: number;
  attemptedAt: number;
}
export function refreshBaseline(
  delivery: WorkItemDelivery,
  observation?: DeliveryObservation,
): RefreshBaseline {
  return {
    target: JSON.stringify([
      delivery.provider,
      delivery.repository,
      delivery.number,
      delivery.host_id ?? "",
    ]),
    generation: observation?.generation ?? 0,
    checkedAt: observation?.checked_at ?? 0,
    attemptedAt: observation?.attempted_at ?? 0,
  };
}
/** New completed facts make another explicit refresh safe; they do not prove request success. */
export function refreshReconciled(
  baseline: RefreshBaseline,
  delivery: WorkItemDelivery,
  observation?: DeliveryObservation,
): boolean {
  const current = refreshBaseline(delivery, observation);
  return (
    current.target !== baseline.target ||
    (!observation?.refresh_pending &&
      current.generation > baseline.generation &&
      (current.checkedAt > baseline.checkedAt || current.attemptedAt > baseline.attemptedAt))
  );
}

/** Execution completion does not decide whether the plan stage is finished. */
export function stageProgressLabel(
  stage: WorkItemStage,
  counts?: ExecutionCounts,
  pending = false,
): string {
  if ((stage.status !== "active" && stage.status !== "planned") || !counts)
    return stageLabels[stage.status] ?? stage.status;
  const live =
    (counts.recovering ?? 0) +
    counts.queued +
    counts.running +
    counts.accepting +
    counts.waiting_owner +
    counts.paused +
    counts.unknown +
    counts.idle;
  if (live > 0) return "推进中";
  if (counts.completed > 0)
    return pending ? "本轮执行已完成 · 阶段待核对" : "本轮执行已完成 · 后续责任未结束";
  if (counts.cancelled > 0) return "本轮已取消 · 后续待安排";
  return "待推进";
}

export function stageNeedsReview(item: WorkItemView, stageId: string): boolean {
  const stages = new Map(item.stages?.map((stage) => [stage.stage_id, stage]));
  const pendingTasks = new Set(item.pending_reviews?.map((review) => review.task_id));
  for (const stage of stages.values()) {
    if (!stage.task_ids?.some((id) => pendingTasks.has(id))) continue;
    let current: WorkItemStage | undefined = stage;
    const seen = new Set<string>();
    while (current && !seen.has(current.stage_id)) {
      if (current.stage_id === stageId) return true;
      seen.add(current.stage_id);
      current = current.parent_id ? stages.get(current.parent_id) : undefined;
    }
  }
  return false;
}
