// Indexed handoff status (D196). No background summary production or settings writes.
import type { ConversationHandoffView } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import type { OpSender } from "../app/ops.ts";
import { Store } from "../state/store.ts";

export interface HandoffState {
  view: ConversationHandoffView | null;
  reading: boolean;
  saving: boolean;
  error?: string | undefined;
  readError?: string | undefined;
  pendingId?: string | undefined;
}

export class ConversationHandoff {
  readonly state = new Store<HandoffState>({ view: null, reading: false, saving: false });
  private controller: AbortController | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay = 1000;
  private lastEvent = 0;
  private active = false;
  private running: Promise<void> | null = null;
  private again = false;
  constructor(private readonly api: Reader) {}
  start(): void {
    if (this.active) return;
    this.active = true;
    void this.refresh();
  }
  invalidate(seq: number): void {
    if (seq <= this.lastEvent) return;
    this.lastEvent = seq;
    if (!this.active || this.timer !== undefined) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.refresh();
    }, 1000);
  }
  refresh(): Promise<void> {
    if (!this.active) return Promise.resolve();
    if (this.running) {
      this.again = true;
      return this.running;
    }
    clearTimeout(this.timer);
    this.timer = undefined;
    this.running = (async () => {
      do {
        this.again = false;
        const controller = new AbortController();
        this.controller = controller;
        this.state.set({ ...this.state.get(), reading: true });
        try {
          const view = await this.api.get<ConversationHandoffView>(
            "/api/v1/conversation/handoff",
            controller.signal,
          );
          if (controller.signal.aborted) return;
          this.state.set({ ...this.state.get(), view, reading: false, readError: undefined });
          this.delay = 1000;
          if (
            view.enabled &&
            (view.pending || ["pending", "sending"].includes(view.maintenance?.status ?? ""))
          )
            this.retry();
        } catch (err) {
          if (controller.signal.aborted) return;
          this.state.set({ ...this.state.get(), reading: false, readError: String(err) });
          this.retry();
        }
      } while (this.again && this.active);
    })().finally(() => {
      this.running = null;
    });
    return this.running;
  }
  private retry(): void {
    if (!this.active) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.refresh();
    }, this.delay);
    this.delay = Math.min(this.delay * 2, 30000);
  }
  async reconcile(_sender: OpSender): Promise<void> {
    await this.refresh();
  }
  async stop(): Promise<void> {
    this.active = false;
    this.again = false;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.controller?.abort();
    await this.running;
  }
}
