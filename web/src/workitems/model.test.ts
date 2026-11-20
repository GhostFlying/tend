import { describe, expect, test } from "vitest";
import type { IssueDelivery, IssueStage } from "../api/generated/api.ts";
import {
  columns,
  evidenceLabel,
  observationErrorLabel,
  parallelStages,
  refreshBaseline,
  refreshReconciled,
  splitWorkItemIds,
  stageRows,
  stageProgressLabel,
} from "./model.ts";

const workItemID = "wi_01M5M3ME31875GDA8F7P5XTD74";
describe("work-item presentation", () => {
  test("evidence and observation failures use readable labels with diagnostic fallback", () => {
    expect(["acceptance", "delivery", "owner_decision"].map(evidenceLabel)).toEqual([
      "独立验收",
      "交付核验",
      "用户决定",
    ]);
    expect(evidenceLabel("future_basis")).toBe("其他依据（future_basis）");
    expect(observationErrorLabel("authentication_required")).toBe("需要登录或访问权限");
    expect(observationErrorLabel("cli_unavailable")).toBe("执行主机缺少查询工具");
    expect(observationErrorLabel("future_error")).toBe("未知错误（future_error）");
  });
  test("unknown refresh requires completed newer facts for its own delivery", () => {
    const delivery: IssueDelivery = {
      delivery_id: "pr",
      provider: "github",
      repository: "example/repo",
      number: 1,
    };
    const observed = {
      work_item_id: workItemID,
      delivery_id: "pr",
      generation: 1,
      state: "open",
      checked_at: 1000,
    };
    const baseline = refreshBaseline(delivery, observed);
    expect(refreshReconciled(baseline, delivery, { ...observed })).toBe(false);
    expect(refreshReconciled(baseline, delivery, { ...observed, generation: 2 })).toBe(false);
    expect(
      refreshReconciled(baseline, delivery, {
        ...observed,
        generation: 2,
        checked_at: 2000,
        refresh_pending: true,
      }),
    ).toBe(false);
    expect(
      refreshReconciled(baseline, delivery, { ...observed, generation: 2, checked_at: 2000 }),
    ).toBe(true);
    expect(
      refreshReconciled(baseline, delivery, {
        ...observed,
        generation: 2,
        attempted_at: 2000,
        error_kind: "query_failed",
      }),
    ).toBe(true);
    expect(refreshReconciled(baseline, { ...delivery, number: 2 }, observed)).toBe(true);
  });
  test("plan column labels remain readable", () => {
    expect(columns.map((column) => column.title)).toEqual([
      "待安排",
      "推进中",
      "待检查",
      "待处理",
      "暂缓",
      "已结束",
    ]);
  });
  test("grouping preserves dynamic stage identities and dependency order", () => {
    const stages: IssueStage[] = [
      {
        stage_id: "web",
        parent_id: "build",
        title: "界面",
        status: "planned",
        depends_on: ["investigate"],
      },
      { stage_id: "build", title: "实现", status: "active" },
      {
        stage_id: "api",
        parent_id: "build",
        title: "服务接口",
        status: "active",
        depends_on: ["investigate"],
      },
      { stage_id: "investigate", title: "调查", status: "done" },
      { stage_id: "check", title: "独立验收", status: "planned", depends_on: ["build"] },
    ];
    expect(stageRows(stages).map(({ stage, depth }) => [stage.stage_id, depth])).toEqual([
      ["build", 0],
      ["web", 1],
      ["api", 1],
      ["investigate", 0],
      ["check", 0],
    ]);
    expect([...parallelStages(stages)]).toEqual(["web", "api"]);
  });
  test("parallel children inherit ancestor dependencies and retired-group blocking", () => {
    const dependency: IssueStage = { stage_id: "research", title: "调查", status: "planned" };
    const group: IssueStage = {
      stage_id: "group",
      title: "实现",
      status: "planned",
      depends_on: ["research"],
    };
    const children = ["api", "web"].map((id): IssueStage => ({
      stage_id: id,
      title: id,
      status: "planned",
      parent_id: "group",
    }));
    expect([...parallelStages([dependency, group, ...children])]).toEqual([]);
    expect([...parallelStages([{ ...dependency, status: "done" }, group, ...children])]).toEqual([
      "api",
      "web",
    ]);
    expect([
      ...parallelStages([
        { ...dependency, status: "done" },
        { ...group, status: "retired" },
        ...children,
      ]),
    ]).toEqual([]);
    expect([
      ...parallelStages([
        { ...dependency, status: "done" },
        { ...group, status: "cancelled" },
        ...children,
      ]),
    ]).toEqual([]);
  });
  test.each(["short", "all"] as const)(
    "short references keep filenames and paths intact (%s)",
    (mode) => {
      const files =
        "T123.txt T123/src/main.go src/T123 T123\\code.ts t123.文档 " +
        "logs/case-T123-results.txt case-t123-results.txt logs/case-T123-results logs/staff@team-T123 C:\\logs\\T123-report.txt";
      expect(splitWorkItemIds(files, mode)).toEqual([files]);
      expect(splitWorkItemIds("按-T123-跟进", mode)).toEqual([
        "按-",
        { workItemId: "T123" },
        "-跟进",
      ]);
      expect(splitWorkItemIds("Check T123. Then (t456), please.", mode)).toEqual([
        "Check ",
        { workItemId: "T123" },
        ". Then (",
        { workItemId: "t456" },
        "), please.",
      ]);
    },
  );
  test.each(["short", "all"] as const)("short references leave URL text intact (%s)", (mode) => {
    const links =
      "https://example.test/?issue=T123 https://example.test/#t234 " +
      "mailto:ops@example.org?subject=T345 ssh://git@example.org/repo?label=T567 " +
      "(ssh://[::1]/repo(T789)?labels[]=T890)";
    expect(splitWorkItemIds(`${links}，继续 T456。`, mode)).toEqual([
      `${links}，继续 `,
      { workItemId: "T456" },
      "。",
    ]);
  });
  test("references recognize only complete work-item IDs at text boundaries", () => {
    const id = workItemID;
    expect(splitWorkItemIds(`继续 ${id}。`)).toEqual(["继续 ", { workItemId: id }, "。"]);
    for (const text of [
      `prefix_${id}`,
      `${id}extra`,
      "wi_81M5M3ME31875GDA8F7P5XTD74",
      "wi_01M5M3ME",
    ])
      expect(splitWorkItemIds(text)).toEqual([text]);
  });
});

test("completed execution remains distinct from stage and responsibility completion", () => {
  const counts = {
    queued: 0,
    running: 0,
    accepting: 0,
    waiting_owner: 0,
    paused: 0,
    unknown: 0,
    idle: 0,
    completed: 1,
    cancelled: 0,
  };
  const stage: IssueStage = { stage_id: "work", title: "Work", status: "active" };
  expect(stageProgressLabel(stage, counts, true)).toBe("本轮执行已完成 · 阶段待核对");
  expect(stageProgressLabel(stage, counts, false)).toBe("本轮执行已完成 · 后续责任未结束");
  expect(stageProgressLabel(stage, { ...counts, running: 1 }, true)).toBe("推进中");
  expect(stageProgressLabel(stage, { ...counts, completed: 0, cancelled: 1 }, true)).toBe(
    "本轮已取消 · 后续待安排",
  );
  expect(stageProgressLabel({ ...stage, status: "done" }, counts, false)).toBe("已完成");
});
