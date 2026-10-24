import type { TaskSummary, WorkItemPage } from "../src/api/generated/api.ts";

/** Keep resource layout exercises on the primary board, alongside real task links. */
export function workItemPage(project = "sample", tasks: TaskSummary[] = []): WorkItemPage {
  return {
    items: [
      {
        work_item_id: "wi_01M5M3ME31875GDA8F7P5XTD74",
        title: "Sample ongoing work item",
        project,
        intent: "Keep the next step visible after an execution finishes.",
        status: "open",
        column: "unplanned",
        revision: 1,
        created_at: 1,
        updated_at: 1,
        stages: [
          {
            stage_id: "investigate",
            title: "Investigation",
            status: "done",
            task_ids: tasks.map((task) => task.task_id),
          },
        ],
        tasks,
      },
    ],
  };
}
