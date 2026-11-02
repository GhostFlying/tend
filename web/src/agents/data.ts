// Public registration and executor observations remain separate server facts.
import type {
  AgentCategoryPage,
  HostedAgentInventory,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";
import type { ReadState } from "../workitems/live.ts";
import { WorkItemRead } from "../workitems/live.ts";

export class AgentCatalog {
  readonly categories: WorkItemRead<AgentCategoryPage>;
  readonly hosted: WorkItemRead<HostedAgentInventory>;
  readonly runner: WorkItemRead<RunnerAgentCatalog>;
  readonly names = new Store<ReadonlyMap<string, string>>(new Map());
  readonly catalogs = new Store<ReadonlyMap<string, ReadState<RunnerAgentCatalog>>>(new Map());
  private categoryCursors: string[] = [];
  private categoryState: "active" | "retired" | "all" = "active";
  private readonly runners = new Map<string, WorkItemRead<RunnerAgentCatalog>>();

  constructor(private api: Reader) {
    this.categories = new WorkItemRead({
      get: async <T>(path: string, signal?: AbortSignal): Promise<T> =>
        (await this.readCategoryPages(path, signal)) as T,
    });
    this.hosted = new WorkItemRead(api);
    this.runner = new WorkItemRead(api);
    this.hosted.state.subscribe(() => this.updateNames());
  }

  openCategories(state: "active" | "retired" | "all" = "active", after?: string): Promise<void> {
    if (!after || state !== this.categoryState) this.categoryCursors = [];
    if (after && !this.categoryCursors.includes(after)) this.categoryCursors.push(after);
    this.categoryState = state;
    return this.categories.open(`/api/v1/categories?${new URLSearchParams({ state })}`);
  }

  private async readCategoryPages(path: string, signal?: AbortSignal): Promise<AgentCategoryPage> {
    const pages = this.categoryCursors.length + 1;
    const cursors: string[] = [];
    const items = new Map<string, NonNullable<AgentCategoryPage["items"]>[number]>();
    let nextPath = path;
    let result: AgentCategoryPage;
    for (let page = 0; ; page++) {
      signal?.throwIfAborted();
      result = await this.api.get<AgentCategoryPage>(nextPath, signal);
      signal?.throwIfAborted();
      for (const category of result.items ?? []) items.set(category.category_id, category);
      if (page + 1 >= pages || !result.more || !result.next) break;
      cursors.push(result.next);
      nextPath = `${path}&after=${encodeURIComponent(result.next)}`;
    }
    if (pages === this.categoryCursors.length + 1) this.categoryCursors = cursors;
    return { ...result, items: [...items.values()] };
  }

  openHosted(): Promise<void> {
    return this.hosted.open("/api/v1/hosted-agents");
  }

  openRunner(hostID: string): Promise<void> {
    return this.runner.open(`/api/v1/hosts/${encodeURIComponent(hostID)}/agents`);
  }

  runnerFor(hostID: string): WorkItemRead<RunnerAgentCatalog> {
    let read = this.runners.get(hostID);
    if (!read) {
      read = new WorkItemRead(this.api);
      this.runners.set(hostID, read);
      const current = read;
      read.state.subscribe(() => {
        this.catalogs.set(new Map(this.catalogs.get()).set(hostID, current.state.get()));
        this.updateNames();
      });
    }
    return read;
  }

  private updateNames(): void {
    const names = new Map<string, string>();
    for (const read of this.runners.values()) {
      const catalog = read.state.get().value;
      for (const agent of [
        ...(catalog?.retained_definitions ?? []),
        ...(catalog?.projection?.agents ?? []),
      ])
        names.set(
          `${agent.identity.execution_environment_id}/${agent.identity.local_id}`,
          agent.display_name,
        );
    }
    for (const agent of this.hosted.state.get().value?.projection?.agents ?? [])
      names.set(
        `${agent.identity.execution_environment_id}/${agent.identity.local_id}`,
        agent.display_name,
      );
    const before = this.names.get();
    if (before.size !== names.size || [...names].some(([key, value]) => before.get(key) !== value))
      this.names.set(names);
  }

  ensureRunner(hostID: string, refresh = false): void {
    const read = this.runnerFor(hostID);
    if (!read.state.get().value && !read.state.get().loading && !read.state.get().error)
      void read.open(`/api/v1/hosts/${encodeURIComponent(hostID)}/agents`);
    else if (refresh) void read.refresh();
  }

  refresh(): void {
    void this.categories.refresh();
    for (const read of [this.hosted, this.runner, ...this.runners.values()]) void read.refresh();
  }

  stop(): void {
    this.categoryCursors = [];
    for (const read of [this.categories, this.hosted, this.runner, ...this.runners.values()])
      read.close();
  }
}
