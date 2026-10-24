// The send queue (v0.1 §14.2, D70). A message is written to the outbox
// with a new request ID before anything is sent; sending, resending after a
// reload or reconnect, and the owner's manual resend all reuse that ID, so
// tend records the message once however many times it arrives (D11).
//
// Messages go out one at a time, oldest first, so tend receives them in
// the order they were written. A try without an answer is retried a few
// times; after that the message is unknown and waits for the owner, or for
// a reconnect to look its request up (invariant 4: reconcile, do not guess).
// Until then the messages written after it wait too, so their order holds.
import { ApiError, NetworkError } from "../api/client.ts";
import type {
  APIResponse,
  DecisionChoice,
  Envelope,
  OpStatus,
  RequestView,
  SubmitMessagePayload,
  ConversationImageInput,
  ConversationAttachment,
} from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";
import { Store, type Readable } from "../state/store.ts";
import type { Outbox, OutgoingMessage, SendState } from "./outbox.ts";

/** The part of the API client the queue uses. */
export interface Sender {
  submit(envelope: Envelope): Promise<APIResponse>;
  upload?(requestId: string, blob: Blob, name: string, signal: AbortSignal): Promise<APIResponse>;
  lookup(requestId: string): Promise<RequestView | null>;
}

export interface QueueOptions {
  outbox: Outbox;
  sender: Sender;
  now?: () => number;
  newId?: (now: number) => string;
  sleep?: (ms: number) => Promise<void>;
  /** Whether the device has a network at all; default navigator.onLine. */
  online?: () => boolean;
  /** Pauses between tries; one more try than pauses. */
  backoffMs?: readonly number[];
  /** How long a sent message stays in the outbox. Default one day. */
  keepSentMs?: number;
}

export interface Draft {
  body: string;
  images?: ConversationImageInput[];
  files?: File[];
  replyTo?: string;
  lastSeenSeq?: number;
  /** The option of an open decision the message chooses (D71). */
  decision?: DecisionChoice;
}

/** The decision option a message in the outbox chooses, if any. */
export function choiceOf(m: OutgoingMessage): DecisionChoice | undefined {
  return (m.envelope.payload as SubmitMessagePayload | undefined)?.decision;
}

/** How one try ended. */
type Outcome =
  | { kind: "settled"; state: SendState; error?: string; messageId?: string; seq?: number }
  /** No answer, or an answer that says nothing about the message. */
  | { kind: "retry"; error: string }
  /** The device may not send at all: no network, or not an owner. */
  | { kind: "stop" };

const defaultBackoff = [1_000, 3_000, 8_000];

/** The list of a browser without an outbox. */
export const noOutbox: Readable<readonly OutgoingMessage[]> = new Store([]);

export class SendQueue {
  private readonly outbox: Outbox;
  private readonly sender: Sender;
  private readonly now: () => number;
  private readonly newId: (now: number) => string;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly online: () => boolean;
  private readonly backoffMs: readonly number[];
  private readonly keepSentMs: number;
  private readonly messages = new Store<readonly OutgoingMessage[]>([]);
  private draining: Promise<void> | null = null;
  private kicked = false;
  private readonly discarded = new Set<string>();
  private transfer: { requestId: string; controller: AbortController } | null = null;
  /** The order the next message gets: one past the highest stored. */
  private nextOrder = 1;

  private constructor(options: QueueOptions) {
    this.outbox = options.outbox;
    this.sender = options.sender;
    this.now = options.now ?? Date.now;
    this.newId = options.newId ?? ((now) => newRequestId(now));
    this.sleep = options.sleep ?? ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
    this.online = options.online ?? (() => navigator.onLine);
    this.backoffMs = options.backoffMs ?? defaultBackoff;
    this.keepSentMs = options.keepSentMs ?? 24 * 60 * 60 * 1000;
  }

  /** Loads the outbox and drops messages sent long ago. Call resume() to send. */
  static async open(options: QueueOptions): Promise<SendQueue> {
    const q = new SendQueue(options);
    const cutoff = q.now() - q.keepSentMs;
    const kept: OutgoingMessage[] = [];
    for (const m of await q.outbox.all()) {
      q.nextOrder = Math.max(q.nextOrder, m.order + 1);
      if (m.state === "sent" && m.updatedAt < cutoff) {
        await q.outbox.delete(m.requestId);
      } else {
        kept.push(m);
      }
    }
    q.messages.set(sorted(kept));
    return q;
  }

  /** The outbox, oldest first. */
  get list(): Readable<readonly OutgoingMessage[]> {
    return this.messages;
  }

  /**
   * Writes a message down and starts sending it. Resolves once it is in the
   * outbox, before tend answers.
   */
  async enqueue(draft: Draft): Promise<OutgoingMessage> {
    // Taken before any await, so concurrent calls keep their call order.
    const order = this.nextOrder++;
    const now = this.now();
    const requestId = this.newId(now);
    const payload: SubmitMessagePayload = { body: draft.body };
    if (draft.images?.length) {
      payload.images = draft.images.map((image) => ({ ...image }));
    }
    if (draft.replyTo) {
      payload.reply_to = draft.replyTo;
    }
    if (draft.lastSeenSeq) {
      payload.last_seen_seq = draft.lastSeenSeq;
    }
    if (draft.decision) {
      payload.decision = draft.decision;
    }
    const message: OutgoingMessage = {
      requestId,
      order,
      envelope: {
        request_id: requestId,
        operation: "conversation.submit_message",
        target: { type: "conversation" },
        payload,
      },
      body: draft.body,
      createdAt: now,
      updatedAt: now,
      state: "sending",
      ...(draft.files?.length
        ? {
            uploads: draft.files.map((file) => ({
              requestId: this.newId(now),
              name: file.name,
              blob: file,
            })),
          }
        : {}),
    };
    await this.save(message);
    void this.kick();
    return message;
  }

  /**
   * Sends what is pending and reconciles what is unknown: after a reload, a
   * reconnect or the network coming back.
   */
  async resume(): Promise<void> {
    for (const m of this.messages.get()) {
      if (m.state === "unknown") {
        await this.reconcile(m.requestId);
      }
    }
    await this.kick();
  }

  /**
   * Looks an unknown message up in tend: a recorded request settles it; a
   * request tend never recorded is sent again with the same ID.
   */
  async reconcile(requestId: string): Promise<void> {
    const m = this.find(requestId);
    if (!m || m.state !== "unknown") {
      return;
    }
    if (m.uploads?.length && !m.attachmentsFrozen) {
      await this.update(requestId, { state: "sending" });
      await this.kick();
      return;
    }
    let view: RequestView | null;
    try {
      view = await this.sender.lookup(requestId);
    } catch {
      // Still no answer; it stays unknown.
      return;
    }
    if (view === null) {
      await this.update(requestId, { state: "sending" });
      void this.kick();
      return;
    }
    const outcome = settle(view.status, view.result, view.error?.message);
    if (outcome.kind === "settled") {
      await this.update(requestId, outcome);
      // Settled, it no longer holds back the messages after it.
      void this.kick();
    }
  }

  /**
   * The owner's resend of an unknown message: the same envelope and request
   * ID, sent again. A refused message would only be refused again.
   */
  async resend(requestId: string): Promise<void> {
    const m = this.find(requestId);
    if (!m || m.state !== "unknown") {
      return;
    }
    if (m.uploads?.length && !m.attachmentsFrozen) {
      await this.reconcile(requestId);
      return;
    }
    await this.update(requestId, { state: "sending" });
    await this.kick();
  }

  /** Resolves when the queue has stopped sending for now. */
  idle(): Promise<void> {
    return this.draining ?? Promise.resolve();
  }

  /**
   * Drops a message from the outbox. Dropping an unknown message lets the
   * messages after it go.
   */
  async discard(requestId: string): Promise<void> {
    this.discarded.add(requestId);
    // Remove the in-memory intent before aborting: the transfer may settle
    // while the IndexedDB deletion is still committing.
    this.messages.set(this.messages.get().filter((m) => m.requestId !== requestId));
    if (this.transfer?.requestId === requestId) this.transfer.controller.abort();
    await this.outbox.delete(requestId);
    void this.kick();
  }

  /** Starts sending if it is not already; resolves when it stops. */
  private kick(): Promise<void> {
    if (this.draining) {
      this.kicked = true;
      return this.draining;
    }
    this.draining = (async () => {
      try {
        do {
          this.kicked = false;
          if (!(await this.drain())) {
            return;
          }
        } while (this.kicked);
      } finally {
        this.draining = null;
      }
    })();
    return this.draining;
  }

  /**
   * Sends pending messages in order; false if it had to stop. An unknown
   * message holds back the ones after it: sending them first and the
   * unknown one later, if tend never got it, would reverse their order.
   */
  private async drain(): Promise<boolean> {
    for (;;) {
      const next = this.messages.get().find((m) => m.state === "sending" || m.state === "unknown");
      if (!next) {
        return true;
      }
      if (next.state === "unknown" || !(await this.deliver(next))) {
        return false;
      }
    }
  }

  /** Sends one message until tend answers or the tries run out. */
  private async deliver(m: OutgoingMessage): Promise<boolean> {
    if (m.uploads?.length && !m.attachmentsFrozen) {
      if (!(await this.prepareAttachments(m.requestId))) return false;
      const current = this.find(m.requestId);
      if (!current || current.state !== "sending") return true;
      if (!current.attachmentsFrozen) return false;
      m = current;
    }
    for (let attempt = 0; ; attempt++) {
      const outcome = await this.attempt(m.envelope);
      if (outcome.kind === "stop") {
        return false;
      }
      if (outcome.kind === "settled") {
        await this.update(m.requestId, outcome);
        return true;
      }
      if (!this.online()) {
        // Without a network a try says nothing; wait for it to come back.
        return false;
      }
      const pause = this.backoffMs[attempt];
      if (pause === undefined) {
        await this.update(m.requestId, { state: "unknown", error: outcome.error });
        return true;
      }
      await this.sleep(pause);
      if (this.find(m.requestId)?.state !== "sending") {
        // Discarded meanwhile.
        return true;
      }
    }
  }

  /** Upload receipts and the final reference list are committed before message delivery. */
  private async prepareAttachments(requestId: string): Promise<boolean> {
    for (let index = 0; ; index++) {
      let m = this.find(requestId);
      if (!m) return true;
      const upload = m.uploads?.[index];
      if (!upload) {
        await this.save({
          ...m,
          attachmentsFrozen: true,
          envelope: {
            ...m.envelope,
            payload: {
              ...(m.envelope.payload as SubmitMessagePayload),
              attachments: m.uploads!.map((entry) => entry.receipt!.file_id),
            },
          },
        });
        return true;
      }
      if (upload.receipt) continue;
      for (let attempt = 0; ; attempt++) {
        if (!this.online()) return false;
        const controller = new AbortController();
        this.transfer = { requestId, controller };
        try {
          let operation: APIResponse["operation"];
          if (upload.attempted) {
            const existing = await this.sender.lookup(upload.requestId);
            if (existing) operation = existing;
          }
          if (!operation) {
            upload.attempted = true;
            m = this.find(requestId);
            if (!m) return true;
            await this.save({ ...m, uploads: [...m.uploads!] });
            if (!this.sender.upload) throw new Error("附件上传不可用");
            operation = (
              await this.sender.upload(
                upload.requestId,
                upload.blob,
                upload.name,
                controller.signal,
              )
            ).operation;
          }
          if (operation?.status === "failed") {
            await this.update(requestId, {
              state: "failed",
              error: operation.error?.message ?? "附件上传失败",
            });
            return true;
          }
          if (operation?.status === "unknown") {
            await this.update(requestId, { state: "unknown", error: "附件上传结果待核对" });
            return true;
          }
          const receipt = attachmentReceipt(operation?.result);
          if (!receipt) throw new NetworkError("附件上传回执不完整");
          m = this.find(requestId);
          if (!m) return true;
          const uploads = m.uploads!.map((entry, n) =>
            n === index ? { ...entry, receipt } : entry,
          );
          await this.save({ ...m, uploads, updatedAt: this.now() });
          break;
        } catch (error) {
          if (controller.signal.aborted || !this.find(requestId)) return true;
          if (error instanceof ApiError && error.status === 403) return false;
          if (error instanceof ApiError && error.status < 500 && error.status !== 401) {
            await this.update(requestId, { state: "failed", error: error.message });
            return true;
          }
          if (!this.online()) return false;
          const pause = this.backoffMs[attempt];
          if (pause === undefined) {
            await this.update(requestId, {
              state: "unknown",
              error: "附件上传未收到回执，等待核对",
            });
            return true;
          }
          await this.sleep(pause);
          if (!this.find(requestId)) return true;
        } finally {
          if (this.transfer?.controller === controller) this.transfer = null;
        }
      }
    }
  }

  private async attempt(envelope: Envelope): Promise<Outcome> {
    let res: APIResponse;
    try {
      res = await this.sender.submit(envelope);
    } catch (err) {
      if (err instanceof NetworkError) {
        return { kind: "retry", error: "tend 没有回应" };
      }
      if (err instanceof ApiError) {
        if (err.status === 403) {
          return { kind: "stop" };
        }
        if (err.status >= 500 || err.status === 401) {
          return { kind: "retry", error: err.message };
        }
        // invalid_request, conflict_payload and the like: tend refused it.
        return { kind: "settled", state: "failed", error: err.message };
      }
      throw err;
    }
    const op = res.operation;
    if (!op) {
      return { kind: "retry", error: "tend 的回答里没有结果" };
    }
    return settle(op.status, op.result, op.error?.message);
  }

  private find(requestId: string): OutgoingMessage | undefined {
    if (this.discarded.has(requestId)) return undefined;
    return this.messages.get().find((m) => m.requestId === requestId);
  }

  private async update(
    requestId: string,
    change: Partial<Pick<OutgoingMessage, "state" | "error" | "messageId" | "seq">>,
  ): Promise<void> {
    const m = this.find(requestId);
    if (!m) {
      return;
    }
    const next: OutgoingMessage = { ...m, updatedAt: this.now() };
    next.state = change.state ?? m.state;
    if (change.error === undefined) {
      delete next.error;
    } else {
      next.error = change.error;
    }
    if (change.messageId !== undefined) {
      next.messageId = change.messageId;
    }
    if (change.seq !== undefined) {
      next.seq = change.seq;
    }
    await this.save(next);
  }

  private async save(m: OutgoingMessage): Promise<void> {
    if (this.discarded.has(m.requestId)) return;
    await this.outbox.put(m);
    if (this.discarded.has(m.requestId)) return;
    const others = this.messages.get().filter((o) => o.requestId !== m.requestId);
    this.messages.set(sorted([...others, m]));
  }
}

/** Maps an operation's status to the message's state. */
function settle(status: OpStatus, result: unknown, error: string | undefined): Outcome {
  switch (status) {
    case "accepted":
    case "succeeded": {
      // conversation.submit_message answers with the message's ID and place.
      const r = (result ?? {}) as { message_id?: unknown; seq?: unknown };
      return {
        kind: "settled",
        state: "sent",
        ...(typeof r.message_id === "string" ? { messageId: r.message_id } : {}),
        ...(typeof r.seq === "number" ? { seq: r.seq } : {}),
      };
    }
    case "failed":
      return { kind: "settled", state: "failed", error: error ?? "tend 拒绝了这条消息" };
    case "unknown":
      // tend itself cannot tell; only the owner decides to send it again.
      return { kind: "settled", state: "unknown", error: error ?? "tend 无法确认结果" };
  }
}

function sorted(messages: OutgoingMessage[]): OutgoingMessage[] {
  // Two tabs may hand out the same order; the request ID breaks the tie.
  return messages.sort((a, b) => a.order - b.order || cmp(a.requestId, b.requestId));
}

function cmp(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function attachmentReceipt(value: unknown): ConversationAttachment | undefined {
  if (!value || typeof value !== "object") return;
  const r = value as ConversationAttachment;
  if (
    typeof r.file_id === "string" &&
    typeof r.name === "string" &&
    typeof r.media_type === "string" &&
    typeof r.size === "number" &&
    typeof r.sha256 === "string"
  )
    return r;
}
