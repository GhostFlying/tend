import { describe, expect, test } from "vitest";
import type { WorkItemView } from "../api/generated/api.ts";
import { ComposerDraft } from "../conversation/draft.ts";
import {
  columns,
  concern,
  evidenceLabel,
  observationErrorLabel,
  parallelStages,
  refreshBaseline,
  refreshReconciled,
  splitWorkItemIds,
  stageRows,
} from "./model.ts";

const item: WorkItemView = {
  work_item_id: "wi_01M5M3ME31875GDA8F7P5XTD74",
  title: "调查后的持续跟进",
  project: "sample",
  intent: "找到原因，再根据事实决定后续",
  revision: 4,
  status: "open",
  column: "unplanned",
  created_at: 1,
  updated_at: 2,
  stages: [{ stage_id: "investigate", title: "调查", status: "done" }],
};
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
    const delivery = {
      delivery_id: "pr",
      provider: "github",
      repository: "example/repo",
      number: 1,
    };
    const observed = {
      work_item_id: item.work_item_id,
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
  test("finishing this round remains visible as an unresolved next step", () => {
    expect(concern(item)).toBe("本轮已结束，后续尚未安排");
    expect(columns.map((column) => column.title)).toEqual([
      "待安排",
      "进行中",
      "待检查",
      "待处理",
      "暂缓",
      "已结束",
    ]);
    expect(
      concern({
        ...item,
        follow_ons: [
          {
            follow_on_id: "next",
            kind: "problem",
            text: "需要 owner 决定范围",
            source: "owner",
            status: "open",
          },
        ],
      }),
    ).toBe("需要 owner 决定范围");
  });
  test("grouping preserves dynamic stage identities and dependency order", () => {
    const stages = [
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
    const dependency = { stage_id: "research", title: "调查", status: "planned" };
    const group = { stage_id: "group", title: "实现", status: "planned", depends_on: ["research"] };
    const children = ["api", "web"].map((id) => ({
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
  test("references recognize only complete work-item IDs at text boundaries", () => {
    const id = item.work_item_id;
    expect(splitWorkItemIds(`继续 ${id}。`)).toEqual(["继续 ", { workItemId: id }, "。"]);
    for (const text of [
      `prefix_${id}`,
      `${id}extra`,
      "wi_81M5M3ME31875GDA8F7P5XTD74",
      "wi_01M5M3ME",
    ])
      expect(splitWorkItemIds(text)).toEqual([text]);
  });
  test("navigation adds a proposal without replacing an unsent message or files", () => {
    const draft = new ComposerDraft();
    draft.write("已有草稿");
    draft.workItem(item, "snooze");
    expect(draft.state.get().text).toContain("已有草稿\n\n");
    expect(draft.state.get().text).toContain(item.work_item_id);
    expect(draft.state.get().text).toContain("运行中的任务继续按原约定推进，不停止 run");
    expect(draft.state.get().focus).toBe(1);
    draft.workItem(item, "close");
    expect(draft.state.get().text).toContain("未处理承诺、运行任务和未确认操作");
    expect(draft.state.get().focus).toBe(2);
  });
});
