import { expect, test } from "vitest";
import type { IssueSummary } from "../api/generated/api.ts";
import { responsibilityLabel } from "./boardModel.ts";

const issue = (patch: Partial<IssueSummary> = {}): IssueSummary => ({
  issue_id: "issue",
  title: "Complete delivery",
  project: "sample",
  state: "active",
  work_status: "in_progress",
  revision: 5,
  assignment_epoch: 1,
  assignment_state: "active",
  assignee: { kind: "agent", ref: "agent" },
  board: {
    status: "in_progress",
    stages_total: 0,
    stages_done: 0,
    attention_count: 0,
    execution: {
      accepting: 0,
      cancelled: 1,
      completed: 5,
      idle: 0,
      paused: 0,
      queued: 0,
      running: 0,
      unknown: 0,
      waiting_owner: 0,
    },
  },
  ...patch,
});

test("finished executions leave ongoing responsibility and the owner next step visible", () => {
  const item = issue();
  expect(responsibilityLabel(item)).toBe("待负责人确认下一步");
  item.board!.progress = {
    summary: "Deployment accepted",
    next_step: "Settle the original delivery",
    issue_revision: 4,
    updated_at: 1,
    updated_by: item.assignee,
  };
  expect(responsibilityLabel(item)).toBe("负责人继续推进");
  item.board!.attention_count = 1;
  expect(responsibilityLabel(item)).toBe("等待负责人处理");
  item.board!.owner_attention_count = 1;
  expect(responsibilityLabel(item)).toBe("等待你处理");
  item.state = "closed";
  expect(responsibilityLabel(item)).toBe("Issue 已完成");
});

test("actual unresolved and active execution facts remain visible alongside historical results", () => {
  const item = issue();
  item.board!.execution.unknown = 1;
  expect(responsibilityLabel(item)).toBe("执行结果待核对");
  item.board!.execution.unknown = 0;
  item.board!.execution.running = 1;
  expect(responsibilityLabel(item)).toBe("1 项运行中");
  item.board!.execution.running = 0;
  item.state = "paused";
  expect(responsibilityLabel(item)).toBe("Issue 已暂停");
  item.state = "cancelled";
  expect(responsibilityLabel(item)).toBe("Issue 已取消");
});
