import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, test } from "vitest";
import type { WorkItemView } from "../api/generated/api.ts";
import { ServicesContext, type Services } from "../app/services.tsx";
import { WorkItemProgress } from "./Progress.tsx";

describe("work item progress component", () => {
  test("shows a cancelled task's unresolved worker in the default current tab", () => {
    const item = {
      work_item_id: "item-a",
      revision: 1,
      title: "Retain execution facts",
      project: "sample",
      intent: "Show physical execution separately from the business outcome",
      status: "open",
      column: "active",
      stages: [
        {
          stage_id: "implementation",
          title: "Implementation",
          status: "active",
          task_ids: ["task-a"],
        },
      ],
      tasks: [
        {
          task_id: "task-a",
          title: "Cancelled while worker status is unknown",
          project: "sample",
          workspace: "sample",
          rating: "p2",
          spec_version: 1,
          control_revision: 2,
          next_implement_generation: 2,
          lifecycle: "cancelled",
          control_mode: "managed",
          block_reason: "none",
          execution: { state: "unknown", run_id: "run-a", role: "implement" },
        },
      ],
      created_at: 1,
      updated_at: 2,
    } satisfies WorkItemView;
    const services = { openTask() {} } as unknown as Services;
    const html = renderToStaticMarkup(
      createElement(
        ServicesContext.Provider,
        { value: services },
        createElement(WorkItemProgress, { item }),
      ),
    );
    expect(html).toContain("1 执行待核对");
    expect(html).toContain("当前（1）");
    expect(html).toContain("已结束（0）");
    expect(html).toContain("Cancelled while worker status is unknown");
  });
});
