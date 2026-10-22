// Answering a decision from a button (D71). An option tend carries out
// itself goes to decisions.respond, and tend records the answer and does
// it in one transaction. Any other option becomes a conversation message
// linked to the decision, which the main agent reads, records with
// decisions_resolve and acts on. Either way the answer names the version
// the owner saw; a decision that changed meanwhile is read again.
import type { AnswerResult, ConversationItem, DecisionView } from "../api/generated/api.ts";
import { notify } from "../app/notice.ts";
import { runOp } from "../app/ops.ts";
import type { Services } from "../app/services.tsx";
import type { OutgoingMessage } from "../queue/outbox.ts";
import { choiceOf } from "../queue/sendQueue.ts";
import { answerMessage, effectLabels, effectOf, optionLabel } from "./text.ts";

export async function answer(
  s: Services,
  d: DecisionView,
  option: string,
  taskTitle: string | undefined,
): Promise<void> {
  const effect = effectOf(d, option);
  if (!effect) {
    if (!s.queue) {
      notify("这个浏览器不能保存待发送的消息，请在对话里回答", "warn");
      return;
    }
    const lastSeq = s.live.conversation.get().items.at(-1)?.seq;
    await s.queue.enqueue({
      body: answerMessage(d, taskTitle, option),
      decision: { decision_id: d.decision_id, version: d.version, option },
      ...(lastSeq ? { lastSeenSeq: lastSeq } : {}),
    });
    return;
  }
  const outcome = await runOp(s.client, {
    operation: "decisions.respond",
    target: { type: "decision", id: d.decision_id },
    payload: { version: d.version, option },
  });
  switch (outcome.kind) {
    case "done": {
      const result = outcome.result as AnswerResult | undefined;
      const label = effectLabels[effect];
      if (result?.effect && !result.effect.applied) {
        const why = result.effect.reason ? `：${result.effect.reason}` : "";
        notify(`回答已记下，但 tend 没有执行「${label}」${why}`, "warn");
      } else {
        notify(`tend 已执行：${label}`);
      }
      break;
    }
    case "stale":
      notify("这个待决事项已经变了，已重新读取", "warn");
      break;
    case "refused":
      notify(`tend 没有接受「${optionLabel(d, option)}」：${outcome.message}`, "warn");
      break;
    case "unknown":
      notify(outcome.message, "warn");
      break;
  }
  void s.live.refreshSnapshot();
  if (d.task_id && s.live.detail.get()?.taskId === d.task_id) {
    void s.live.refreshDetail();
  }
}

/**
 * How a message answering the decision stands, if one does: pending until
 * the main agent's turn for it ends, failed if tend refused it (most
 * likely because the decision changed).
 */
export function messageAnswer(
  d: Pick<DecisionView, "decision_id" | "version">,
  items: readonly ConversationItem[],
  outbox: readonly OutgoingMessage[],
): { option: string; state: "pending" | "failed" } | undefined {
  const mine = (c: { decision_id: string; version: number } | undefined) =>
    c?.decision_id === d.decision_id && c.version === d.version;
  for (const it of items) {
    if (mine(it.decision) && (it.state === "queued" || it.state === "processing")) {
      return { option: it.decision!.option, state: "pending" };
    }
  }
  for (const m of outbox) {
    const c = choiceOf(m);
    if (!mine(c)) {
      continue;
    }
    if (m.state === "failed") {
      return { option: c!.option, state: "failed" };
    }
    // Sent and not in the conversation yet: still on its way.
    const arriving = m.state === "sent" && (m.seq ?? Infinity) > (items.at(-1)?.seq ?? 0);
    if (m.state === "sending" || m.state === "unknown" || arriving) {
      return { option: c!.option, state: "pending" };
    }
  }
  return undefined;
}
