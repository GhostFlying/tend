import type { IssueAssignee, IssuePage, IssueWorkStatus } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { boardColumns } from "./boardModel.ts";

export interface BoardFilter {
  project?: string;
  assignee?: IssueAssignee;
  includeClosed?: boolean;
}

export function boardPagePath(filter: BoardFilter, status: IssueWorkStatus): string {
  const query = new URLSearchParams({
    board: "true",
    parent_id: "",
    board_status: status,
    limit: "25",
  });
  if (filter.project) query.set("project", filter.project);
  if (filter.assignee) {
    query.set("assignee_kind", filter.assignee.kind);
    query.set("assignee_ref", filter.assignee.ref);
  }
  if (filter.includeClosed) query.set("include_closed", "true");
  return `/api/v1/issues?${query}`;
}

/** Reuse the existing cancellable reader, including its SSE coalescing/backoff.
 * Refresh rereads the loaded prefix so moved cards cannot survive in old pages. */
class BoardColumnRead {
  readonly read: WorkItemRead<IssuePage>;
  private pageCount = 1;
  private path = "";

  constructor(api: Reader, received: (page: IssuePage) => void) {
    this.read = new WorkItemRead<IssuePage>(
      {
        get: async <T>(path: string, signal?: AbortSignal): Promise<T> => {
          let result: IssuePage | undefined;
          let cursor = "";
          const pages = this.pageCount;
          for (let index = 0; index < pages; index++) {
            const url = new URL(path, "https://tend.invalid");
            if (cursor) url.searchParams.set("after", cursor);
            const page = await api.get<IssuePage>(`${url.pathname}${url.search}`, signal);
            if (signal?.aborted) return page as T;
            result = result
              ? { ...page, items: [...(result.items ?? []), ...(page.items ?? [])] }
              : page;
            if (!page.more || !page.next) break;
            cursor = page.next;
          }
          if (!result) throw new Error("未取得看板记录");
          const items = new Map((result.items ?? []).map((item) => [item.issue_id, item]));
          return { ...result, items: [...items.values()] } as T;
        },
      },
      (page) => {
        const current = this.read.state.get().value;
        if (current && (page.facts_seq ?? 0) < (current.facts_seq ?? 0)) return current;
        received(page);
        return page;
      },
    );
  }

  open(path: string): Promise<void> {
    if (path !== this.path) this.pageCount = 1;
    this.path = path;
    return this.read.open(path);
  }

  more(): Promise<void> {
    if (!this.read.state.get().value?.more || this.read.state.get().loading)
      return Promise.resolve();
    this.pageCount++;
    return this.read.refresh();
  }
}

export class IssueBoardData {
  readonly summary = new Store<Pick<IssuePage, "counts" | "assignees" | "facts_seq">>({});
  readonly columns: ReadonlyMap<IssueWorkStatus, BoardColumnRead>;
  private key = "";

  constructor(api: Reader) {
    this.columns = new Map(
      boardColumns.map(({ id }) => [
        id,
        new BoardColumnRead(api, (page) => {
          if ((page.facts_seq ?? 0) < (this.summary.get().facts_seq ?? 0)) return;
          this.summary.set({
            ...(page.counts ? { counts: page.counts } : {}),
            ...(page.assignees ? { assignees: page.assignees } : {}),
            facts_seq: page.facts_seq ?? 0,
          });
        }),
      ]),
    );
  }

  open(filter: BoardFilter): void {
    const key = JSON.stringify(filter);
    if (key !== this.key) this.summary.set({});
    this.key = key;
    for (const [id, column] of this.columns) void column.open(boardPagePath(filter, id));
  }

  refresh(): void {
    for (const column of this.columns.values()) void column.read.refresh();
  }

  close(): void {
    for (const column of this.columns.values()) column.read.close();
    this.key = "";
    this.summary.set({});
  }
}
