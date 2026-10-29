import type { MessageContent as ConversationItem } from "./eventContent.ts";
import { deploymentStorageName } from "../api/deployment.ts";
// Drafts and withdrawal intentions share one durable transaction boundary.
import type { ConversationAttachment, Envelope } from "../api/generated/api.ts";
export interface SavedDraft {
  id: string;
  text: string;
  files: File[];
  attachments: ConversationAttachment[];
  replyTo?: string | undefined;
  source?: ConversationItem | undefined;
  updated: number;
}
export interface Withdrawal {
  localRequestId?: string;
  localDraft?: SavedDraft;
  id: string;
  draftId: string;
  envelope: Envelope;
  item: ConversationItem;
  edit: boolean;
  state: "pending" | "done" | "failed";
  error?: string | undefined;
}
export function request<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}
function committed(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = tx.onerror = () => reject(tx.error);
  });
}
export class DraftStorage {
  private readonly instance = crypto.randomUUID();
  private lease: { id: string; owner: string } | undefined;
  constructor(private db: IDBDatabase) {}
  static async open(
    factory: IDBFactory = indexedDB,
    name = deploymentStorageName("tend-drafts"),
  ): Promise<DraftStorage> {
    const open = factory.open(name, 2);
    open.onupgradeneeded = () => {
      if (!open.result.objectStoreNames.contains("drafts"))
        open.result.createObjectStore("drafts", { keyPath: "id" });
      if (!open.result.objectStoreNames.contains("withdrawals"))
        open.result.createObjectStore("withdrawals", { keyPath: "id" });
      open.result.createObjectStore("leases", { keyPath: "id" });
    };
    const db = await request(open);
    db.onversionchange = () => db.close();
    return new DraftStorage(db);
  }
  close(): void {
    this.db.close();
  }
  async claim(id: string, owner: string, now = Date.now()): Promise<boolean> {
    const tx = this.db.transaction("leases", "readwrite");
    const done = committed(tx),
      store = tx.objectStore("leases");
    const old = (await request(store.get(id))) as
      { owner: string; expires: number; instance: string } | undefined;
    if (old && old.owner !== owner && old.expires > now) {
      await done;
      return false;
    }
    store.put({ id, owner, instance: this.instance, expires: now + 60000 });
    await done;
    this.lease = { id, owner };
    return true;
  }
  async release(): Promise<void> {
    if (!this.lease) return;
    const tx = this.db.transaction("leases", "readwrite"),
      done = committed(tx),
      store = tx.objectStore("leases");
    const old = (await request(store.get(this.lease.id))) as
      { owner: string; instance: string } | undefined;
    if (old?.owner === this.lease.owner && old.instance === this.instance)
      store.delete(this.lease.id);
    await done;
  }
  private async checkLease(tx: IDBTransaction): Promise<void> {
    if (!this.lease) return;
    const old = (await request(tx.objectStore("leases").get(this.lease.id))) as
      { owner: string; expires: number; instance: string } | undefined;
    if (
      old?.owner !== this.lease.owner ||
      old.expires <= Date.now() ||
      old.instance !== this.instance
    )
      throw new Error("草稿编辑权已转移到另一标签页；内容仍在本页，请刷新后恢复");
  }
  async drafts(): Promise<SavedDraft[]> {
    return request(this.db.transaction("drafts").objectStore("drafts").getAll());
  }
  async withdrawals(): Promise<Withdrawal[]> {
    return request(this.db.transaction("withdrawals").objectStore("withdrawals").getAll());
  }
  async save(store: "drafts" | "withdrawals", value: SavedDraft | Withdrawal): Promise<void> {
    const tx = this.db.transaction([store, "leases"], "readwrite", { durability: "strict" });
    const done = committed(tx);
    try {
      await this.checkLease(tx);
    } catch (error) {
      await done;
      throw error;
    }
    if (store === "withdrawals") {
      const old = (await request(tx.objectStore(store).get(value.id))) as Withdrawal | undefined;
      const next = value as Withdrawal;
      if (old && (old.state !== "pending" || old.draftId !== next.draftId)) {
        await done;
        return;
      }
    }
    tx.objectStore(store).put(value);
    await done;
  }
  async adoptWithdrawal(id: string, draftId: string): Promise<void> {
    const tx = this.db.transaction(["withdrawals", "leases"], "readwrite", {
      durability: "strict",
    });
    const done = committed(tx);
    try {
      await this.checkLease(tx);
    } catch (error) {
      await done;
      throw error;
    }
    const store = tx.objectStore("withdrawals");
    const w = (await request(store.get(id))) as Withdrawal | undefined;
    if (!w || w.state !== "pending") {
      await done;
      return;
    }
    const old = (await request(tx.objectStore("leases").get(w.draftId))) as
      { expires: number } | undefined;
    if (w.draftId !== draftId && old && old.expires > Date.now()) {
      await done;
      throw new Error("原标签页仍在处理这次取回；关闭后再恢复，或等待编辑权到期");
    }
    store.put({
      ...w,
      draftId,
      ...(w.localDraft ? { localDraft: { ...w.localDraft, id: draftId } } : {}),
    });
    await done;
  }
  async restore(w: Withdrawal, recovered: SavedDraft): Promise<boolean> {
    const tx = this.db.transaction(["drafts", "withdrawals", "leases"], "readwrite", {
      durability: "strict",
    });
    const done = committed(tx);
    try {
      await this.checkLease(tx);
    } catch (error) {
      await done;
      throw error;
    }
    const withdrawals = tx.objectStore("withdrawals"),
      drafts = tx.objectStore("drafts");
    const current = (await request(withdrawals.get(w.id))) as Withdrawal | undefined;
    if (current?.state !== "pending" || current.draftId !== w.draftId) {
      await done;
      return false;
    }
    if (w.edit) {
      const previous = (await request(drafts.get(w.draftId))) as SavedDraft | undefined;
      if (previous && (previous.text || previous.files.length || previous.attachments.length))
        drafts.put({ ...previous, id: `stash:${w.id}` });
      drafts.put({ ...recovered, id: `recovered:${w.id}` });
      drafts.put(recovered);
    }
    withdrawals.put({ ...w, state: "done" });
    await done;
    return true;
  }
}
