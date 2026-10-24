import { expect, test } from "vitest";
import type { TaskSummary } from "../api/generated/api.ts";
import { ComposerDraft } from "./draft.ts";

const task = { task_id: "task_example", title: "原任务" } as TaskSummary;
test("follow-up intentions preserve drafts and do not duplicate after owner edits", () => {
  const draft = new ComposerDraft();
  draft.write("已有草稿");
  draft.followUp(task);
  expect(draft.state.get().text.split("\n")).toEqual([
    "已有草稿",
    "",
    "跟进 / 修改任务 task_example（原任务）。",
    "",
    "需要补充或修改的内容：",
    "",
  ]);
  expect(draft.state.get().text).toMatch(/^已有草稿\n\n跟进 \/ 修改任务 task_example/);
  draft.write(draft.state.get().text + "修改按钮位置");
  const edited = draft.state.get().text;
  draft.followUp({ ...task, title: "更新后的标题" });
  expect(draft.state.get()).toEqual({ text: edited, files: [], focus: 2 });
  draft.followUp({ ...task, task_id: "task_another" });
  expect(draft.state.get().text).toContain("task_another");
  expect(draft.state.get().text).toContain("修改按钮位置");
});

test("files survive follow-up navigation and failed durable enqueue", () => {
  const draft = new ComposerDraft();
  const image = new File(["screenshot"], "screenshot.png", { type: "image/png" });
  draft.addFiles([image]);
  draft.followUp(task);
  expect(draft.state.get().files).toEqual([image]);
  const sent = draft.state.get();
  draft.clear();
  draft.write("下一条消息");
  draft.restore(sent.text, sent.files);
  expect(draft.state.get().files).toEqual([image]);
  expect(draft.state.get().text).toContain("下一条消息");
  draft.removeFile(0);
  expect(draft.state.get().files).toEqual([]);
});
test("failed enqueue restoration preserves text written while storage was pending", () => {
  const draft = new ComposerDraft();
  draft.write("发送中的内容");
  const sent = draft.state.get().text;
  draft.write("");
  draft.followUp(task);
  draft.restore(sent);
  expect(draft.state.get().text).toMatch(/^发送中的内容\n\n跟进 \/ 修改任务 task_example/);
});
