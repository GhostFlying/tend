// Canonical responsibility reads stay separate from the legacy work-item facade.
import type {
  IssueContextView,
  IssueDeliveryPage,
  IssueFollowupHistory,
  IssueFollowupPage,
  IssuePage,
  IssueState,
  IssueView,
  IssueNotificationSettings,
  IssueResultPage,
} from "../api/generated/api.ts";
import { IssueActivityRead } from "./activity.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { IssueBoardData } from "./boardData.ts";

export interface IssueFilter {
  project?: string;
  state?: IssueState;
  includeClosed?: boolean;
  parentID?: string;
  after?: string;
}

export function issuePagePath(filter: IssueFilter = {}): string {
  const query = new URLSearchParams();
  if (filter.project) query.set("project", filter.project);
  if (filter.state) query.set("state", filter.state);
  if (filter.includeClosed) query.set("include_closed", "true");
  // Empty selects roots; omission reads across parents. These are different APIs.
  if (filter.parentID !== undefined) query.set("parent_id", filter.parentID);
  if (filter.after) query.set("after", filter.after);
  return `/api/v1/issues${query.size ? `?${query}` : ""}`;
}

export class Issues {
  readonly notificationSettings: WorkItemRead<IssueNotificationSettings>;
  readonly board: IssueBoardData;
  readonly activity: IssueActivityRead;
  readonly results: WorkItemRead<IssueResultPage>;
  readonly list: WorkItemRead<IssuePage>;
  readonly detail: WorkItemRead<IssueView>;
  readonly history: WorkItemRead<IssueView>;
  readonly context: WorkItemRead<IssueContextView>;
  readonly followups: WorkItemRead<IssueFollowupPage>;
  readonly followupHistory: WorkItemRead<IssueFollowupHistory>;
  readonly deliveries: WorkItemRead<IssueDeliveryPage>;
  private readonly childReads = new Map<string, WorkItemRead<IssuePage>>();

  constructor(private readonly api: Reader) {
    this.notificationSettings = new WorkItemRead(api);
    this.board = new IssueBoardData(api);
    this.activity = new IssueActivityRead(api);
    this.results = new WorkItemRead(api);
    this.list = new WorkItemRead(api);
    this.detail = new WorkItemRead(api);
    this.history = new WorkItemRead(api);
    this.context = new WorkItemRead(api);
    this.followups = new WorkItemRead(api);
    this.followupHistory = new WorkItemRead(api);
    this.deliveries = new WorkItemRead(api);
  }

  openList(filter: IssueFilter = {}): Promise<void> {
    return this.list.open(issuePagePath(filter));
  }

  children(id: string, after?: string, includeClosed = false): WorkItemRead<IssuePage> {
    const key = `${id}:${includeClosed}`;
    let read = this.childReads.get(key);
    if (!read) {
      read = new WorkItemRead(this.api);
      this.childReads.set(key, read);
    }
    void read.open(issuePagePath({ parentID: id, includeClosed, ...(after ? { after } : {}) }));
    return read;
  }

  open(id: string): Promise<void> {
    return this.detail.open(`/api/v1/issues/${encodeURIComponent(id)}`);
  }

  openResults(id: string, after = ""): Promise<void> {
    return this.results.open(
      `/api/v1/issues/${encodeURIComponent(id)}/results${after ? `?after=${encodeURIComponent(after)}` : ""}`,
    );
  }

  openHistory(id: string, revision: number): Promise<void> {
    return this.history.open(`/api/v1/issues/${encodeURIComponent(id)}?revision=${revision}`);
  }

  openContext(id: string, after?: number): Promise<void> {
    const query = new URLSearchParams();
    if (after !== undefined) query.set("after", String(after));
    return this.context.open(
      `/api/v1/issues/${encodeURIComponent(id)}/context${query.size ? `?${query}` : ""}`,
    );
  }

  openFollowups(id: string, after?: string): Promise<void> {
    const query = new URLSearchParams();
    if (after) query.set("after", after);
    return this.followups.open(
      `/api/v1/issues/${encodeURIComponent(id)}/followups${query.size ? `?${query}` : ""}`,
    );
  }

  openFollowupHistory(
    id: string,
    ruleID: string,
    cursor: { afterOccurrence?: string; afterRevision?: number } = {},
  ): Promise<void> {
    const query = new URLSearchParams();
    if (cursor.afterOccurrence) query.set("after_occurrence", cursor.afterOccurrence);
    if (cursor.afterRevision !== undefined)
      query.set("after_revision", String(cursor.afterRevision));
    return this.followupHistory.open(
      `/api/v1/issues/${encodeURIComponent(id)}/followups/${encodeURIComponent(ruleID)}/history${query.size ? `?${query}` : ""}`,
    );
  }

  openDeliveries(id: string): Promise<void> {
    return this.deliveries.open(`/api/v1/issues/${encodeURIComponent(id)}/deliveries`);
  }

  private reads() {
    return [
      this.notificationSettings,
      this.activity,
      this.results,
      this.list,
      this.detail,
      this.history,
      this.context,
      this.followups,
      this.followupHistory,
      this.deliveries,
      ...this.childReads.values(),
    ];
  }

  refresh(): void {
    this.board.refresh();
    for (const read of this.reads()) {
      void read.refresh();
    }
  }

  stop(): void {
    this.board.close();
    for (const read of this.reads()) read.close();
    // Reopening must preserve handles retained by subscribed React surfaces.
  }
}
