import type { ApiClient } from "../api/client.ts";
import type { MessagingEvent, MessagingMessage, MessagingPage } from "../api/generated/api.ts";
import { EventStream } from "../live/eventStream.ts";
import { Store } from "../state/store.ts";

const messagesURL = "/api/v1/messaging/messages";
const workRefreshMs = 200;
const detailBatch = 8;

export class MessagingFeed {
  readonly state = new Store<{
    items: MessagingMessage[];
    loading: boolean;
    more: boolean;
    error: string;
  }>({ items: [], loading: true, more: false, error: "" });

  private controller: AbortController | undefined;
  private stopping: Promise<void> | undefined;
  private connected = false;
  private initialized = false;
  private reset = false;
  private forward = false;
  private dirty = new Set<string>();
  private workIssues = new Set<string>();
  private workTimer: ReturnType<typeof setTimeout> | undefined;
  private reading: Promise<void> | undefined;
  private paging: Promise<void> | undefined;
  private readonly stream: EventStream<MessagingEvent>;
  private quote: { id: string; invalidate: () => void } | undefined;

  constructor(private client: Pick<ApiClient, "get" | "openStream" | "noteAlive">) {
    this.stream = new EventStream<MessagingEvent>({
      open: (after, signal) =>
        client.openStream(
          `/api/v1/messaging/events${after === undefined ? "" : `?after=${after}`}`,
          signal,
        ),
      onStart: (_eventSeq, fresh) => {
        if (!this.controller || this.stopping) return;
        this.connected = true;
        this.reset ||= fresh || !this.initialized;
        this.forward = true;
        this.quote?.invalidate();
        void this.read();
      },
      onEvent: (event) => this.handleEvent(event),
      onAlive: () => {
        if (!this.controller || this.stopping) return;
        client.noteAlive();
        if (this.state.get().error) void this.read();
      },
      onLost: () => {
        this.connected = false;
        if (this.controller && !this.stopping)
          this.state.set({ ...this.state.get(), error: "连接已断开，正在恢复消息…" });
      },
      onReset: () => {
        this.connected = false;
        this.reset = true;
      },
    });
  }

  start() {
    if (this.controller) return;
    const controller = new AbortController();
    this.controller = controller;
    if (this.stopping) {
      void this.stopping.then(() => {
        if (!controller.signal.aborted) this.stream.start();
      });
    } else {
      this.stream.start();
    }
  }

  stop(): Promise<void> {
    if (!this.controller) return this.stopping ?? Promise.resolve();
    this.controller.abort();
    this.controller = undefined;
    this.connected = false;
    clearTimeout(this.workTimer);
    this.workTimer = undefined;
    // Drain aborted reads and the EventStream loop before restarting the same
    // stream. Its cursor remains valid and pending invalidations stay queued.
    const stopping = Promise.all([
      this.stopping,
      this.stream.stop(),
      this.reading,
      this.paging,
    ]).then(() => {
      if (this.stopping === stopping) this.stopping = undefined;
    });
    this.stopping = stopping;
    return stopping;
  }

  refresh() {
    this.forward = true;
    return this.read();
  }

  watchQuote(id: string, invalidate: () => void) {
    const quote = { id, invalidate };
    this.quote = quote;
    return () => {
      if (this.quote === quote) this.quote = undefined;
    };
  }

  private handleEvent(event: MessagingEvent) {
    if (!this.controller || this.stopping) return;
    const loadingPage = this.reset || this.forward || this.reading || this.paging;
    const items = this.state.get().items;
    if (event.kind === "messaging.work_changed") {
      const issueID = event.issue_id;
      if (
        !issueID ||
        (!loadingPage &&
          !items.some((m) => m.direction === "input" && m.issue_ids?.includes(issueID)))
      )
        return;
      this.workIssues.add(issueID);
      this.workTimer ??= setTimeout(() => {
        this.workTimer = undefined;
        void this.read();
      }, workRefreshMs);
      return;
    }
    if (event.kind === "messaging.turn_processing") {
      const loaded = new Set(items.map((m) => m.message_id));
      for (const id of event.message_ids ?? []) {
        if (this.quote?.id === id) this.quote.invalidate();
        if (loadingPage || loaded.has(id)) this.dirty.add(id);
      }
      void this.read();
      return;
    }
    if (event.object_type !== "message") return;
    if (this.quote?.id === event.object_id) this.quote.invalidate();
    if (event.kind === "messaging.input" || event.kind === "messaging.published")
      this.forward = true;
    if (loadingPage || items.some((m) => m.message_id === event.object_id))
      this.dirty.add(event.object_id);
    void this.read();
  }

  private queueChangedWork() {
    if (!this.workIssues.size) return;
    for (const message of this.state.get().items) {
      if (message.direction === "input" && message.issue_ids?.some((id) => this.workIssues.has(id)))
        this.dirty.add(message.message_id);
    }
    this.workIssues.clear();
  }

  private read(): Promise<void> {
    if (!this.controller || !this.connected) return Promise.resolve();
    if (
      !this.reset &&
      !this.forward &&
      !this.dirty.size &&
      (this.workTimer || !this.workIssues.size)
    )
      return this.reading ?? Promise.resolve();
    if (!this.reading) {
      const { signal } = this.controller;
      // Start after the current SSE chunk so its work is read together.
      this.reading = Promise.resolve()
        .then(() => this.readPending(signal))
        .then((complete) => {
          this.reading = undefined;
          if (complete) void this.read();
        });
    }
    return this.reading;
  }

  private async readPending(signal: AbortSignal) {
    while (!signal.aborted && this.connected) {
      if (this.paging) await this.paging.catch(() => {});
      if (signal.aborted || !this.connected) return false;
      if (!this.workTimer) this.queueChangedWork();
      const reset = this.reset;
      const forward = this.forward;
      const ids = [...this.dirty];
      if (!reset && !forward && !ids.length) return true;
      this.reset = false;
      this.forward = false;
      this.dirty.clear();
      const previous = this.state.get();
      try {
        const latest = reset
          ? await this.client.get<MessagingPage>(`${messagesURL}?latest=1`, signal)
          : undefined;
        const arrived = latest
          ? (latest.items ?? [])
          : forward
            ? await this.readAfter(previous.items.at(-1)?.sequence ?? 0, signal)
            : [];
        const updates: MessagingMessage[] = [];
        if (!reset) {
          const arrivedIDs = new Set(arrived.map((message) => message.message_id));
          const visibleIDs = new Set(this.state.get().items.map((message) => message.message_id));
          const remainingIDs = ids.filter((id) => visibleIDs.has(id) && !arrivedIDs.has(id));
          for (let i = 0; i < remainingIDs.length && !signal.aborted; i += detailBatch) {
            updates.push(
              ...(await Promise.all(
                remainingIDs
                  .slice(i, i + detailBatch)
                  .map((id) =>
                    this.client.get<MessagingMessage>(
                      `${messagesURL}/${encodeURIComponent(id)}`,
                      signal,
                    ),
                  ),
              )),
            );
          }
        }
        if (signal.aborted) throw signal.reason;
        if (this.reset) continue;
        const retained = this.state.get();
        const items = reset ? arrived : mergeMessages(retained.items, arrived, updates);
        this.initialized = true;
        this.state.set({
          items,
          loading: false,
          more: latest?.more ?? retained.more,
          error: "",
        });
      } catch (e) {
        this.reset ||= reset;
        this.forward ||= forward;
        for (const id of ids) this.dirty.add(id);
        if (!signal.aborted)
          this.state.set({
            ...this.state.get(),
            loading: false,
            error: e instanceof Error ? e.message : String(e),
          });
        return false;
      }
    }
    return false;
  }

  private async readAfter(after: number, signal: AbortSignal) {
    const items: MessagingMessage[] = [];
    while (!signal.aborted) {
      const page = await this.client.get<MessagingPage>(`${messagesURL}?after=${after}`, signal);
      items.push(...(page.items ?? []));
      if (!page.more || page.next <= after) break;
      after = page.next;
    }
    return items;
  }

  older(): Promise<void> {
    this.paging ??= this.readOlder().finally(() => {
      this.paging = undefined;
    });
    return this.paging;
  }

  private async readOlder() {
    const signal = this.controller?.signal;
    const before = this.state.get().items[0]?.sequence;
    if (!signal || !before) return;
    try {
      const page = await this.client.get<MessagingPage>(`${messagesURL}?before=${before}`, signal);
      const old = this.state.get();
      if (signal.aborted || old.items[0]?.sequence !== before) return;
      this.state.set({
        ...old,
        items: mergeMessages(page.items ?? [], old.items),
        more: page.more,
      });
    } catch (e) {
      if (!signal.aborted) throw e;
    }
  }
}

function mergeMessages(...groups: MessagingMessage[][]): MessagingMessage[] {
  const messages = new Map<string, MessagingMessage>();
  for (const group of groups)
    for (const message of group) messages.set(message.message_id, message);
  return [...messages.values()].sort((a, b) => a.sequence - b.sequence);
}
