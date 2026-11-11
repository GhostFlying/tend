// Recover intent from current business objects, never a transport receipt cache.
import { ApiError, type BusinessMessage } from "../api/client.ts";
import type {
  Envelope,
  TaskSummary,
  IssueView,
  AttentionView,
  IssueFollowupPage,
} from "../api/generated/api.ts";
import { newObjectId } from "../api/ids.ts";
import type { OpOutcome, OpSender } from "./ops.ts";

const creationPrefixes: Record<string, string> = {
  "tasks.create": "task",
  "issues.create": "wi",
  "work_items.create": "wi",
  "decisions.open": "dec",
  "attentions.create": "attn",
  "attentions.raise": "attn",
  "conversation.consult": "consultation:conversation",
  "planner.entries_put": "next",
  "issues.deadlines_put": "deadline",
};

/** Reserve the actual object key before persisting or transporting a creation. */
export function prepareOperation(envelope: Envelope, now = Date.now()): Envelope {
  const prefix =
    ["planner.entries_put", "issues.deadlines_put"].includes(envelope.operation) &&
    envelope.expected_control_revision !== undefined
      ? undefined
      : creationPrefixes[envelope.operation];
  let prepared =
    prefix && !envelope.target.id
      ? { ...envelope, target: { ...envelope.target, id: newObjectId(prefix, now) } }
      : envelope;
  const nested: Record<string, [string, string]> = {
    "issues.create_task": ["task_id", "task"],
    "webhooks.create": ["subscription_id", "webhook"],
    "issues.followups_create": ["rule_id", "followup"],
    "work_items.followups_create": ["rule_id", "followup"],
    "conversation.submit_message": ["message_id", "msg"],
    "conversation.inject_source": ["message_id", "msg"],
    "messaging.send": ["message_id", "msg"],
    "messaging.input": ["message_id", "msg"],
    "messaging.reply": ["message_id", "msg"],
    "messaging.notify": ["message_id", "msg"],
    "conversation.reply": ["message_id", "msg"],
    "runs.send_message": ["message_id", "msg"],
  };
  const field = nested[envelope.operation];
  if (field) {
    const payload = (envelope.payload ?? {}) as Record<string, unknown>;
    if (!payload[field[0]])
      prepared = { ...prepared, payload: { ...payload, [field[0]]: newObjectId(field[1], now) } };
  }
  return prepared;
}

const unknown = (): OpOutcome => ({
  kind: "unknown",
  message: "尚不能从业务记录确认结果，保留原操作继续核对。",
});
const stale = "已重新读取当前业务状态，请根据最新内容决定下一步；未确认原操作是否执行。";

/** Reading current state is not proof of a particular request's execution. */
export async function recoverOperation(sender: OpSender, e: Envelope): Promise<OpOutcome> {
  if (!sender.get) return unknown();
  const payload = (e.payload ?? {}) as Record<string, unknown>;
  const id = e.target.id;
  const encoded = encodeURIComponent(id ?? "");
  try {
    if (e.operation === "tasks.create" && id) {
      const result = await sender.get<TaskSummary>(`/api/v1/tasks/${encoded}`);
      return result?.task_id === id ? { kind: "done", result } : unknown();
    }
    if (["issues.create", "work_items.create"].includes(e.operation) && id) {
      const result = await sender.get<IssueView>(`/api/v1/issues/${encoded}`);
      return result?.issue_id === id ? { kind: "done", result } : unknown();
    }
    if (["attentions.create", "attentions.raise"].includes(e.operation) && id) {
      const result = await sender.get<AttentionView>(`/api/v1/attentions/${encoded}`);
      return result?.attention_id === id ? { kind: "done", result } : unknown();
    }
    if (e.operation === "issues.create_task" && typeof payload.task_id === "string") {
      const result = await sender.get<TaskSummary>(
        `/api/v1/tasks/${encodeURIComponent(payload.task_id)}`,
      );
      return result?.task_id === payload.task_id ? { kind: "done", result } : unknown();
    }
    if (
      ["conversation.submit_message", "conversation.inject_source", "messaging.send"].includes(
        e.operation,
      ) &&
      typeof payload.message_id === "string"
    ) {
      const message = await sender.get<BusinessMessage>(
        `/api/v1/messages/${encodeURIComponent(payload.message_id)}`,
      );
      return message?.message_id === payload.message_id
        ? { kind: "done", result: { message_id: message.message_id, seq: message.seq } }
        : unknown();
    }
    if (e.operation === "issues.followups_create" && id && typeof payload.rule_id === "string") {
      let after = "";
      for (;;) {
        const page = await sender.get<IssueFollowupPage>(
          `/api/v1/issues/${encoded}/followups?limit=100&after=${encodeURIComponent(after)}`,
        );
        const rule = page.items?.find((item) => item.rule_id === payload.rule_id);
        if (rule) return { kind: "done", result: rule };
        if (!page.more || !page.next || page.next === after) return unknown();
        after = page.next;
      }
    }
    if (["tasks.cancel", "control.set_mode"].includes(e.operation) && id) {
      const current = await sender.get<TaskSummary>(`/api/v1/tasks/${encoded}`);
      if (current?.task_id !== id) return unknown();
      if (
        (e.operation === "tasks.cancel" && current.lifecycle === "cancelled") ||
        (e.operation === "control.set_mode" && current.control_mode === payload.mode)
      )
        return { kind: "done", result: current };
      return { kind: "stale", message: stale, current };
    }
    if (
      [
        "issues.update",
        "issues.assign",
        "issues.pause",
        "issues.resume",
        "issues.close",
        "issues.cancel",
        "issues.reopen",
        "issues.reparent",
        "issues.set_status",
        "issues.set_notification_settings",
      ].includes(e.operation) &&
      id
    ) {
      const currentIssue = await sender.get<IssueView>(`/api/v1/issues/${encoded}`);
      return currentIssue?.issue_id === id
        ? { kind: "stale", message: stale, currentIssue }
        : unknown();
    }
    if (
      [
        "attentions.update",
        "attentions.withdraw",
        "attentions.present",
        "attentions.respond",
        "attentions.confirm",
      ].includes(e.operation) &&
      id
    ) {
      const currentAttention = await sender.get<AttentionView>(`/api/v1/attentions/${encoded}`);
      return currentAttention?.attention_id === id
        ? { kind: "stale", message: stale, currentAttention }
        : unknown();
    }
    if (e.operation === "conversation.set_settings") {
      const conversation = encodeURIComponent(
        typeof payload.conversation_id === "string" ? payload.conversation_id : "main",
      );
      const current = await sender.get<Record<string, unknown>>(
        `/api/v1/conversation/settings?conversation_id=${conversation}`,
      );
      const matches = Object.entries(payload).every(
        ([key, value]) => key === "conversation_id" || current[key] === value,
      );
      return matches ? { kind: "done", result: current } : { kind: "stale", message: stale };
    }
    if (
      ["conversation.interrupt_reply", "conversation.withdraw_message"].includes(e.operation) &&
      id
    ) {
      const current = await sender.get<BusinessMessage>(`/api/v1/messages/${encoded}`);
      if (current?.message_id !== id) return unknown();
      if (
        (e.operation === "conversation.withdraw_message" && current.control?.withdrawn_at) ||
        (e.operation === "conversation.interrupt_reply" &&
          ["interrupted", "replied", "failed"].includes(current.state ?? ""))
      )
        return { kind: "done", result: current };
      return unknown();
    }
    if (e.operation === "work_item_choices.respond" && id) {
      await sender.get(`/api/v1/work-item-choices/${encoded}`);
      return { kind: "stale", message: stale };
    }
    if (e.operation === "issues.refresh_delivery" && id) {
      await sender.get(`/api/v1/issues/${encoded}/deliveries`);
      return {
        kind: "stale",
        message:
          "已重新读取当前交付事实；原刷新动作是否执行仍未确认，可按当前状态决定是否再次观察。",
      };
    }
    // Remote effects and unobserved operations remain unknown. An absent
    // object or newer unrelated version never authorizes an automatic replay.
    return unknown();
  } catch (err) {
    if (err instanceof ApiError && err.code === "not_found") return unknown();
    throw err;
  }
}
