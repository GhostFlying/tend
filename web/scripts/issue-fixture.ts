import type { IssuePage, IssueView, TaskSummary } from "../src/api/generated/api.ts";

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
