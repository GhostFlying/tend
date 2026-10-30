import type {
  IssuePage,
  IssueView,
  TaskSummary,
  IssueWorkStatus,
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
  const statuses: IssueWorkStatus[] = [
    "backlog",
    "todo",
    "in_progress",
    "in_review",
    "done",
    "cancelled",
  ];
  const all = page.items
    .filter((item) => !item.parent_issue_id)
    .map((item) => ({
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
        execution: {},
        attention_count: 0,
      },
    }));
  return {
    ...page,
    items: all.filter((item) => item.board.status === url.searchParams.get("board_status")),
    counts: Object.fromEntries(
      statuses.map((status) => [status, all.filter((item) => item.board.status === status).length]),
    ),
    assignees: all.map((item) => item.assignee),
    facts_seq: 1,
  };
}
