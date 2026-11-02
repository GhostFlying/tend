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
  private categoryAfter = "";
  private categoryEpoch = 0;
  private categoryState: "active" | "retired" | "all" = "active";
  private categoryCursors: string[] = [];
  private refreshingCategories: Promise<void> | null = null;
  private categoryItems: NonNullable<AgentCategoryPage["items"]> = [];
  private readonly runners = new Map<string, WorkItemRead<RunnerAgentCatalog>>();

  constructor(private api: Reader) {
    this.categories = new WorkItemRead(api, (page) => {
      if (!this.categoryAfter) {
        this.categoryItems = page.items ?? [];
        return page;
      }
      const items = new Map(this.categoryItems.map((c) => [c.category_id, c]));
      for (const category of page.items ?? []) items.set(category.category_id, category);
      this.categoryItems = [...items.values()].sort((a, b) =>
        a.category_id.localeCompare(b.category_id),
      );
      return { ...page, items: this.categoryItems };
    });
    this.hosted = new WorkItemRead(api);
    this.runner = new WorkItemRead(api);
    this.hosted.state.subscribe(() => this.updateNames());
  }

  openCategories(state: "active" | "retired" | "all" = "active", after?: string): Promise<void> {
    this.categoryEpoch++;
    return this.openCategoryPage(state, after);
  }

  private openCategoryPage(state: "active" | "retired" | "all", after?: string): Promise<void> {
    if (!after || state !== this.categoryState) this.categoryCursors = [];
    if (after && !this.categoryCursors.includes(after)) this.categoryCursors.push(after);
    this.categoryState = state;
    this.categoryAfter = after ?? "";
    const query = new URLSearchParams({ state });
    if (after) query.set("after", after);
    return this.categories.open(`/api/v1/categories?${query}`);
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
    if (this.categoryCursors.length && !this.refreshingCategories) {
      const pages = this.categoryCursors.length + 1,
        state = this.categoryState;
      const epoch = ++this.categoryEpoch;
      this.refreshingCategories = (async () => {
        await this.openCategoryPage(state);
        for (let page = 1; page < pages; page++) {
          if (this.categoryEpoch !== epoch || this.categories.state.get().error) break;
          const current = this.categories.state.get().value;
          if (!current?.more || !current.next) break;
          await this.openCategoryPage(state, current.next);
        }
      })().finally(() => {
        this.refreshingCategories = null;
      });
    } else if (!this.refreshingCategories) void this.categories.refresh();
    for (const read of [this.hosted, this.runner, ...this.runners.values()]) void read.refresh();
  }

  stop(): void {
    this.categoryEpoch++;
    this.categoryCursors = [];
    this.categoryItems = [];
    this.categoryAfter = "";
    for (const read of [this.categories, this.hosted, this.runner, ...this.runners.values()])
      read.close();
  }
}
