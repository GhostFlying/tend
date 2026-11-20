import type { AttentionView, RespondAttentionPayload } from "../api/generated/api.ts";
import { optionLabel } from "../decisions/text.ts";

export function questionVersion(item: AttentionView) {
  const question = item.decision ?? item.choice;
  if (!question) return item.question_version ?? 0;
  return question.status === "answered" ? question.version - 1 : question.version;
}

export function decisionReceipt(item: AttentionView, version = questionVersion(item)) {
  if (item.kind !== "decision" || item.status !== "resolved" || version <= 0) return;
  const decision = item.decision;
  if (decision?.status === "answered" && decision.version === version + 1) {
    const answer = decision.answer;
    if (!answer || typeof answer !== "object") return;
    const record = answer as Record<string, unknown>;
    const option = typeof record.option === "string" ? record.option : "";
    const text = typeof record.text === "string" ? record.text : "";
    if (option || text) return { option, text };
  }
  const choice = item.choice;
  if (choice?.status === "answered" && choice.version === version + 1 && choice.answer) {
    const [first, ...rest] = choice.answer.split("\n\n");
    return choice.options?.includes(first!)
      ? { option: first!, text: rest.join("\n\n") }
      : { option: "", text: choice.answer };
  }
}

const deliveryLabels: Record<string, string> = {
  approve: "批准",
  defer: "延期",
  end_delivery: "结束交付",
};

export function isDeliveryDecision(item: AttentionView) {
  const options = item.question_options ?? item.decision?.options;
  return (
    Array.isArray(options) &&
    ["approve", "defer", "end_delivery"].every((id) => options.includes(id))
  );
}

export function attentionDecisionLabel(item: AttentionView, option: string) {
  return (
    (isDeliveryDecision(item) && deliveryLabels[option]) ||
    (item.decision ? optionLabel(item.decision, option) : option)
  );
}

export function decisionReceiptTitle(item: AttentionView, option: string) {
  if (isDeliveryDecision(item)) {
    if (option === "approve") return "批准已记录";
    if (option === "defer") return "延期决定已记录";
    if (option === "end_delivery") return "结束交付决定已记录";
  }
  return "答复已记录";
}

export function differsFromSubmission(
  item: AttentionView,
  receipt: { option: string; text: string },
  submitted: RespondAttentionPayload,
) {
  if (questionVersion(item) !== submitted.version) return false;
  if (item.choice) {
    return item.choice.answer !== (submitted.text ?? "");
  }
  return receipt.option !== (submitted.option ?? "") || receipt.text !== (submitted.text ?? "");
}

export function submittedExplanation(submitted: RespondAttentionPayload, legacyChoice: boolean) {
  const text = submitted.text ?? "";
  if (!legacyChoice || !submitted.option) return text;
  return text.slice(submitted.option.length).replace(/^\n\n/, "");
}
