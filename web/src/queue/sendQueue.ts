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
  InjectSourcePayload,
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
  conversationID?: string;
  /** Source workflows reuse durable transfers without becoming owner instructions. */
  operation?: "conversation.submit_message" | "conversation.inject_source";
  scope?: string;
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
  attachments?: ConversationAttachment[];
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
  private readonly conversationID: string;
  private readonly sender: Sender;
  private readonly operation: "conversation.submit_message" | "conversation.inject_source";
  private readonly scope: string | undefined;
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
    this.conversationID = options.conversationID ?? "main";
    this.sender = options.sender;
    this.operation = options.operation ?? "conversation.submit_message";
    this.scope = options.scope;
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
      if (!q.belongs(m)) continue;
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
    if (this.operation !== "conversation.submit_message") throw new Error("此入口只保存手动来源");
    return this.enqueueDraft(draft);
  }

  async enqueueSource(
    draft: Draft,
    metadata: InjectSourcePayload["metadata"],
  ): Promise<OutgoingMessage> {
    if (this.operation !== "conversation.inject_source") throw new Error("此入口只发送聊天消息");
    return this.enqueueDraft(draft, metadata);
  }

  private belongs(m: OutgoingMessage): boolean {
    return (
      m.envelope.operation === this.operation &&
      m.queueScope === this.scope &&
      ((m.envelope.payload as SubmitMessagePayload).conversation_id ?? "main") ===
        this.conversationID
    );
  }

  private async enqueueDraft(
    draft: Draft,
    metadata?: InjectSourcePayload["metadata"],
  ): Promise<OutgoingMessage> {
    // Taken before any await, so concurrent calls keep their call order.
    const order = this.nextOrder++;
    const now = this.now();
    const requestId = this.newId(now);
    const payload: SubmitMessagePayload = {
      body: draft.body,
      ...(this.conversationID !== "main" ? { conversation_id: this.conversationID } : {}),
    };
    if (draft.attachments?.length) payload.attachments = draft.attachments.map((a) => a.file_id);
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
      ...(this.scope !== undefined ? { queueScope: this.scope } : {}),
      requestId,
      order,
      submitAttempted: false,
      references: draft.attachments ?? [],
      envelope: {
        request_id: requestId,
        operation: this.operation,
        target: {
          type: this.operation === "conversation.inject_source" ? "message" : "conversation",
        },
        payload: metadata
          ? ({
              text: draft.body,
              metadata: { ...metadata },
              ...(payload.attachments ? { attachments: payload.attachments } : {}),
              ...(payload.conversation_id ? { conversation_id: payload.conversation_id } : {}),
            } satisfies InjectSourcePayload)
          : payload,
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
    await this.outbox.put(message);
    await this.save(message);
    void this.kick();
    return message;
  }

  /**
   * Sends what is pending and reconciles what is unknown: after a reload, a
   * reconnect or the network coming back.
   */
  async resume(): Promise<void> {
    this.messages.set(sorted((await this.outbox.all()).filter((m) => this.belongs(m))));
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
   * Cancels only before message transport, retaining a durable tombstone
   * for edit recovery and fencing stale senders in other tabs.
   */
  async discard(requestId: string): Promise<void> {
    const current = await this.outbox.edit(requestId, (m) => {
      if (m.submitAttempted !== false && m.state !== "failed") return m;
      const tombstone: OutgoingMessage = {
        ...m,
        state: "cancelled",
        body: "",
        envelope: { ...m.envelope, payload: {} },
      };
      delete tombstone.uploads;
      delete tombstone.references;
      return tombstone;
    });
    if (current && current.state !== "cancelled")
      throw new Error("消息可能已提交，请先核对服务端状态后撤回");
    this.discarded.add(requestId);
    this.messages.set(this.messages.get().filter((m) => m.requestId !== requestId));
    if (this.transfer?.requestId === requestId) this.transfer.controller.abort();
    void this.kick();
  }

  cancelled(id: string): Promise<boolean> {
    return this.outbox.cancelled(id);
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
    if (!this.online()) return false;
    // The same IndexedDB transaction arbitrates cancellation across tabs.
    const claimed = await this.outbox.edit(m.requestId, (current) =>
      current.state === "cancelled"
        ? current
        : {
            ...current,
            submitAttempted: true,
          },
    );
    if (!claimed) {
      this.messages.set(this.messages.get().filter((it) => it.requestId !== m.requestId));
      return true;
    }
    if (claimed.state === "cancelled") {
      this.messages.set(this.messages.get().filter((it) => it.requestId !== m.requestId));
      return true;
    }
    m = claimed;
    if (m.state === "sent" || m.state === "failed") {
      await this.save(m);
      return true;
    }
    await this.save(m);
    for (let attempt = 0; ; attempt++) {
      if (!this.online()) return false;
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
              attachments: [
                ...((m.envelope.payload as SubmitMessagePayload).attachments ?? []),
                ...m.uploads!.map((entry) => entry.receipt!.file_id),
              ],
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
    const saved = await this.outbox.edit(m.requestId, (current) =>
      current.state === "cancelled"
        ? current
        : {
            ...m,
            ...(current.submitAttempted === true ||
            current.attachmentsFrozen === true ||
            (current.submitAttempted === undefined && !current.uploads?.length)
              ? {
                  envelope: current.envelope,
                  uploads: current.uploads ?? [],
                  attachmentsFrozen: current.attachmentsFrozen ?? true,
                }
              : {}),
            ...(current.state === "sent"
              ? { state: current.state, messageId: current.messageId ?? "", seq: current.seq ?? 0 }
              : {}),
            ...(current.submitAttempted === undefined && m.submitAttempted === undefined
              ? {}
              : {
                  submitAttempted: current.submitAttempted === true || m.submitAttempted === true,
                }),
          },
    );
    if (!saved || saved.state === "cancelled") {
      this.discarded.add(m.requestId);
      this.messages.set(this.messages.get().filter((it) => it.requestId !== m.requestId));
      return;
    }
    if (this.discarded.has(m.requestId)) return;
    m = saved;
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
