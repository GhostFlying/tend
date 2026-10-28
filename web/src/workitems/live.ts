// Server projections stay authoritative; events invalidate reads, never replay plans.
import type {
  BackgroundSourcePage,
  FollowupPage,
  FollowupHistoryPage,
  WorkItemPage,
  WorkItemView,
} from "../api/generated/api.ts";
import type { OpSender } from "../app/ops.ts";
import { ChoiceResponse } from "./choice.ts";
import { DeliveryRefresh } from "./refresh.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export interface ReadState<T> {
  value: T | null;
  loading: boolean;
  error?: string | undefined;
}

/** A cancellable, coalesced read with bounded failure backoff. */
export class WorkItemRead<T> {
  readonly state = new Store<ReadState<T>>({ value: null, loading: false });
  private path: string | null = null;
  private generation = 0;
  private controller: AbortController | null = null;
  private running: Promise<void> | null = null;
  private dirty = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay = 1_000;

  constructor(
    private api: Reader,
    private received: (value: T) => T | void = () => {},
  ) {}

  open(path: string): Promise<void> {
    if (this.path === path) return this.refresh();
    this.close();
    this.path = path;
    this.state.set({ value: null, loading: false });
    return this.refresh();
  }

  refresh(): Promise<void> {
    if (!this.path) return Promise.resolve();
    clearTimeout(this.timer);
    this.timer = undefined;
    if (this.running) {
      this.dirty = true;
      return this.running;
    }
    const generation = this.generation;
    const run = (async () => {
      do {
        // A dirty read retries immediately; it supersedes the previous backoff.
        clearTimeout(this.timer);
        this.timer = undefined;
        this.dirty = false;
        const controller = new AbortController();
        this.controller = controller;
        this.state.set({ ...this.state.get(), loading: true });
        try {
          const received = await this.api.get<T>(this.path!, controller.signal);
          if (controller.signal.aborted || generation !== this.generation) return;
          const transformed = this.received(received);
          const value = transformed === undefined ? received : transformed;
          this.state.set({ value, loading: false });
          clearTimeout(this.timer);
          this.timer = undefined;
          this.delay = 1_000;
        } catch (error) {
          if (controller.signal.aborted || generation !== this.generation) return;
          this.state.set({
            ...this.state.get(),
            loading: false,
            error: error instanceof Error ? error.message : String(error),
          });
          clearTimeout(this.timer);
          this.timer = setTimeout(() => void this.refresh(), this.delay);
          this.delay = Math.min(30_000, this.delay * 2);
        }
      } while (this.dirty && generation === this.generation && this.path);
    })().finally(() => {
      if (this.running === run) this.running = null;
    });
    this.running = run;
    return run;
  }

  close(): void {
    this.generation++;
    this.path = null;
    this.controller?.abort();
    this.running = null;
    this.dirty = false;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.delay = 1_000;
    this.state.set({ value: null, loading: false });
  }
}

export class WorkItems {
  readonly backgroundSources: WorkItemRead<BackgroundSourcePage>;
  readonly followups: WorkItemRead<FollowupPage>;
  readonly followupHistory: WorkItemRead<FollowupHistoryPage>;

  private readonly choiceActions = new Map<string, ChoiceResponse>();
  choiceResponse(id: string, sender: OpSender, changed: () => void): ChoiceResponse {
    let response = this.choiceActions.get(id);
    if (!response) {
      response = new ChoiceResponse(sender, changed);
      this.choiceActions.set(id, response);
    }
    return response;
  }
  private readonly refreshActions = new Map<string, DeliveryRefresh>();
  deliveryRefresh(workItemId: string, deliveryId: string, sender: OpSender): DeliveryRefresh {
    const key = `${workItemId}/${deliveryId}`;
    let action = this.refreshActions.get(key);
    if (!action) {
      action = new DeliveryRefresh(sender, () => this.refresh());
      this.refreshActions.set(key, action);
    }
    return action;
  }

  readonly cache = new Store<ReadonlyMap<string, WorkItemView>>(new Map());
  readonly board: WorkItemRead<WorkItemPage>;
  readonly overview: WorkItemRead<WorkItemPage>;
  readonly detail: WorkItemRead<WorkItemView>;
  readonly history: WorkItemRead<WorkItemView>;
  readonly project = new Store("");
  readonly selected = new Store<string | null>(null);
  private boardOpen = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private lookups = new Map<string, WorkItemRead<WorkItemView>>();
  private stopped = false;

  constructor(api: Reader) {
    this.backgroundSources = new WorkItemRead(api);
    this.followups = new WorkItemRead(api);
    this.followupHistory = new WorkItemRead(api);
    const remember = (item: WorkItemView): WorkItemView => {
      const current = this.cache.get().get(item.work_item_id);
      if (current) {
        if (current.revision > item.revision) return current;
        if (current.revision === item.revision) {
          const currentFacts = current.facts_seq;
          const incomingFacts = item.facts_seq;
          // Older servers have no facts_seq; preserve their last-response-wins
          // behavior until both sides carry a comparable transaction watermark.
          if (currentFacts !== undefined && incomingFacts !== undefined) {
            if (currentFacts >= incomingFacts) return current;
          }
        }
      }
      const cache = new Map(this.cache.get());
      cache.set(item.work_item_id, item);
      this.cache.set(cache);
      return item;
    };
    const page = (value: WorkItemPage): WorkItemPage => ({
      ...value,
      items: (value.items ?? []).map(remember),
    });
    this.board = new WorkItemRead(api, page);
    this.overview = new WorkItemRead(api, page);
    this.detail = new WorkItemRead(api, remember);
    this.history = new WorkItemRead(api);
    this.api = api;
    this.remember = remember;
  }
  private api: Reader;
  private remember: (item: WorkItemView) => WorkItemView;

  startOverview(): void {
    if (!this.stopped) void this.overview.open("/api/v1/work-items?include_closed=false");
  }
  openBoard(): void {
    if (this.stopped) return;
    this.boardOpen = true;
    void this.board.open(this.boardPath());
  }
  closeBoard(): void {
    this.boardOpen = false;
    this.board.close();
  }
  setProject(project: string): void {
    if (project === this.project.get()) return;
    this.project.set(project);
    if (this.boardOpen) void this.board.open(this.boardPath());
  }
  private boardPath(): string {
    const query = new URLSearchParams({ include_closed: "true" });
    if (this.project.get()) query.set("project", this.project.get());
    return `/api/v1/work-items?${query}`;
  }
  open(id: string): Promise<void> {
    this.selected.set(id);
    return this.detail.open(`/api/v1/work-items/${encodeURIComponent(id)}`);
  }
  close(): void {
    this.selected.set(null);
    this.detail.close();
    this.history.close();
    this.backgroundSources.close();
    this.followups.close();
    this.followupHistory.close();
  }
  lookUp(id: string): void {
    if (this.stopped || this.cache.get().has(id) || this.lookups.has(id)) return;
    const read = new WorkItemRead(this.api, this.remember);
    this.lookups.set(id, read);
    void read.open(`/api/v1/work-items/${encodeURIComponent(id)}`);
  }
  changed(): void {
    if (this.stopped || this.timer !== undefined) return;
    this.timer = setTimeout(() => this.refresh(), 100);
  }
  refresh(): void {
    if (this.stopped) return;
    clearTimeout(this.timer);
    this.timer = undefined;
    void this.board.refresh();
    void this.overview.refresh();
    void this.detail.refresh();
    void this.history.refresh();
    void this.backgroundSources.refresh();
    void this.followups.refresh();
    void this.followupHistory.refresh();
    for (const read of this.lookups.values()) void read.refresh();
  }
  stop(): void {
    this.stopped = true;
    clearTimeout(this.timer);
    this.board.close();
    this.overview.close();
    this.detail.close();
    this.history.close();
    this.backgroundSources.close();
    this.followups.close();
    this.followupHistory.close();
    for (const read of this.lookups.values()) read.close();
  }
}
