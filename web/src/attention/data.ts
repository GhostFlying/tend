import type {
  AttentionPage,
  AttentionView,
  IssueDeadline,
  NextActionPage,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

export interface AttentionFilter {
  issueID?: string;
  kind?: string;
  status?: string;
  after?: string;
  includeArchived?: boolean;
  actionableOnly?: boolean;
}

export class AttentionData {
  readonly inbox: WorkItemRead<AttentionPage>;
  readonly next: WorkItemRead<NextActionPage>;
  readonly deadlines: WorkItemRead<IssueDeadline[]>;
  private readonly details = new Map<string, WorkItemRead<AttentionView>>();

  constructor(private readonly api: Reader) {
    this.inbox = new WorkItemRead(api);
    this.next = new WorkItemRead(api);
    this.deadlines = new WorkItemRead(api);
  }

  openInbox(filter: AttentionFilter = {}): Promise<void> {
    const query = new URLSearchParams();
    if (filter.issueID) query.set("issue_id", filter.issueID);
    if (filter.kind) query.set("kind", filter.kind);
    if (filter.status) query.set("status", filter.status);
    if (filter.after) query.set("after", filter.after);
    if (filter.includeArchived) query.set("include_archived", "true");
    if (filter.actionableOnly) query.set("actionable_only", "true");
    return this.inbox.open(`/api/v1/inbox${query.size ? `?${query}` : ""}`);
  }

  // Chat, Inbox and detail use one read of the original Attention identity.
  attention(id: string): WorkItemRead<AttentionView> {
    let read = this.details.get(id);
    if (!read) {
      read = new WorkItemRead(this.api);
      this.details.set(id, read);
      void read.open(`/api/v1/attentions/${encodeURIComponent(id)}`);
    }
    return read;
  }

  openNext(kind?: string, issueID?: string): Promise<void> {
    const query = new URLSearchParams();
    if (kind) query.set("kind", kind);
    if (issueID) query.set("issue_id", issueID);
    return this.next.open(`/api/v1/next-actions${query.size ? `?${query}` : ""}`);
  }

  openDeadlines(issueID?: string): Promise<void> {
    return this.deadlines.open(
      `/api/v1/deadlines${issueID ? `?${new URLSearchParams({ issue_id: issueID })}` : ""}`,
    );
  }

  refresh(): void {
    for (const read of [this.inbox, this.next, this.deadlines, ...this.details.values()]) {
      void read.refresh();
    }
  }

  stop(): void {
    for (const read of [this.inbox, this.next, this.deadlines, ...this.details.values()])
      read.close();
    this.details.clear();
  }
}
