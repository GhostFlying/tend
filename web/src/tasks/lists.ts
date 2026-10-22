// Server-owned task pages (D75). Every refresh starts from a fresh cursor;
// an older filter or page request cannot write into a new generation.
import type { TaskLifecycle, TaskPage, TaskSummary } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export const stages: { id: string; title: string; lifecycle: TaskLifecycle[] }[] = [
  { id: "pending", title: "待开始", lifecycle: ["draft", "queued"] },
  { id: "running", title: "实现中", lifecycle: ["running"] },
  { id: "accepting", title: "验收中", lifecycle: ["awaiting_acceptance"] },
  { id: "changes", title: "待返工", lifecycle: ["changes_required"] },
  { id: "completed", title: "已完成", lifecycle: ["completed"] },
  { id: "cancelled", title: "已取消", lifecycle: ["cancelled"] },
];
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

  constructor(
    private readonly api: Reader,
    private readonly lifecycle: readonly TaskLifecycle[],
    private readonly limit = 50,
  ) {}
  start(): void {
    if (!this.enabled) {
      this.enabled = true;
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
    if (this.project) query.set("project", this.project);
    if (before) query.set("before", before);
    return `/api/v1/tasks?${query}`;
  }
  private read(append: boolean): Promise<void> {
    if (!this.enabled) return Promise.resolve();
    const previous = this.state.get();
    this.cancel();
    const generation = this.generation;
    const controller = new AbortController();
    this.controller = controller;
    this.state.set({ ...previous, loading: !append, paging: append, error: undefined });
    const done = (async () => {
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
    })();
    this.running = done;
    return done;
  }
  private cancel(): void {
    this.generation++;
    this.controller?.abort();
  }
  async stop(): Promise<void> {
    this.enabled = false;
    this.cancel();
    const generation = this.generation;
    await this.running;
    if (generation !== this.generation) return;
    this.state.set({ ...this.state.get(), loading: false, paging: false });
  }
}

export class TaskLists {
  readonly columns: readonly TaskFeed[];
  readonly recent: TaskFeed;
  readonly project = new Store("");
  private timer: ReturnType<typeof setTimeout> | undefined;
  constructor(api: Reader) {
    this.columns = stages.map((stage) => new TaskFeed(api, stage.lifecycle));
    this.recent = new TaskFeed(api, ["completed"], 5);
  }
  setProject(project: string): void {
    this.project.set(project);
    for (const column of this.columns) column.setProject(project);
  }
  open(): void {
    for (const column of this.columns) column.start();
  }
  close(): void {
    for (const column of this.columns) void column.stop();
  }
  refresh(): void {
    clearTimeout(this.timer);
    for (const column of this.columns) void column.refresh();
    void this.recent.refresh();
  }
  changed(): void {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.refresh(), 100);
  }
  async stop(): Promise<void> {
    clearTimeout(this.timer);
    await Promise.all([...this.columns.map((column) => column.stop()), this.recent.stop()]);
  }
}
