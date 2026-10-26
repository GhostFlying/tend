import type { Envelope, WorkItemChoice } from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";
import { runOp, settleOp, type OpOutcome, type OpSender } from "../app/ops.ts";
import { Store } from "../state/store.ts";

export interface ChoiceResponseState {
  pending: boolean;
  message: string;
  unknown?: { envelope: Envelope; missing: boolean };
}
export class ChoiceResponse {
  readonly state = new Store<ChoiceResponseState>({ pending: false, message: "" });
  constructor(
    private sender: OpSender,
    private changed: () => void,
  ) {}
  async answer(choice: WorkItemChoice, answer: string) {
    if (
      choice.status !== "open" ||
      !answer.trim() ||
      this.state.get().pending ||
      this.state.get().unknown
    )
      return;
    await this.attempt({
      request_id: newRequestId(Date.now()),
      operation: "work_item_choices.respond",
      target: { type: "work_item_choice", id: choice.choice_id },
      expected_control_revision: choice.version,
      payload: { answer: answer.trim() },
    });
  }
  async check() {
    const before = this.state.get();
    if (before.pending || !before.unknown) return;
    this.state.set({ ...before, pending: true });
    try {
      const receipt = await this.sender.lookup(before.unknown.envelope.request_id);
      if (receipt)
        this.settle(
          settleOp(receipt.status, receipt.result, receipt.error),
          before.unknown.envelope,
        );
      else
        this.state.set({
          pending: false,
          message: "尚未查到回答，可重试原请求。",
          unknown: { ...before.unknown, missing: true },
        });
    } catch {
      this.state.set({
        ...before,
        pending: false,
        message: "暂时无法核对回答，连接恢复后可再核对。",
      });
    }
    this.changed();
  }
  async retry() {
    const before = this.state.get();
    if (!before.pending && before.unknown?.missing) await this.attempt(before.unknown.envelope);
  }
  private async attempt(envelope: Envelope) {
    this.state.set({ pending: true, message: "正在提交…", unknown: { envelope, missing: false } });
    try {
      this.settle(await runOp(this.sender, envelope), envelope);
    } catch {
      this.settle({ kind: "unknown", message: "回答结果待核对" }, envelope);
    }
    this.changed();
  }
  private settle(outcome: OpOutcome, envelope: Envelope) {
    this.state.set(
      outcome.kind === "unknown"
        ? {
            pending: false,
            message: "回答结果尚未确认，请核对原请求；不会自动重发。",
            unknown: { envelope, missing: false },
          }
        : {
            pending: false,
            message:
              outcome.kind === "done"
                ? "回答已记录，正在更新安排。"
                : outcome.kind === "stale"
                  ? "这个选择已变化，已刷新最新状态。"
                  : `未提交：${outcome.message}`,
          },
    );
  }
}
