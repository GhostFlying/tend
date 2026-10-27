import { deploymentStorageName } from "../api/deployment.ts";
// The outbox: messages the owner wrote, kept in IndexedDB until tend has
// them, so a closed page or a dropped network loses nothing (v0.1 §14.2).
import type { ConversationAttachment, Envelope } from "../api/generated/api.ts";

/**
 * sending: written down and on its way, or waiting to be sent again.
 * sent: tend answered with a receipt.
 * unknown: no answer after several tries; tend may or may not have it.
 * failed: tend refused it.
 */
export type SendState = "sending" | "sent" | "unknown" | "failed" | "cancelled";

export interface OutgoingAttachment {
  requestId: string;
  name: string;
  blob: Blob;
  /** Saved before starting transport, so reload first reconciles this ID. */
  attempted?: boolean;
  receipt?: ConversationAttachment;
}

export interface OutgoingMessage {
  submitAttempted?: boolean;
  references?: ConversationAttachment[];
  uploads?: OutgoingAttachment[];
  attachmentsFrozen?: boolean;
  /** The envelope's request ID; every resend uses it again (D11). */
  requestId: string;
  /**
   * Where the message stands among this browser's messages, increasing in
   * the order they were written. The send order follows it, not the clock,
   * which can repeat or go back.
   */
  order: number;
  /** Exactly what is sent, every time, so tend can deduplicate it. */
  envelope: Envelope;
  body: string;
  createdAt: number;
  updatedAt: number;
  state: SendState;
  /** From the receipt. */
  messageId?: string;
  seq?: number;
  /** Why the last try did not succeed. */
  error?: string;
}

export interface Outbox {
  all(): Promise<OutgoingMessage[]>;
  cancelled(id: string): Promise<boolean>;
  edit(
    id: string,
    change: (current: OutgoingMessage) => OutgoingMessage | null,
  ): Promise<OutgoingMessage | undefined>;
  put(message: OutgoingMessage): Promise<void>;
  delete(requestId: string): Promise<void>;
}

const storeName = "outbox";

function done<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error("IndexedDB request failed"));
  });
}

function committed(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = tx.onerror = () => reject(tx.error ?? new Error("IndexedDB transaction failed"));
  });
}

/** Opens the outbox in the browser's IndexedDB, or in the given factory. */
export async function openOutbox(
  factory: IDBFactory = indexedDB,
  name = deploymentStorageName("tend"),
): Promise<Outbox> {
  const open = factory.open(name, 1);
  open.onupgradeneeded = () => {
    open.result.createObjectStore(storeName, { keyPath: "requestId" });
  };
  const db = await done(open);
  // Another tab upgrading the database closes this connection.
  db.onversionchange = () => db.close();

  const write = async (fn: (store: IDBObjectStore) => void): Promise<void> => {
    // strict: the browser flushes to disk before reporting completion.
    const tx = db.transaction(storeName, "readwrite", { durability: "strict" });
    const result = committed(tx);
    fn(tx.objectStore(storeName));
    await result;
  };

  return {
    async edit(id, change) {
      const tx = db.transaction(storeName, "readwrite", { durability: "strict" });
      const completion = committed(tx);
      const store = tx.objectStore(storeName);
      const current = await done(store.get(id) as IDBRequest<OutgoingMessage | undefined>);
      const next = current ? change(current) : undefined;
      if (next) store.put(next);
      else if (current) store.delete(id);
      await completion;
      return next ?? undefined;
    },
    async cancelled(id) {
      const value = await done(
        db.transaction(storeName).objectStore(storeName).get(id) as IDBRequest<
          OutgoingMessage | undefined
        >,
      );
      return value?.state === "cancelled";
    },
    async all() {
      const tx = db.transaction(storeName, "readonly");
      return (
        await done(tx.objectStore(storeName).getAll() as IDBRequest<OutgoingMessage[]>)
      ).filter((m) => m.state !== "cancelled");
    },
    put: (message) => write((store) => store.put(message)),
    delete: (requestId) => write((store) => store.delete(requestId)),
  };
}
