// A durable interrupt request: restore HTTP delivery under the same request ID,
// and never replay an interrupt tend already recorded (D11, D69).
import { ApiError, FallbackStorage, type KeyValueStorage } from "../api/client.ts";
import type { ConversationItem, Envelope, Operation } from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";
import type { OpSender } from "../app/ops.ts";
import { Store, type Readable } from "../state/store.ts";

export interface InterruptState {
  messageId: string;
  envelope: Envelope;
  phase: "unconfirmed" | "recorded" | "unknown" | "failed";
  error?: string;
}
const key = "tend.interrupt-request.v1";

export class Interrupt {
  private readonly sender: OpSender;
  private readonly storage: KeyValueStorage;
  private readonly newId: () => string;
  private readonly store: Store<InterruptState | null>;
  private inFlight: Promise<void> | null = null;

  constructor(sender: OpSender, options: { storage?: KeyValueStorage; newId?: () => string } = {}) {
    this.sender = sender;
    this.storage = options.storage ?? new FallbackStorage(() => localStorage);
    this.newId = options.newId ?? (() => newRequestId(Date.now()));
    this.store = new Store(this.read());
  }

  get state(): Readable<InterruptState | null> {
    return this.store;
  }

  async start(messageId: string): Promise<void> {
    const previous = this.store.get();
    if (previous?.messageId === messageId && previous.phase !== "failed") return this.resume();
    const request: InterruptState = {
      messageId,
      envelope: {
        request_id: this.newId(),
        operation: "conversation.interrupt_reply",
        target: { type: "message", id: messageId },
        payload: { message_id: messageId },
      },
      phase: "unconfirmed",
    };
    this.write(request);
    // A preceding turn can finish while its HTTP response is still pending.
    if (this.inFlight) await this.inFlight;
    if (!this.current(request)) return;
    return this.run(() => this.transmit(request));
  }

  resume(): Promise<void> {
    const request = this.store.get();
    if (!request || request.phase === "failed") return Promise.resolve();
    return this.run(async () => {
      try {
        const recorded = await this.sender.lookup(request.envelope.request_id);
        if (!this.current(request)) return;
        if (recorded) this.record(request, recorded);
        else if (request.phase === "unconfirmed") await this.transmit(request);
        // A previously recorded request must not become a new external effect,
        // even if a later lookup cannot find it (for example after a restore).
      } catch {
        if (this.current(request) && request.phase === "unconfirmed") {
          this.write({ ...request, error: "停止请求尚未确认，恢复连接后继续查询原请求" });
        }
      }
    });
  }

  observe(items: readonly ConversationItem[]): void {
    const request = this.store.get();
    const message = items.find((item) => item.message_id === request?.messageId);
    if (
      message &&
      (message.state === "replied" || message.state === "interrupted" || message.state === "failed")
    )
      this.write(null);
  }

  private async transmit(request: InterruptState): Promise<void> {
    try {
      const response = await this.sender.submit(request.envelope);
      if (!this.current(request)) return;
      if (response.operation) this.record(request, response.operation);
      else this.write({ ...request, error: "停止请求的回答没有结果，继续查询原请求" });
    } catch (err) {
      if (!this.current(request)) return;
      if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
        this.write({ ...request, phase: "failed", error: err.message });
      } else {
        this.write({ ...request, error: "停止请求尚未确认，恢复连接后继续查询原请求" });
      }
    }
  }

  private record(request: InterruptState, operation: Pick<Operation, "status" | "error">): void {
    if (!this.current(request)) return;
    if (operation.status === "failed")
      this.write({
        ...request,
        phase: "failed",
        error: operation.error?.message ?? "tend 拒绝了停止请求",
      });
    else
      this.write({
        messageId: request.messageId,
        envelope: request.envelope,
        phase: operation.status === "unknown" ? "unknown" : "recorded",
      });
  }

  private current(request: InterruptState): boolean {
    return this.store.get()?.envelope.request_id === request.envelope.request_id;
  }

  private run(action: () => Promise<void>): Promise<void> {
    if (this.inFlight) return this.inFlight;
    const running = Promise.resolve()
      .then(action)
      .finally(() => {
        if (this.inFlight === running) this.inFlight = null;
      });
    this.inFlight = running;
    return running;
  }

  private write(request: InterruptState | null): void {
    if (request && request.phase !== "failed") this.storage.setItem(key, JSON.stringify(request));
    else this.storage.removeItem(key);
    this.store.set(request);
  }

  private read(): InterruptState | null {
    try {
      const value = JSON.parse(this.storage.getItem(key) ?? "null") as InterruptState | null;
      if (
        value &&
        typeof value.messageId === "string" &&
        typeof value.envelope?.request_id === "string" &&
        value.envelope.operation === "conversation.interrupt_reply" &&
        value.envelope.target?.type === "message" &&
        value.envelope.target.id === value.messageId &&
        ["unconfirmed", "recorded", "unknown"].includes(value.phase)
      )
        return value;
    } catch {
      /* Invalid saved data cannot become a control request. */
    }
    return null;
  }
}
