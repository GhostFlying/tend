import { describe, expect, test } from "vitest";
import type { ExecutionState, TaskSummary, WorkItemView } from "../api/generated/api.ts";
import { currentState, progressCounts, selectTasks, uniqueTasks } from "./progress.ts";
const task = (task_id: string, state: ExecutionState): TaskSummary => ({
  task_id,
  title: task_id,
  project: "sample",
  workspace: "sample",
  rating: "p2",
  spec_version: 1,
  control_revision: 1,
  next_implement_generation: 1,
  lifecycle: state === "completed" || state === "cancelled" ? state : "running",
  control_mode: "managed",
  block_reason: "none",
  execution: { state },
});
describe("work item current execution", () => {
  test("shared task is counted once and startup, recovery and waiting are not running", () => {
    const running = task("one", "running");
    const tasks = uniqueTasks({
      tasks: [
        running,
        running,
        task("two", "starting"),
        task("three", "recovering"),
        task("four", "waiting_owner"),
      ],
    } as WorkItemView);
    expect(tasks).toHaveLength(4);
    expect(new Map(progressCounts(tasks))).toEqual(
      new Map([
        ["waiting_owner", 1],
        ["running", 1],
        ["recovering", 1],
        ["starting", 1],
      ]),
    );
  });
  test("ended includes cancelled without counting it as completed; missing facts do not infer execution", () => {
    const idle = task("idle", "running");
    delete idle.execution;
    expect(currentState(idle)).toBe("idle");
    const tasks = [idle, task("done", "completed"), task("cancelled", "cancelled")];
    expect(selectTasks(tasks, "current", null).map((t) => t.task_id)).toEqual(["idle"]);
    expect(selectTasks(tasks, "ended", null).map((t) => t.lifecycle)).toEqual([
      "completed",
      "cancelled",
    ]);
    expect(progressCounts(tasks)).toEqual([["idle", 1]]);
  });
  test("terminal business outcomes stay current until their worker settles", () => {
    const cancelledRunning = {
      ...task("cancelled-running", "cancelled"),
      execution: { state: "running" as const, run_id: "run-a", role: "implement" as const },
    };
    const completedUnknown = {
      ...task("completed-unknown", "completed"),
      execution: { state: "unknown" as const, run_id: "run-b", role: "accept" as const },
    };
    const settled = task("settled", "cancelled");
    const tasks = [cancelledRunning, completedUnknown, settled];
    expect(currentState(cancelledRunning)).toBe("running");
    expect(currentState(completedUnknown)).toBe("unknown");
    expect(selectTasks(tasks, "current", null).map((task) => task.task_id)).toEqual([
      "completed-unknown",
      "cancelled-running",
    ]);
    expect(selectTasks(tasks, "ended", null).map((task) => task.task_id)).toEqual(["settled"]);
    expect(new Map(progressCounts(tasks))).toEqual(
      new Map([
        ["unknown", 1],
        ["running", 1],
      ]),
    );
  });
  test("owner attention first, stable task identity within group, terminal completion newest first", () => {
    const tasks = [
      task("b", "running"),
      task("q", "queued"),
      task("a", "accepting"),
      task("w", "waiting_owner"),
      task("u", "unknown"),
    ];
    expect(selectTasks(tasks, "current", null).map((t) => t.task_id)).toEqual([
      "w",
      "u",
      "a",
      "b",
      "q",
    ]);
    expect(selectTasks(tasks, "current", "queued").map((t) => t.task_id)).toEqual(["q"]);
    expect(
      selectTasks(
        [task("old", "completed"), { ...task("new", "completed"), completed_at: 20 }],
        "ended",
        null,
      )[0]!.task_id,
    ).toBe("new");
    expect(selectTasks(tasks, "current", "paused")).toEqual([]);
  });
});
