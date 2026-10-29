// Public registration and executor observations remain separate server facts.
import type {
  AgentCategoryPage,
  HostedAgentInventory,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

export class AgentCatalog {
  readonly categories: WorkItemRead<AgentCategoryPage>;
  readonly hosted: WorkItemRead<HostedAgentInventory>;
  readonly runner: WorkItemRead<RunnerAgentCatalog>;

  constructor(api: Reader) {
    this.categories = new WorkItemRead(api);
    this.hosted = new WorkItemRead(api);
    this.runner = new WorkItemRead(api);
  }

  openCategories(state: "active" | "retired" | "all" = "active", after?: string): Promise<void> {
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

  refresh(): void {
    for (const read of [this.categories, this.hosted, this.runner]) void read.refresh();
  }

  stop(): void {
    for (const read of [this.categories, this.hosted, this.runner]) read.close();
  }
}
