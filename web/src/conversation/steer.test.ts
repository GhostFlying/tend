import { describe, expect, it } from "vitest";
import { steerNotice } from "./steer.ts";
import { messageStateLabels } from "./model.ts";

describe("owner steer evidence labels", () => {
  it("distinguishes native acceptance, consumption, and completion without a reply", () => {
    expect(messageStateLabels.accepted).toContain("等待安全边界");
    expect(messageStateLabels.processing).toBe("正在处理");
    expect(messageStateLabels.processed).toContain("未生成回复");
    expect(steerNotice({ status: "accepted", thread_id: "thread", turn_id: "turn" })).toContain(
      "工具可能需要先完成",
    );
  });
  it("keeps unsupported and uncertain input visible with different explanations", () => {
    expect(
      steerNotice({
        status: "waiting",
        reason: "native executor does not support steer",
        thread_id: "thread",
        turn_id: "turn",
      }),
    ).toContain("不支持实时补充");
    expect(steerNotice({ status: "unknown", thread_id: "thread", turn_id: "turn" })).toContain(
      "不会自动重发",
    );
  });
  it("distinguishes preparation failure from native rejection and unspecified waiting", () => {
    const notice = (reason?: string) =>
      steerNotice({
        status: "waiting",
        ...(reason === undefined ? {} : { reason }),
        thread_id: "thread",
        turn_id: "turn",
      });
    expect(notice("attachment or message preparation failed; waiting for a later turn")).toContain(
      "尚未送入原生会话",
    );
    expect(
      notice("attachment or message preparation failed; waiting for a later turn"),
    ).not.toContain("拒绝");
    expect(
      notice("native rejected current-turn steer (unsupported or turn already ended)"),
    ).toContain("原生会话拒绝");
    expect(notice()).toContain("等待后续处理");
    expect(notice("unrecognized future waiting reason")).not.toContain("拒绝");
  });
});
