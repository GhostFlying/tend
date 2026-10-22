// Shared hosted conversation settings (D73). Reads retry with capped backoff;
// uncertain writes retain their request identity and are only looked up.
import type {
  ConversationSettingsView,
  SetConversationSettingsPayload,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { runOp, settleOp, type OpSender, type OpOutcome } from "../app/ops.ts";
import { Store } from "../state/store.ts";

export interface SettingsState {
  view: ConversationSettingsView | null;
  reading: boolean;
  saving: boolean;
  error?: string | undefined;
  readError?: string | undefined;
  pendingId?: string | undefined;
}

export class ConversationSettings {
  readonly state = new Store<SettingsState>({ view: null, reading: false, saving: false });
  private controller: AbortController | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay = 1_000;
  private active = false;
  private running: Promise<void> | null = null;
  private again = false;

  constructor(private readonly api: Reader) {}

  start(): void {
    if (this.active) return;
    this.active = true;
    void this.refresh();
  }

  refresh(): Promise<void> {
    if (!this.active) return Promise.resolve();
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
    const controller = new AbortController();
    this.controller = controller;
    this.state.set({ ...this.state.get(), reading: true });
    try {
      const received = await this.api.get<ConversationSettingsView>(
        "/api/v1/conversation/settings",
        controller.signal,
      );
      if (controller.signal.aborted) return;
      const old = this.state.get();
      // A warming harness may omit its catalog; keep the last readable values.
      const view =
        received.enabled && !received.available && old.view
          ? { ...old.view, enabled: true, available: false, revision: received.revision }
          : received;
      this.state.set({ ...old, view, reading: false, readError: undefined });
      if (received.enabled && !received.available) this.retry();
      else this.delay = 1_000;
    } catch (err) {
      if (controller.signal.aborted) return;
      this.state.set({ ...this.state.get(), reading: false, readError: String(err) });
      this.retry();
    }
  }

  private retry(): void {
    if (!this.active) return;
    this.timer = setTimeout(() => {
      void this.refresh();
    }, this.delay);
    this.delay = Math.min(this.delay * 2, 30_000);
  }

  async save(sender: OpSender, payload: SetConversationSettingsPayload): Promise<void> {
    const before = this.state.get();
    if (!before.view?.available || before.saving || before.pendingId) return;
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
        operation: "conversation.set_settings",
        target: { type: "conversation" },
        expected_control_revision: before.view.revision,
        payload,
      });
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
    this.controller?.abort();
    await this.running;
  }
}
