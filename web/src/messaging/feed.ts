import type { ApiClient } from "../api/client.ts";
import type { MessagingMessage, MessagingPage } from "../api/generated/api.ts";
import { EventStream } from "../live/eventStream.ts";
import { Store } from "../state/store.ts";

export class MessagingFeed {
  readonly state = new Store<{
    items: MessagingMessage[];
    loading: boolean;
    more: boolean;
    error: string;
  }>({ items: [], loading: true, more: false, error: "" });
  private controller = new AbortController();
  private running = false;
  private pending = false;
  private dirty = new Set<string>();
  private stream: EventStream;
  constructor(private client: Pick<ApiClient, "get" | "openStream" | "noteAlive">) {
    this.stream = new EventStream({
      open: (after, signal) =>
        client.openStream(
          `/api/v1/messaging/events${after === undefined ? "" : `?after=${after}`}`,
          signal,
        ),
      onStart: () => {
        for (const m of this.state.get().items)
          if (m.interaction?.state === "open" || m.direction === "input")
            this.dirty.add(m.message_id);
        void this.refresh();
      },
      onEvent: (event) => {
        if (event.object_type === "message") this.dirty.add(event.object_id);
        if (event.kind === "messaging.work_changed" || event.kind === "messaging.processing") {
          for (const m of this.state.get().items)
            if (m.direction === "input") this.dirty.add(m.message_id);
        }
        void this.refresh();
      },
      onAlive: () => {
        client.noteAlive();
        if (this.state.get().error && this.dirty.size) void this.refresh();
      },
      onLost: () => this.state.set({ ...this.state.get(), error: "连接已断开，正在恢复消息…" }),
    });
  }
  start() {
    if (this.running) return;
    this.running = true;
    this.stream.start();
  }
  async stop() {
    this.running = false;
    this.controller.abort();
    await this.stream.stop();
  }
  async refresh() {
    this.pending = true;
    if (this.runningRefresh) return;
    this.runningRefresh = true;
    let inflight: string[] = [];
    try {
      while (this.pending && this.running) {
        this.pending = false;
        const ids = [...this.dirty];
        inflight = ids;
        this.dirty.clear();
        const page = await this.client.get<MessagingPage>(
          "/api/v1/messaging/messages?latest=1",
          this.controller.signal,
        );
        const current = new Set((page.items ?? []).map((m) => m.message_id));
        const updates = await Promise.all(
          ids
            .filter(
              (id) => !current.has(id) && this.state.get().items.some((m) => m.message_id === id),
            )
            .map((id) =>
              this.client.get<MessagingMessage>(
                `/api/v1/messaging/messages/${encodeURIComponent(id)}`,
                this.controller.signal,
              ),
            ),
        );
        if (!this.running) return;
        const old = this.state.get();
        const last = old.items.at(-1)?.sequence;
        const arrived = [...(page.items ?? [])];
        // Resume every missing page after a long disconnect, even if the newest
        // window no longer overlaps the messages already on screen.
        if (last !== undefined && arrived[0] && arrived[0].sequence > last) {
          let after = last;
          while (this.running) {
            const missing = await this.client.get<MessagingPage>(
              `/api/v1/messaging/messages?after=${after}`,
              this.controller.signal,
            );
            arrived.push(...(missing.items ?? []));
            if (!missing.more || missing.next <= after) break;
            after = missing.next;
          }
        }
        if (!this.running) return;
        const retained = this.state.get();
        const messages = new Map(retained.items.map((m) => [m.message_id, m]));
        for (const item of [...arrived, ...updates]) messages.set(item.message_id, item);
        this.state.set({
          items: [...messages.values()].sort((a, b) => a.sequence - b.sequence),
          loading: false,
          more: retained.items.length ? retained.more : page.more,
          error: "",
        });
        inflight = [];
      }
    } catch (e) {
      for (const id of inflight) this.dirty.add(id);
      if (!this.controller.signal.aborted)
        this.state.set({
          ...this.state.get(),
          loading: false,
          error: e instanceof Error ? e.message : String(e),
        });
    } finally {
      this.runningRefresh = false;
    }
  }
  private runningRefresh = false;
  async older() {
    const before = this.state.get().items[0]?.sequence;
    if (!before) return;
    const page = await this.client.get<MessagingPage>(
      `/api/v1/messaging/messages?before=${before}`,
      this.controller.signal,
    );
    if (!this.running) return;
    const old = this.state.get();
    const messages = new Map([...(page.items ?? []), ...old.items].map((m) => [m.message_id, m]));
    this.state.set({
      ...old,
      items: [...messages.values()].sort((a, b) => a.sequence - b.sequence),
      more: page.more,
    });
  }
}
