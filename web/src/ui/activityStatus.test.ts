import { describe, expect, it } from "vitest";
import {
  activitySummary,
  conversationStatus,
  elapsedLabel,
  runActivityStatus,
  stageLabel,
  terminalLabel,
} from "./activityStatus.ts";
import type { IssueConversationItem, RunLogView } from "../api/generated/api.ts";

describe("live activity status", () => {
  it("derives stages from meaningful event types and tool labels", () => {
    for (const [type, name, label] of [
      ["reasoning", "", "思考中"],
      ["agentMessage", "", "正在回复"],
      ["commandExecution", "", "正在执行命令"],
      ["tool", "exec_command", "正在执行命令"],
      ["tool", "files_read", "正在读取文件"],
      ["tool", "grep", "正在搜索代码"],
      ["fileChange", "", "正在修改文件"],
      ["tool", "apply_patch", "正在修改文件"],
      ["webSearch", "", "正在搜索网页"],
      ["tool", "issues_read", "正在调用 查看 Issue"],
      ["unknown", "", "思考中"],
    ])
      expect(stageLabel(type!, name!)).toBe(label);
  });
  it("formats elapsed time without negative or invalid durations", () => {
    expect(elapsedLabel(83000)).toBe("1:23");
    expect(elapsedLabel(-1)).toBe("0:00");
    expect(elapsedLabel(NaN)).toBe("0:00");
    expect(elapsedLabel(3600000)).toBe("60:00");
  });
  it("replaces live state with terminal summaries", () => {
    const status = conversationStatus([], "failed", 1000, 84000);
    expect(activitySummary(status, 90000)).toBe("执行失败 · 0 个步骤 · 用时 1:23");
    expect(status.tone).toBe("error");
    expect(terminalLabel("cancelled")).toBe("已停止");
    expect(terminalLabel("unknown")).toBe("结果待确认");
  });
  it("uses latest conversation event and keeps the turn start", () => {
    const tool: IssueConversationItem = {
      message_id: "call",
      kind: "message",
      author: "agent",
      seq: 1,
      created_at: "2026-01-01T00:00:02Z",
      tool: {
        name: "files_read",
        turn_id: "turn",
        status: "running",
        updated_at: "2026-01-01T00:00:02Z",
      },
    };
    const reply = { ...tool, body: "reply", seq: 2 };
    delete reply.tool;
    expect(conversationStatus([tool], "processing", 1000).label).toBe("正在读取文件");
    expect(conversationStatus([tool, reply], "processing", 1000).label).toBe("正在回复");
    expect(conversationStatus([tool], "replied", 1000, 5000).active).toBe(false);
  });
  it("uses durable run starts and latest sequence, not array step order", () => {
    const log = (seq: number, type: string, kind: string): RunLogView => ({
      seq,
      kind,
      created_at: `2026-01-01T00:00:0${seq}Z`,
      body: { kind, thread_id: "thread", turn_id: "turn", item_id: type, data: { type } },
    });
    const rows = [
      log(1, "turn", "turn.started"),
      log(2, "commandExecution", "item.started"),
      log(3, "reasoning", "item.started"),
      log(4, "commandExecution", "item.completed"),
    ];
    const status = runActivityStatus(rows, "running");
    expect(status.label).toBe("正在执行命令");
    expect(status.startedAt).toBe(Date.parse(rows[0]!.created_at));
    expect(runActivityStatus(rows, "finished").label).toBe("已完成");
    expect(runActivityStatus([], "starting").startedAt).toBe(null);
    expect(runActivityStatus(rows.slice(1), "running").startedAt).toBe(null);
    expect(activitySummary(conversationStatus([], "replied"), NaN)).toContain("用时未知");
  });
});
