import { renderToStaticMarkup } from "react-dom/server";
import { expect, test, vi } from "vitest";
import type { IssueConversationItem } from "../api/generated/api.ts";
import { BackgroundNotice, backgroundHeading, backgroundState } from "./BackgroundNotice.tsx";

vi.mock(import("../app/services.tsx"), async (importOriginal) => ({
  ...(await importOriginal()),
  useServices: () => ({ client: { get: vi.fn() } }),
}));

function item(status = "started"): IssueConversationItem {
  return {
    message_id: "background-one",
    author: "system",
    kind: "message",
    notice_type: "background_batch",
    seq: 1,
    created_at: "2026-01-01T00:00:00Z",
    background: {
      batch_id: "batch-one",
      message_id: "background-one",
      status: "frozen",
      source_count: 4,
      created_at: 1,
      presentation: {
        recipient: {
          identity: { execution_environment_id: "runner-one", local_id: "cedar" },
          display_name: "青竹",
          status,
        },
        sources: [
          {
            seq: 1,
            message_id: "source-one",
            kind: "task.completed",
            summary: "发布验证任务已完成，报告原件保持可读取。",
            object_type: "task",
            object_id: "task-original",
          },
          {
            seq: 2,
            message_id: "source-two",
            kind: "issue.status_changed",
            summary: "Issue 工作状态调整为待检查。",
            object_type: "issue",
            object_id: "issue-original",
          },
        ],
        more: true,
      },
    },
  };
}

test("a background disclosure names its frozen recipient and the changes with object links", () => {
  const output = renderToStaticMarkup(<BackgroundNotice item={item()} presentation="issue" />);
  expect(output).toContain("青竹 · 4 项后台变化");
  expect(output).toContain("任务完成、Issue 工作状态变化");
  expect(output).toContain("已接收，正在处理");
  expect(output).toContain("发布验证任务已完成，报告原件保持可读取。");
  expect(output).toContain('href="/tasks/task-original"');
  expect(output).toContain('href="/issues/issue-original"');
  expect(output).toContain("以上为部分变化");
  expect(output).not.toContain("负责人收到后台变化");
});

test.each([
  ["starting", "已安排，等待接收确认"],
  ["rejected", "接收未成功"],
  ["unknown", "执行结果待核对"],
  ["interrupted", "处理已停止"],
  ["failed", "本次处理失败"],
  ["completed", "本轮执行已结束"],
])("native %s does not imply successful business processing", (status, expected) => {
  expect(backgroundState(item(status))).toBe(expected);
});

test("a native completion plus check-in identifies only that round of reconciliation", () => {
  const notice = item("completed");
  notice.background!.checkin = { note: "等待已有审阅", created_at: 2 };
  expect(backgroundState(notice)).toBe("已完成本轮核对");
});

test("old or unclaimed messages never infer an Agent or successful receipt", () => {
  const notice = item();
  delete notice.background!.presentation;
  notice.background!.status = "pending";
  expect(backgroundHeading(notice)).toBe("4 项后台变化");
  expect(backgroundState(notice)).toBe("待处理，尚无接收记录");
  delete notice.background;
  expect(backgroundHeading(notice)).toBe("后台变化");
  expect(backgroundState(notice)).toBe("接收者与处理进度未记录");
});
