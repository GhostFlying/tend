import type { Envelope } from "../api/generated/api.ts";
import { deploymentStorageName } from "../api/deployment.ts";
import { newRequestId } from "../api/ids.ts";
import { runOp, settleOp, type OpSender, type OpOutcome } from "../app/ops.ts";
import { Store } from "../state/store.ts";

export class FollowupActions {
  readonly state = new Store<{ busy: boolean; message: string; pending: Envelope[] }>({
    busy: false,
    message: "",
    pending: [],
  });
  private readonly key: string;
  private readonly suffix = deploymentStorageName("");
  private readable = false;
  constructor(
    private sender: OpSender,
    item: string,
    private storage: Storage | undefined,
    private changed: () => void,
  ) {
    this.key = `tend.followup-requests.${item}`;
    this.reload();
  }
  reload() {
    try {
      if (!this.storage) throw new Error("storage unavailable");
      const all: Envelope[] = [];
      for (let i = 0; i < this.storage.length; i++) {
        const key = this.storage.key(i);
        if (
          key?.startsWith(this.key + ".") &&
          (this.suffix ? key.endsWith(this.suffix) : !key.includes(":deployment:"))
        ) {
          const raw = this.storage.getItem(key);
          if (raw) all.push(JSON.parse(raw) as Envelope);
        }
      }
      this.readable = true;
      this.state.set({ ...this.state.get(), pending: all });
    } catch {
      this.readable = false;
      this.state.set({
        ...this.state.get(),
        message: "无法读取待核对操作，请恢复浏览器存储后再操作",
      });
    }
  }
  async execute(op: Omit<Envelope, "request_id">) {
    this.reload();
    if (!this.readable || !this.storage || this.state.get().busy || this.state.get().pending.length)
      return false;
    const envelope = { ...op, request_id: newRequestId(Date.now()) };
    try {
      this.storage.setItem(
        `${this.key}.${envelope.request_id}${this.suffix}`,
        JSON.stringify(envelope),
      );
      this.reload();
    } catch {
      this.state.set({ ...this.state.get(), message: "无法持久保存操作，本次未发送" });
      return;
    }
    return this.attempt(envelope);
  }
  async check(envelope: Envelope) {
    if (this.state.get().busy) return;
    this.state.set({ ...this.state.get(), busy: true });
    try {
      const result = await this.sender.lookup(envelope.request_id);
      if (result) this.settle(settleOp(result.status, result.result, result.error), envelope);
      else
        this.state.set({
          ...this.state.get(),
          message: "尚未查到；仍无法证明未执行，可重试同一请求",
        });
    } catch {
      this.state.set({ ...this.state.get(), message: "暂时无法核对，请恢复连接后再试" });
    } finally {
      this.state.set({ ...this.state.get(), busy: false });
    }
  }
  async retry(envelope: Envelope) {
    if (!this.state.get().busy) await this.attempt(envelope);
  }
  private async attempt(envelope: Envelope) {
    this.state.set({ ...this.state.get(), busy: true });
    try {
      const outcome = await runOp(this.sender, envelope);
      this.settle(outcome, envelope);
      return outcome.kind === "done";
    } catch {
      this.settle({ kind: "unknown", message: "结果待核对" }, envelope);
      return false;
    } finally {
      this.state.set({ ...this.state.get(), busy: false });
      this.changed();
    }
  }
  private settle(result: OpOutcome, envelope: Envelope) {
    if (result.kind !== "unknown") {
      this.reload();
      this.storage?.removeItem(`${this.key}.${envelope.request_id}${this.suffix}`);
      this.reload();
    }
    this.state.set({
      ...this.state.get(),
      message:
        result.kind === "done"
          ? "安排已保存"
          : result.kind === "unknown"
            ? "结果待核对；刷新页面后仍会保留原请求"
            : result.message,
    });
    this.changed();
  }
}

export function followupStorage(): Storage | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}
