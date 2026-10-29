// A manual delivery refresh keeps its identity until receipt or newer facts resolve it.
import type {
  DeliveryObservation,
  Envelope,
  IssueDelivery,
  WorkItemView,
} from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";
import { runOp, settleOp, type OpOutcome, type OpSender } from "../app/ops.ts";
import { Store } from "../state/store.ts";
import { refreshBaseline, type RefreshBaseline } from "./model.ts";

export interface DeliveryRefreshState {
  pending: boolean;
  message: string;
  unknown?: { envelope: Envelope; baseline: RefreshBaseline; missing: boolean };
}
export class DeliveryRefresh {
  readonly state = new Store<DeliveryRefreshState>({ pending: false, message: "" });
  constructor(
    private sender: OpSender,
    private changed: () => void,
  ) {}

  async start(
    item: WorkItemView,
    delivery: IssueDelivery,
    observation?: DeliveryObservation,
  ): Promise<void> {
    if (this.state.get().pending) return;
    const envelope: Envelope = {
      request_id: newRequestId(Date.now()),
      operation: "work_items.refresh_delivery",
      target: { type: "work_item", id: item.work_item_id },
      expected_control_revision: item.revision,
      payload: { delivery_id: delivery.delivery_id },
    };
    await this.attempt(envelope, refreshBaseline(delivery, observation));
  }

  async check(): Promise<void> {
    const before = this.state.get();
    if (before.pending || !before.unknown) return;
    this.state.set({ ...before, pending: true });
    try {
      const receipt = await this.sender.lookup(before.unknown.envelope.request_id);
      if (!receipt) {
        this.state.set({
          pending: false,
          message: "未查到原刷新请求，可显式重试同一请求。",
          unknown: { ...before.unknown, missing: true },
        });
      } else
        this.settle(
          settleOp(receipt.status, receipt.result, receipt.error),
          before.unknown.envelope,
          before.unknown.baseline,
        );
    } catch {
      this.state.set({
        ...before,
        pending: false,
        message: "仍无法核对刷新请求，连接恢复后可再核对。",
      });
    }
    this.changed();
  }

  async retry(): Promise<void> {
    const before = this.state.get();
    if (before.pending || !before.unknown?.missing) return;
    await this.attempt(before.unknown.envelope, before.unknown.baseline);
  }

  private async attempt(envelope: Envelope, baseline: RefreshBaseline): Promise<void> {
    this.state.set({ pending: true, message: "", unknown: { envelope, baseline, missing: false } });
    try {
      this.settle(await runOp(this.sender, envelope), envelope, baseline);
    } catch {
      this.state.set({
        pending: false,
        message: "无法确认刷新请求，等待账本更新或手动核对。",
        unknown: { envelope, baseline, missing: false },
      });
    }
    this.changed();
  }

  private settle(outcome: OpOutcome, envelope: Envelope, baseline: RefreshBaseline) {
    if (outcome.kind === "unknown") {
      this.state.set({
        pending: false,
        message: "刷新请求尚未确认，等待账本事件或连接恢复；不会自动重发。",
        unknown: { envelope, baseline, missing: false },
      });
    } else
      this.state.set({
        pending: false,
        message:
          outcome.kind === "done"
            ? "已请求重新观察，等待执行主机回报。"
            : outcome.kind === "stale"
              ? "计划刚被修改，已刷新。请查看最新事实后再操作。"
              : `刷新未执行：${outcome.message}`,
      });
  }
}
