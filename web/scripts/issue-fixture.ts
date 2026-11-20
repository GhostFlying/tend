import type {
  IssuePage,
  IssueView,
  TaskSummary,
  IssueBoardColumn,
  IssueBoardCard,
  IssueSummary,
} from "../src/api/generated/api.ts";

export const issueID = "wi_01M5M3ME31875GDA8F7P5XTD74";
export function issueView(project = "sample", tasks: TaskSummary[] = []): IssueView {
  return {
    issue_id: issueID,
    plan: {
      title: "Sample ongoing Issue",
      project,
      intent: "Keep the next step visible after an execution finishes.",
      stages: [],
    },
    state: "active",
    revision: 1,
    assignment_epoch: 1,
    assignment_state: "active",
    assignee: { kind: "owner", ref: "Example owner" },
    depends_on: [],
    created_at: 1,
    updated_at: 1,
    tasks,
  };
}
export function issuePage(project = "sample", tasks: TaskSummary[] = []): IssuePage {
  const issue = issueView(project, tasks);
  return {
    items: [
      {
        issue_id: issue.issue_id,
        title: issue.plan.title,
        project,
        state: issue.state,
        revision: issue.revision,
        assignee: issue.assignee,
        assignment_epoch: issue.assignment_epoch,
        assignment_state: issue.assignment_state,
      },
    ],
    more: false,
  };
}

/** Adapt existing hierarchy fixtures to the separately filtered board contract. */
export function issueBoardFixture(request: string | undefined, value: unknown): unknown {
  const url = new URL(request ?? "/", "https://fixture.invalid");
  if (url.pathname !== "/api/v1/issues" || url.searchParams.get("board") !== "true") return value;
  const page = value as IssuePage;
  if (!Array.isArray(page.items)) return value;
  const columns: IssueBoardColumn[] = ["backlog", "wip", "waiting", "done", "cancelled"];
  const all = page.items
    .filter((item) => !item.parent_issue_id)
    .map((item): IssueSummary & { board: IssueBoardCard } => ({
      ...item,
      board: item.board ?? {
        status:
          item.state === "closed"
            ? "done"
            : item.state === "cancelled"
              ? "cancelled"
              : item.work_status || "in_progress",
        stages_total: 0,
        stages_done: 0,
        execution: {
          accepting: 0,
          cancelled: 0,
          completed: 0,
          idle: 0,
          paused: 0,
          queued: 0,
          running: 0,
          unknown: 0,
          waiting_owner: 0,
        },
        attention_count: 0,
      },
    }));
  const displayColumn = (item: (typeof all)[number]): IssueBoardColumn => {
    if (item.board.column) return item.board.column;
    switch (item.board.status) {
      case "backlog":
      case "done":
      case "cancelled":
        return item.board.status;
      case "todo":
        return "waiting";
      default:
        return "wip";
    }
  };
  const selected = url.searchParams.get("board_column");
  return {
    ...page,
    items: all
      .filter((item) => selected === "all" || displayColumn(item) === selected)
      .map((item) => ({ ...item, board: { ...item.board, column: displayColumn(item) } })),
    column_counts: Object.fromEntries(
      columns.map((column) => [
        column,
        all.filter((item) => displayColumn(item) === column).length,
      ]),
    ),
    assignees: all.map((item) => item.assignee),
    facts_seq: 1,
  };
}
