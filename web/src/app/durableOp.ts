// Owner control requests persist before transport. Recovery retains the original
// target, revision and payload; a different tab cannot replace a pending request.
import type { Envelope } from "../api/generated/api.ts";
import { runOp, settleOp, type OpOutcome, type OpSender } from "./ops.ts";

export class DurableOp {
  constructor(
    private readonly scope: string,
    private readonly factory: IDBFactory = indexedDB,
  ) {}

  private async change(
    update: (current: Envelope | undefined) => Envelope | undefined,
  ): Promise<Envelope | undefined> {
    const open = this.factory.open("tend-control-requests", 1);
    open.onupgradeneeded = () => open.result.createObjectStore("requests");
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error);
    });
    try {
      return await new Promise<Envelope | undefined>((resolve, reject) => {
        const tx = db.transaction("requests", "readwrite", { durability: "strict" });
        const store = tx.objectStore("requests");
        const read = store.get(this.scope) as IDBRequest<Envelope | undefined>;
        let next: Envelope | undefined;
        read.onsuccess = () => {
          next = update(read.result);
          if (next) store.put(next, this.scope);
          else store.delete(this.scope);
        };
        tx.oncomplete = () => resolve(next);
        tx.onerror = tx.onabort = () => reject(tx.error ?? new Error("无法保存操作回执身份"));
      });
    } finally {
      db.close();
    }
  }

  pending(): Promise<Envelope | undefined> {
    return this.change((current) => current);
  }

  private async settle(envelope: Envelope, result: OpOutcome): Promise<OpOutcome> {
    if (result.kind !== "unknown") {
      await this.change((current) =>
        current?.request_id === envelope.request_id ? undefined : current,
      );
    }
    return result;
  }

  async submit(sender: OpSender, envelope: Envelope): Promise<OpOutcome> {
    const reserved = await this.change((current) => current ?? envelope);
    if (reserved?.request_id !== envelope.request_id) {
      return { kind: "unknown", message: "该操作仍有待核对请求，请先核对原请求。" };
    }
    return this.settle(envelope, await runOp(sender, envelope));
  }

  async reconcile(sender: OpSender): Promise<OpOutcome> {
    const original = await this.pending();
    if (!original) return { kind: "done", result: undefined };
    try {
      const receipt = await sender.lookup(original.request_id);
      // A successful not-found lookup permits an explicit retry with exactly
      // the original identity. This never resubmits a new native operation.
      const result = receipt
        ? settleOp(receipt.status, receipt.result, receipt.error)
        : await runOp(sender, original);
      return this.settle(original, result);
    } catch {
      return { kind: "unknown", message: "尚未取得原请求回执，继续保留待核对状态。" };
    }
  }
}
