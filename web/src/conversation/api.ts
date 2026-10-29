import type {
  IssueAgentConversation as AgentConversation,
  IssueAgentConversationPage as AgentConversationPage,
  ManualSource,
  NativeEntry,
  RunMessagePage,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

/** The existing LiveData/queue/settings readers can address any logical chat. */
export function conversationReader(api: Reader, conversationID: string): Reader {
  return {
    get<T>(path: string, signal?: AbortSignal): Promise<T> {
      if (
        path === "/api/v1/conversation" ||
        path.startsWith("/api/v1/conversation?") ||
        path.startsWith("/api/v1/conversation/")
      ) {
        const url = new URL(path, "https://tend.invalid");
        url.searchParams.set("conversation_id", conversationID);
        path = url.pathname + url.search;
      }
      return api.get<T>(path, signal);
    },
  };
}

export class DirectConversations {
  readonly list: WorkItemRead<AgentConversationPage>;
  readonly detail: WorkItemRead<AgentConversation>;
  readonly source: WorkItemRead<ManualSource>;
  readonly runMessages: WorkItemRead<RunMessagePage>;
  readonly nativeEntry: WorkItemRead<NativeEntry>;

  constructor(api: Reader) {
    this.list = new WorkItemRead(api);
    this.detail = new WorkItemRead(api);
    this.source = new WorkItemRead(api);
    this.runMessages = new WorkItemRead(api);
    this.nativeEntry = new WorkItemRead(api);
  }

  openList(search = "", status = "", after = ""): Promise<void> {
    const query = new URLSearchParams({ search, status, projection: "issues" });
    if (after) query.set("after", after);
    return this.list.open(`/api/v1/agent-conversations?${query}`);
  }

  open(id: string): Promise<void> {
    return this.detail.open(
      `/api/v1/agent-conversations/${encodeURIComponent(id)}?projection=issues`,
    );
  }

  openSource(id: string): Promise<void> {
    return this.source.open(`/api/v1/sources/${encodeURIComponent(id)}`);
  }

  openRunMessages(runID: string, after?: number): Promise<void> {
    return this.runMessages.open(
      `/api/v1/runs/${encodeURIComponent(runID)}/messages${after === undefined ? "" : `?after=${after}`}`,
    );
  }

  openNativeEntry(runID: string, provider?: string): Promise<void> {
    return this.nativeEntry.open(
      `/api/v1/runs/${encodeURIComponent(runID)}/native-entry${provider ? `?${new URLSearchParams({ provider })}` : ""}`,
    );
  }

  refresh(): void {
    for (const read of [this.list, this.detail, this.source, this.runMessages, this.nativeEntry]) {
      void read.refresh();
    }
  }

  stop(): void {
    for (const read of [this.list, this.detail, this.source, this.runMessages, this.nativeEntry])
      read.close();
  }
}
