// Independent shared handoff settings (D81). Reads retry with capped backoff;
// uncertain writes retain their request identity and are only looked up.
import type {
  ConversationHandoffView,
  SetConversationHandoffSettingsPayload,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { runOp, settleOp, type OpSender, type OpOutcome } from "../app/ops.ts";
import { Store } from "../state/store.ts";

export interface HandoffState {
  view: ConversationHandoffView | null;
  reading: boolean;
  saving: boolean;
  error?: string | undefined;
  readError?: string | undefined;
  pendingId?: string | undefined;
  preparationRequestId?: string | undefined;
}

export class ConversationHandoff {
  readonly state = new Store<HandoffState>({ view: null, reading: false, saving: false });
  private controller: AbortController | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay = 1_000;
  private contentVersion = 0;
  private lastContentEvent = 0;
  private prepareVersion = 0;
  private contentTimer: ReturnType<typeof setTimeout> | undefined;
  private active = false;
  private running: Promise<void> | null = null;
  private again = false;

  constructor(private readonly api: Reader) {}

  start(): void {
    if (this.active) return;
    this.active = true;
    void this.refresh();
  }

  /** Conversation content changed, including an update of an existing item. */
  invalidate(eventSeq: number): void {
    if (eventSeq <= this.lastContentEvent) return;
    this.lastContentEvent = eventSeq;
    this.contentVersion++;
    const state = this.state.get();
    const view = state.view;
    if (view?.summary?.current) {
      this.state.set({ ...state, view: { ...view, summary: { ...view.summary, current: false } } });
    }
    if (!this.active || this.contentTimer !== undefined) return;
    // A stream can emit many ledger items per second. Mark stale immediately,
    // but coalesce its status reads without aborting a read already in flight.
    this.contentTimer = setTimeout(() => {
      this.contentTimer = undefined;
      void this.refresh();
    }, 1_000);
  }

  refresh(): Promise<void> {
    if (!this.active) return Promise.resolve();
    clearTimeout(this.contentTimer);
    this.contentTimer = undefined;
    if (this.running) {
      this.again = true;
      return this.running;
    }
    clearTimeout(this.timer);
    this.running = (async () => {
      do {
        this.again = false;
        await this.read();
      } while (this.again && this.active);
    })().finally(() => {
      this.running = null;
    });
    return this.running;
  }

  private async read(): Promise<void> {
    clearTimeout(this.timer);
    this.timer = undefined;
    const version = this.contentVersion;
    const preparingVersion = this.prepareVersion;
    const controller = new AbortController();
    this.controller = controller;
    this.state.set({ ...this.state.get(), reading: true });
    try {
      const received = await this.api.get<ConversationHandoffView>(
        "/api/v1/conversation/handoff",
        controller.signal,
      );
      if (controller.signal.aborted) return;
      const old = this.state.get();
      const view =
        version !== this.contentVersion && received.summary?.current
          ? { ...received, summary: { ...received.summary, current: false } }
          : received;
      const observedPreparation =
        preparingVersion === this.prepareVersion &&
        (received.maintenance !== undefined || received.pending || received.preparing);
      this.state.set({
        ...old,
        view,
        reading: false,
        readError: undefined,
        preparationRequestId: observedPreparation ? undefined : old.preparationRequestId,
      });
      this.delay = 1_000;
      if (
        received.enabled &&
        (received.pending ||
          received.preparing ||
          ["pending", "sending"].includes(received.maintenance?.status ?? "") ||
          this.state.get().preparationRequestId)
      )
        this.retry();
    } catch (err) {
      if (controller.signal.aborted) return;
      this.state.set({ ...this.state.get(), reading: false, readError: String(err) });
      this.retry();
    }
  }

  private retry(): void {
    if (!this.active) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      void this.refresh();
    }, this.delay);
    this.delay = Math.min(this.delay * 2, 30_000);
  }

  save(sender: OpSender, payload: SetConversationHandoffSettingsPayload): Promise<void> {
    return this.write(sender, payload.idle_minutes);
  }

  prepare(sender: OpSender): Promise<void> {
    return this.write(sender);
  }

  private async write(sender: OpSender, idleMinutes?: number): Promise<void> {
    const before = this.state.get();
    if (!before.view?.enabled || before.saving || before.pendingId) return;
    if (
      idleMinutes === undefined &&
      (before.preparationRequestId ||
        before.view.pending ||
        before.view.preparing ||
        ["pending", "sending"].includes(before.view.maintenance?.status ?? ""))
    )
      return;
    this.state.set({ ...before, saving: true, error: undefined });
    let requestId: string | undefined;
    const tracked: OpSender = {
      submit: (envelope) => {
        requestId = envelope.request_id;
        return sender.submit(envelope);
      },
      lookup: (id) => sender.lookup(id),
    };
    try {
      const outcome = await runOp(tracked, {
        operation:
          idleMinutes === undefined ? "conversation.handoff" : "conversation.set_handoff_settings",
        target: { type: "conversation" },
        ...(idleMinutes === undefined ? {} : { expected_control_revision: before.view.revision }),
        payload: idleMinutes === undefined ? { prepare_only: true } : { idle_minutes: idleMinutes },
      });
      if (idleMinutes === undefined && outcome.kind === "done" && requestId) {
        this.prepareVersion++;
        this.state.set({ ...this.state.get(), preparationRequestId: requestId });
      }
      this.settle(outcome, requestId);
      await this.refresh();
    } catch (err) {
      this.state.set({
        ...this.state.get(),
        saving: false,
        pendingId: requestId,
        error: String(err),
      });
    }
  }

  async reconcile(sender: OpSender): Promise<void> {
    const before = this.state.get();
    if (!before.pendingId || before.saving) return;
    this.state.set({ ...before, saving: true });
    try {
      const record = await sender.lookup(before.pendingId);
      if (!record) {
        this.state.set({
          ...this.state.get(),
          saving: false,
          pendingId: undefined,
          error: "尚未查到这次设置操作，已重新读取当前设置",
        });
      } else {
        const outcome = settleOp(record.status, record.result, record.error);
        if (record.operation === "conversation.handoff" && outcome.kind === "done") {
          this.prepareVersion++;
          this.state.set({ ...this.state.get(), preparationRequestId: before.pendingId });
        }
        this.settle(outcome, before.pendingId);
      }
      await this.refresh();
    } catch {
      this.state.set({
        ...this.state.get(),
        saving: false,
        error: "仍无法核对设置，连接恢复后可再核对",
      });
    }
  }

  private settle(outcome: OpOutcome, id: string | undefined): void {
    this.state.set({
      ...this.state.get(),
      saving: false,
      pendingId: outcome.kind === "unknown" ? id : undefined,
      error: outcome.kind === "done" ? undefined : outcome.message,
    });
  }

  async stop(): Promise<void> {
    this.active = false;
    this.again = false;
    clearTimeout(this.timer);
    clearTimeout(this.contentTimer);
    this.contentTimer = undefined;
    this.controller?.abort();
    await this.running;
  }
}
