// Server-owned task pages (D75). Every refresh starts from a fresh cursor;
// an older filter or page request cannot write into a new generation.
import type { TaskLifecycle, TaskPage, TaskSummary } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export const taskTabs = [
  {
    id: "current",
    title: "当前",
    lifecycle: ["draft", "queued", "running", "waiting", "awaiting_acceptance", "changes_required"],
  },
  { id: "ended", title: "已结束", lifecycle: ["completed", "cancelled"] },
  {
    id: "all",
    title: "全部",
    lifecycle: [
      "draft",
      "queued",
      "running",
      "waiting",
      "awaiting_acceptance",
      "changes_required",
      "completed",
      "cancelled",
    ],
  },
] satisfies { id: string; title: string; lifecycle: TaskLifecycle[] }[];
export interface FeedState {
  items: readonly TaskSummary[];
  pages: number;
  loading: boolean;
  paging: boolean;
  next?: string | undefined;
  error?: string | undefined;
}
const empty = (): FeedState => ({ items: [], pages: 0, loading: false, paging: false });

export class TaskFeed {
  readonly state = new Store<FeedState>(empty());
  private enabled = false;
  private project = "";
  private generation = 0;
  private controller: AbortController | null = null;
  private running: Promise<void> | null = null;
  private dirty = false;

  constructor(
    private readonly api: Reader,
    private readonly lifecycle: readonly TaskLifecycle[],
    private readonly limit = 50,
    private readonly completedOrder = false,
  ) {}
  start(): void {
    if (!this.enabled) {
      this.enabled = true;
      this.generation++;
      void this.refresh();
    }
  }
  setProject(project: string): void {
    if (this.project === project) return;
    this.cancel();
    this.project = project;
    this.state.set(empty());
    if (this.enabled) void this.refresh();
  }
  refresh(): Promise<void> {
    return this.read(false);
  }
  more(): Promise<void> {
    const state = this.state.get();
    if (!state.next || state.loading || state.paging || state.error) return Promise.resolve();
    return this.read(true);
  }
  private path(before?: string): string {
    const query = new URLSearchParams({
      all: "1",
      lifecycle: this.lifecycle.join(","),
      limit: String(this.limit),
    });
    if (this.completedOrder) query.set("sort", "completed_at");
    if (this.project) query.set("project", this.project);
    if (before) query.set("before", before);
    return `/api/v1/tasks?${query}`;
  }
  private read(append: boolean): Promise<void> {
    if (!this.enabled) return Promise.resolve();
    if (this.running) {
      if (!append) this.dirty = true;
      return this.running;
    }
    const generation = this.generation;
    const done = (async () => {
      let nextAppend = append;
      do {
        this.dirty = false;
        await this.readPage(nextAppend, generation);
        nextAppend = false;
      } while (this.enabled && generation === this.generation && this.dirty);
    })().finally(() => {
      if (this.running === done) this.running = null;
    });
    this.running = done;
    return done;
  }
  private async readPage(append: boolean, generation: number): Promise<void> {
    const previous = this.state.get();
    const controller = new AbortController();
    this.controller = controller;
    this.state.set({ ...previous, loading: !append, paging: append, error: undefined });
    try {
      let before = append ? previous.next : undefined;
      const byId = new Map<string, TaskSummary>(
        append ? previous.items.map((t) => [t.task_id, t]) : [],
      );
      const target = append ? 1 : Math.max(1, previous.pages);
      let pages = append ? previous.pages : 0;
      for (let i = 0; i < target; i++) {
        const page = await this.api.get<TaskPage>(this.path(before), controller.signal);
        if (controller.signal.aborted || generation !== this.generation) return;
        for (const task of page.tasks ?? []) {
          // The first occurrence belongs to the newer page. Never duplicate
          // a row that moved across a pagination boundary while reading.
          if (
            !byId.has(task.task_id) &&
            this.lifecycle.includes(task.lifecycle) &&
            (!this.project || task.project === this.project)
          )
            byId.set(task.task_id, task);
        }
        pages++;
        const next = page.next || undefined;
        if (next && next === before) throw new Error("任务分页游标没有前进，请重试");
        before = next;
        if (!before) break;
      }
      if (!controller.signal.aborted && generation === this.generation)
        this.state.set({
          items: [...byId.values()],
          pages,
          next: before,
          loading: false,
          paging: false,
        });
    } catch (err) {
      if (!controller.signal.aborted && generation === this.generation)
        this.state.set({
          ...this.state.get(),
          loading: false,
          paging: false,
          error: err instanceof Error ? err.message : String(err),
        });
    }
  }
  private cancel(): Promise<void> | null {
    this.generation++;
    this.dirty = false;
    this.controller?.abort();
    const running = this.running;
    this.running = null;
    return running;
  }
  async stop(): Promise<void> {
    this.enabled = false;
    const running = this.cancel();
    const generation = this.generation;
    await running;
    if (generation !== this.generation) return;
    this.state.set({ ...this.state.get(), loading: false, paging: false });
  }
}

export class TaskLists {
  readonly feeds: readonly TaskFeed[];
  readonly recent: TaskFeed;
  readonly project = new Store("");
  private timer: ReturnType<typeof setTimeout> | undefined;
  constructor(api: Reader) {
    this.feeds = taskTabs.map((tab) => new TaskFeed(api, tab.lifecycle));
    this.recent = new TaskFeed(api, ["completed"], 5, true);
  }
  setProject(project: string): void {
    this.project.set(project);
    for (const column of this.feeds) column.setProject(project);
  }
  open(index = 0): void {
    for (const [i, feed] of this.feeds.entries()) {
      if (i === index) feed.start();
      else void feed.stop();
    }
  }
  close(): void {
    for (const column of this.feeds) void column.stop();
  }
  refresh(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    for (const column of this.feeds) void column.refresh();
    void this.recent.refresh();
  }
  changed(): void {
    if (this.timer === undefined) this.timer = setTimeout(() => this.refresh(), 100);
  }
  async stop(): Promise<void> {
    clearTimeout(this.timer);
    this.timer = undefined;
    await Promise.all([...this.feeds.map((column) => column.stop()), this.recent.stop()]);
  }
}
