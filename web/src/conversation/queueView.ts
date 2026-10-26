// Queue and presentation blocks are independent of the raw conversation page.
import { ApiError } from "../api/client.ts";
import type {
  ConversationQueuePage,
  ConversationItem,
  LedgerEvent,
  TranscriptBlock,
  TranscriptPage,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export type Category = "owner" | "background" | "withdrawn";
interface QueueState {
  supported: boolean | undefined;
  pages: Partial<Record<Category, ConversationQueuePage>>;
  blocks: TranscriptBlock[];
  more: boolean;
  before: number;
  loaded: boolean;
  loadingEarlier: boolean;
  error: string | undefined;
}
export class ConversationQueue {
  readonly state = new Store<QueueState>({
    supported: undefined,
    pages: {},
    blocks: [],
    more: false,
    before: 0,
    loaded: false,
    loadingEarlier: false,
    error: undefined,
  });
  private readonly parents = new Map<string, Map<string, ConversationItem>>();
  private active = false;
  private running: Promise<void> | null = null;
  private dirty = false;
  private watermark = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private controller = new AbortController();
  constructor(
    private api: Reader,
    private currentItem: (item: ConversationItem) => ConversationItem = (item) => item,
  ) {}
  refreshChoices(): void {
    this.publish(this.state.get());
  }
  private publish(state: QueueState): void {
    this.state.set({
      ...state,
      blocks: state.blocks.map((block) => ({
        ...block,
        trigger: this.currentItem(block.trigger),
        ...(block.items ? { items: block.items.map(this.currentItem) } : {}),
      })),
    });
  }
  start(): void {
    this.active = true;
    void this.refresh();
  }
  stop(): void {
    this.active = false;
    this.controller.abort();
    clearTimeout(this.timer);
  }
  changed(event: LedgerEvent): void {
    if (!this.active) return;
    const item = event.item;
    if (item?.author === "main_agent" && item.reply_to) {
      const state = this.state.get();
      if (!state.blocks.some((b) => !b.notice_only && b.trigger.message_id === item.reply_to))
        this.readParent(item.reply_to, item);
      this.publish({
        ...state,
        blocks: state.blocks.map((b) => {
          if (b.notice_only || b.trigger.message_id !== item.reply_to) return b;
          const old = b.items?.find((it) => it.message_id === item.message_id);
          if ((old?.assistant_revision ?? 0) > (item.assistant_revision ?? 0)) return b;
          return {
            ...b,
            items: [
              ...(b.items ?? []).filter((it) => it.message_id !== item.message_id),
              item,
            ].sort((a, b) => a.seq - b.seq),
          };
        }),
      });
    }
    void this.refresh();
  }
  private readParent(id: string, item: ConversationItem): void {
    const pending = this.parents.get(id);
    if (pending) {
      const old = pending.get(item.message_id);
      if ((old?.assistant_revision ?? 0) <= (item.assistant_revision ?? 0))
        pending.set(item.message_id, item);
      return;
    }
    const items = new Map([[item.message_id, item]]);
    this.parents.set(id, items);
    void this.api
      .get<TranscriptBlock>(
        `/api/v1/conversation/blocks/${encodeURIComponent(id)}`,
        this.controller.signal,
      )
      .then((block) => {
        if (!this.active) return;
        const state = this.state.get();
        const current = state.blocks.find((b) => b.position === block.position);
        const merged = mergeOutput(
          [...(block.items ?? []), ...(current?.items ?? [])],
          [...items.values()],
        );
        this.publish({
          ...state,
          blocks: [
            ...state.blocks.filter((b) => b.position !== block.position),
            { ...block, items: merged },
          ].sort((a, b) => a.position - b.position),
        });
      })
      .catch((error) => {
        if (this.active) this.publish({ ...this.state.get(), error: String(error) });
      })
      .finally(() => this.parents.delete(id));
  }
  refresh(): Promise<void> {
    if (!this.active) return Promise.resolve();
    if (this.running) {
      this.dirty = true;
      return this.running;
    }
    clearTimeout(this.timer);
    this.running = (async () => {
      do {
        this.dirty = false;
        try {
          const pages: Partial<Record<Category, ConversationQueuePage>> = {};
          for (const category of ["owner", "background", "withdrawn"] as const) {
            let page = await this.api.get<ConversationQueuePage>(
              `/api/v1/conversation/queue?category=${category}`,
              this.controller.signal,
            );
            const wanted = this.state.get().pages[category]?.items?.length ?? 0;
            while (page.next && (page.items ?? []).length < wanted) {
              const next = await this.api.get<ConversationQueuePage>(
                `/api/v1/conversation/queue?category=${category}&after=${page.next}`,
                this.controller.signal,
              );
              page = { ...next, items: [...(page.items ?? []), ...(next.items ?? [])] };
            }
            pages[category] = page;
          }
          const transcript = await this.api.get<TranscriptPage>(
            "/api/v1/conversation/transcript",
            this.controller.signal,
          );
          if (!this.active) return;
          const fetched = [...(transcript.blocks ?? [])];
          const previous = this.state.get();
          // Reconnect can skip more than one page. Fill the gap before joining
          // the latest page to retained history, keeping the old scroll anchor.
          const newestPrevious = previous.blocks.at(-1)?.position;
          let bridge = transcript;
          while (
            newestPrevious !== undefined &&
            bridge.more &&
            bridge.next_before &&
            (bridge.blocks?.[0]?.position ?? 0) > newestPrevious
          ) {
            const before = bridge.next_before;
            bridge = await this.api.get<TranscriptPage>(
              `/api/v1/conversation/transcript?before=${before}`,
              this.controller.signal,
            );
            if (bridge.next_before && bridge.next_before >= before)
              throw new Error("对话分页尚未前进，请重新读取");
            fetched.push(...(bridge.blocks ?? []));
          }
          for (const old of previous.blocks)
            if (
              !old.notice_only &&
              old.trigger.state === "processing" &&
              !fetched.some((b) => b.position === old.position)
            ) {
              fetched.push(
                await this.api.get<TranscriptBlock>(
                  `/api/v1/conversation/blocks/${encodeURIComponent(old.trigger.message_id)}`,
                  this.controller.signal,
                ),
              );
            }
          for (let i = 0; i < fetched.length; i++) {
            let block = fetched[i]!;
            const wanted =
              previous.blocks.find((b) => b.position === block.position)?.items?.length ?? 0;
            while (!block.notice_only && block.more && (block.items?.length ?? 0) < wanted) {
              const next = await this.api.get<TranscriptBlock>(
                `/api/v1/conversation/blocks/${encodeURIComponent(block.trigger.message_id)}?after=${block.next_seq ?? 0}`,
                this.controller.signal,
              );
              block = { ...next, items: mergeOutput(block.items ?? [], next.items ?? []) };
            }
            fetched[i] = block;
          }
          if (!this.active) return;
          const watermark = Math.min(
            transcript.event_seq,
            ...Object.values(pages).map((p) => p.event_seq),
          );
          if (watermark < this.watermark) {
            this.dirty = true;
            continue;
          }
          this.watermark = watermark;
          const old = this.state.get();
          const blocks = new Map(old.blocks.map((b) => [b.position, b]));
          for (const b of fetched) {
            const previous = blocks.get(b.position);
            // Retain explicitly loaded continuation pages, replacing changed items by ID.
            const items = new Map(previous?.items?.map((it) => [it.message_id, it]));
            for (const it of b.items ?? [])
              if (
                (items.get(it.message_id)?.assistant_revision ?? 0) <= (it.assistant_revision ?? 0)
              )
                items.set(it.message_id, it);
            blocks.set(b.position, {
              ...b,
              items: [...items.values()].sort((a, b) => a.seq - b.seq),
            });
          }
          this.publish({
            ...old,
            supported: true,
            pages,
            blocks: [...blocks.values()].sort((a, b) => a.position - b.position),
            more: old.loaded ? old.more : transcript.more,
            before: old.before || transcript.next_before || 0,
            loaded: true,
            error: undefined,
          });
        } catch (error) {
          if (!this.active) return;
          if (error instanceof ApiError && error.status === 404) {
            this.publish({ ...this.state.get(), supported: false, error: undefined });
          } else {
            this.publish({
              ...this.state.get(),
              error: error instanceof Error ? error.message : String(error),
            });
            this.timer = setTimeout(() => void this.refresh(), 3000);
          }
        }
      } while (this.dirty && this.active);
    })().finally(() => {
      this.running = null;
    });
    return this.running;
  }
  async moreQueue(category: Category): Promise<void> {
    const current = this.state.get().pages[category];
    if (!current?.next) return;
    const page = await this.api.get<ConversationQueuePage>(
      `/api/v1/conversation/queue?category=${category}&after=${current.next}`,
    );
    if (this.state.get().pages[category] !== current) return;
    const items = new Map(
      [...(current.items ?? []), ...(page.items ?? [])].map((it) => [it.item.message_id, it]),
    );
    this.publish({
      ...this.state.get(),
      pages: { ...this.state.get().pages, [category]: { ...page, items: [...items.values()] } },
    });
  }
  async earlier(): Promise<void> {
    const current = this.state.get();
    if (!current.more || current.loadingEarlier) return;
    this.publish({ ...current, loadingEarlier: true });
    try {
      const page = await this.api.get<TranscriptPage>(
        `/api/v1/conversation/transcript?before=${current.before}`,
      );
      const now = this.state.get();
      const blocks = new Map([...(page.blocks ?? []), ...now.blocks].map((b) => [b.position, b]));
      this.publish({
        ...now,
        blocks: [...blocks.values()].sort((a, b) => a.position - b.position),
        more: page.more,
        before: page.next_before || 0,
      });
    } catch (error) {
      this.publish({ ...this.state.get(), error: String(error) });
    } finally {
      this.publish({ ...this.state.get(), loadingEarlier: false });
    }
  }
  async moreBlock(block: TranscriptBlock): Promise<void> {
    const page = await this.api.get<TranscriptBlock>(
      `/api/v1/conversation/blocks/${encodeURIComponent(block.trigger.message_id)}?after=${block.next_seq || 0}`,
    );
    const now = this.state.get();
    this.publish({
      ...now,
      blocks: now.blocks.map((b) =>
        b.position === block.position
          ? {
              ...page,
              items: mergeOutput(b.items ?? [], page.items ?? []),
            }
          : b,
      ),
    });
  }
}

function mergeOutput(old: ConversationItem[], incoming: ConversationItem[]): ConversationItem[] {
  const items = new Map(old.map((it) => [it.message_id, it]));
  for (const it of incoming)
    if ((items.get(it.message_id)?.assistant_revision ?? 0) <= (it.assistant_revision ?? 0))
      items.set(it.message_id, it);
  return [...items.values()].sort((a, b) => a.seq - b.seq);
}
