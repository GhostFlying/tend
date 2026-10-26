import { describe, expect, test } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { ExecutionCounts, TaskSummary, WorkItemView } from "../api/generated/api.ts";
import { ExecutionSummary } from "./Execution.tsx";
import { executionCounts, executionLook, queueReasonText } from "./execution.ts";
import { taskLook } from "./status.ts";

const counts: ExecutionCounts = {
  running: 0,
  queued: 0,
  accepting: 0,
  waiting_owner: 0,
  paused: 0,
  unknown: 0,
  completed: 0,
  cancelled: 0,
  idle: 0,
};
describe("server execution facts", () => {
  test("idle describes the present and never claims a task has no execution history", () => {
    expect(executionLook({ state: "idle" })).toEqual({ label: "当前未执行", tone: "idle" });
    expect(executionCounts({ ...counts, idle: 1 })).toEqual([
      { state: "idle", text: "1 当前未执行" },
    ]);
  });
  test("owner decisions take precedence over broad implementation and acceptance lifecycles", () => {
    for (const [lifecycle, block_reason, reason] of [
      ["changes_required", "budget_exhausted", "等待调整实现次数"],
      ["awaiting_acceptance", "acceptance_env_unavailable", "等待验收环境恢复"],
      ["running", "workspace_unavailable", "等待工作区恢复"],
    ] as const) {
      const task = {
        lifecycle,
        block_reason,
        control_mode: "managed",
        execution: {
          state: "waiting_owner",
          queue_reason: { code: "task_blocked", message: block_reason },
        },
      } satisfies Pick<TaskSummary, "lifecycle" | "control_mode" | "block_reason" | "execution">;
      expect(taskLook(task)).toEqual({ label: "等待用户", tone: "warn" });
      expect(queueReasonText(task)).toBe(reason);
      const withoutQueueReason = { ...task, execution: { state: "waiting_owner" as const } };
      expect(queueReasonText(withoutQueueReason)).toBe(reason);
      expect(withoutQueueReason.execution).not.toHaveProperty("queue_reason");
    }
  });
  test("a lifecycle marked running can still be queued without a worker", () => {
    const task = {
      lifecycle: "running",
      control_mode: "managed",
      block_reason: "none",
      execution: {
        state: "queued",
        queue_reason: {
          code: "workspace_busy",
          message: "resource occupied",
          task_id: "task_owner",
        },
      },
    } satisfies Pick<TaskSummary, "lifecycle" | "control_mode" | "block_reason" | "execution">;
    expect(taskLook(task)).toEqual({ label: "排队中", tone: "warn" });
    expect(queueReasonText(task)).toBe("等待工作区释放");
    expect(executionLook({ state: "running", role: "implement", run_id: "run_1" }).label).toBe(
      "执行中",
    );
  });
  test("acceptance waiting for allocation is distinct from actual acceptance", () => {
    expect(executionLook({ state: "queued", role: "accept" }).label).toBe("排队中 · 待验收");
    expect(executionLook({ state: "accepting", role: "accept", run_id: "run_2" }).label).toBe(
      "验收中",
    );
    expect(executionLook({ state: "waiting_owner" }).label).toBe("等待用户");
    expect(executionLook({ state: "unknown" }).label).toBe("执行待核对");
  });
  test("an active plan without execution facts never fabricates running workers", () => {
    const plan = {
      stages: [{ stage_id: "design", title: "Design", status: "active" }],
    } satisfies Partial<WorkItemView>;
    expect(
      renderToStaticMarkup(
        createElement(ExecutionSummary, { counts: (plan as Partial<WorkItemView>).execution }),
      ),
    ).toBe("");
    expect(renderToStaticMarkup(createElement(ExecutionSummary, { counts }))).toContain(
      "尚无执行任务",
    );
  });
  test("mixed and overlapping stages retain authoritative counts rather than recounting task links", () => {
    expect(executionCounts({ ...counts, running: 1, queued: 2 })).toEqual([
      { state: "running", text: "1 执行中" },
      { state: "queued", text: "2 排队中" },
    ]);
    expect(
      executionCounts({ ...counts, accepting: 1, waiting_owner: 2, completed: 3 }).map(
        (f) => f.text,
      ),
    ).toEqual(["1 验收中", "2 等待用户", "3 已完成"]);
  });
  test("wait labels follow the server-selected constraint and preserve unknown facts", () => {
    const reasons = [
      "ready",
      "workspace_busy",
      "dependency",
      "host_capacity",
      "host_unavailable",
      "host_requirements",
      "task_blocked",
    ] as const;
    expect(
      reasons.map((code) =>
        queueReasonText({
          block_reason: "none",
          execution: { state: "queued", queue_reason: { code, message: "server fact" } },
        }),
      ),
    ).toEqual([
      "等待执行主机领取",
      "等待工作区释放",
      "等待前序任务",
      "等待主机空闲槽位",
      "等待执行主机恢复可用",
      "等待满足要求的执行主机",
      "等待任务条件满足",
    ]);
    expect(
      queueReasonText({
        block_reason: "waiting_decision",
        execution: {
          state: "waiting_owner",
          queue_reason: { code: "task_blocked", message: "owner decision" },
        },
      }),
    ).toBe("等待用户决定");
    expect(queueReasonText({ block_reason: "waiting_resource" })).toBe("");
  });
});

test("recovery is visible separately from running and preserves its diagnostic", () => {
  expect(executionLook({ state: "recovering", recovery: { phase: "pending" } }).label).toBe(
    "异常处理中",
  );
  expect(executionLook({ state: "recovering", recovery: { phase: "running" } }).label).toBe(
    "恢复中",
  );
  expect(
    queueReasonText({
      block_reason: "none",
      execution: {
        state: "recovering",
        recovery: { phase: "running", attempts: 1, code: "invalid_encrypted_content" },
      },
    }),
  ).toBe("已启动恢复会话 · 第 1 次恢复 · invalid_encrypted_content");
  expect(executionCounts({ ...counts, recovering: 1 })).toEqual([
    { state: "recovering", text: "1 恢复或异常处理中" },
  ]);
});

test("startup diagnostics distinguish readiness timeout and count startup separately", () => {
  expect(executionCounts({ ...counts, starting: 1 })).toEqual([
    { state: "starting", text: "1 正在启动" },
  ]);
  expect(
    queueReasonText({
      block_reason: "waiting_decision",
      execution: {
        state: "waiting_owner",
        startup: { phase: "failed", code: "readiness_timeout", attempts: 1 },
      },
    }),
  ).toBe("等待 CLI 就绪超时 · 已自动重试 1 次");
  expect(
    queueReasonText({
      block_reason: "none",
      execution: {
        state: "recovering",
        startup: { phase: "retry_pending", code: "readiness_timeout", attempts: 0 },
      },
    }),
  ).toContain("正在确认旧终端停止后重试");
  expect(
    queueReasonText({
      block_reason: "none",
      execution: {
        state: "running",
        startup: { phase: "started", code: "readiness_timeout", attempts: 0 },
      },
    }),
  ).toBe("");
});
