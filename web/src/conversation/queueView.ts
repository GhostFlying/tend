import { ApiError } from "../api/client.ts";
import type {
  IssueConversationQueuePage as ConversationQueuePage,
  LedgerEvent,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export type Category = "owner" | "withdrawn";
interface QueueState {
  supported: boolean | undefined;
  pages: Partial<Record<Category, ConversationQueuePage>>;
  error: string | undefined;
}

export class ConversationQueue {
  readonly state = new Store<QueueState>({ supported: undefined, pages: {}, error: undefined });
  private controller: AbortController | undefined;
  private running: Promise<void> | null = null;
  private dirty = false;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(private readonly api: Reader) {}

  start(): void {
    if (this.controller) return;
    this.controller = new AbortController();
    void this.refresh();
  }

  stop(): void {
    this.controller?.abort();
    this.controller = undefined;
    clearTimeout(this.timer);
  }

  changed(event: LedgerEvent): void {
    if (
      event.kind === "conversation.message_state" ||
      event.kind === "conversation.processing_stopped" ||
      event.kind === "conversation.message_withdrawn" ||
      event.kind === "conversation.urgent_consumed" ||
      (event.kind === "conversation.message" && event.item?.control)
    ) {
      void this.refresh();
    }
  }

  refresh(): Promise<void> {
    if (!this.controller) return Promise.resolve();
    this.dirty = true;
    if (this.running) return this.running;
    clearTimeout(this.timer);
    this.running = (async () => {
      while (this.dirty && this.controller) {
        this.dirty = false;
        const { signal } = this.controller;
        const read = async (category: Category) => {
          const page = await this.readCategory(category, signal);
          if (signal.aborted) return;
          const state = this.state.get();
          this.state.set({
            ...state,
            supported: category === "owner" ? true : state.supported,
            pages: { ...state.pages, [category]: page },
          });
        };
        const [owner, withdrawn] = await Promise.allSettled([read("owner"), read("withdrawn")]);
        if (signal.aborted) continue;
        if (
          owner.status === "rejected" &&
          owner.reason instanceof ApiError &&
          owner.reason.status === 404
        ) {
          this.state.set({ ...this.state.get(), supported: false, error: undefined });
          continue;
        }
        const failure = owner.status === "rejected" ? owner : withdrawn;
        this.state.set({
          ...this.state.get(),
          error: failure.status === "rejected" ? String(failure.reason) : undefined,
        });
        if (failure.status === "rejected") {
          this.timer = setTimeout(() => void this.refresh(), 3000);
        }
      }
    })().finally(() => {
      this.running = null;
    });
    return this.running;
  }

  private page(category: Category, signal: AbortSignal, after?: number) {
    const query = new URLSearchParams({ category, projection: "chat" });
    if (after) query.set("after", String(after));
    return this.api.get<ConversationQueuePage>(`/api/v1/conversation/queue?${query}`, signal);
  }

  private async readCategory(
    category: Category,
    signal: AbortSignal,
  ): Promise<ConversationQueuePage> {
    const wanted = this.state.get().pages[category]?.items?.length ?? 0;
    let page = await this.page(category, signal);
    while (page.next && (page.items?.length ?? 0) < wanted) {
      const next = await this.page(category, signal, page.next);
      page = { ...next, items: [...(page.items ?? []), ...(next.items ?? [])] };
    }
    return page;
  }

  async moreQueue(category: Category): Promise<void> {
    const current = this.state.get().pages[category];
    const signal = this.controller?.signal;
    if (!current?.next || !signal) return;
    const page = await this.page(category, signal, current.next);
    if (signal.aborted || this.state.get().pages[category] !== current) return;
    const items = new Map(
      [...(current.items ?? []), ...(page.items ?? [])].map((it) => [it.item.message_id, it]),
    );
    this.state.set({
      ...this.state.get(),
      pages: {
        ...this.state.get().pages,
        [category]: { ...page, items: [...items.values()] },
      },
    });
  }
}
