import { expect, test } from "vitest";
import { noticeTitle } from "./status.ts";

test("legacy completion and new release notifications keep separate meanings", () => {
  expect(noticeTitle()).toBe("任务完成");
  expect(noticeTitle("task_completed")).toBe("任务完成");
  expect(noticeTitle("upgrade_started")).toBe("发布开始");
  expect(noticeTitle("upgrade_host_restored")).toBe("主机已恢复");
  expect(noticeTitle("upgrade_attention")).toBe("发布需要处理");
  expect(noticeTitle("future_notice")).toBe("系统通知");
});
