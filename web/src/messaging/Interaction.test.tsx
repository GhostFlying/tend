import { expect, test } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { MessagingMessage } from "../api/generated/api.ts";
import { Interaction, interactionStatus } from "./Interaction.tsx";
const message = {
  message_id: "msg_question",
  interaction: {
    interaction_id: "int_question",
    object_type: "decision",
    object_id: "decision",
    version: 1,
    question: "下一步怎么办？",
    options: [{ option_id: "retry", label: "重新实现" }],
    text_allowed: true,
    state: "answered",
    handled_channel: "telegram",
    handled_at: 1000,
    answer_summary: "重新实现",
  },
} as MessagingMessage;
test("a question handled in another channel shows its source and has no active answer controls", () => {
  const html = renderToStaticMarkup(
    <Interaction
      message={message}
      busy={false}
      onAnswer={() => {
        throw new Error("unexpected action");
      }}
      onReply={() => {
        throw new Error("unexpected reply");
      }}
    />,
  );
  expect(html).toContain("已在 Telegram 处理");
  expect(html).toContain("你的回答：重新实现");
  expect(html).not.toContain("<button");
  expect(html).not.toContain("任务已完成");
});
test("superseded questions are distinguished from a confirmed answer", () => {
  expect(interactionStatus({ ...message.interaction!, state: "superseded" })).toContain(
    "问题已更新",
  );
  expect(interactionStatus({ ...message.interaction!, state: "withdrawn" })).toContain("已撤回");
});

test("scheduled reminders retain their per-message window alongside shared controls", () => {
  for (const [window, label] of [
    ["near", "即将到达约定行动时间"],
    ["due", "已到约定行动时间"],
  ]) {
    const html = renderToStaticMarkup(
      <Interaction
        message={{ ...message, reason: `attention_reminder:attention:${window}` }}
        busy={false}
        onAnswer={() => {}}
        onReply={() => {}}
      />,
    );
    expect(html).toContain(label);
    expect(html).toContain("已在 Telegram 处理");
  }
});
