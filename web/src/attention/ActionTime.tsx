import type { AttentionView } from "../api/generated/api.ts";

export function AttentionActionTime({ item }: { item: AttentionView }) {
  const time = item.latest_action_at;
  const zone = item.latest_action_timezone;
  return (
    <span>
      {time === undefined
        ? "最晚行动时间未定"
        : `最晚行动：${new Date(time).toLocaleString("zh-CN", {
            ...(zone ? { timeZone: zone } : {}),
            month: "numeric",
            day: "numeric",
            hour: "2-digit",
            minute: "2-digit",
          })}${zone ? `（${zone}）` : ""}`}
      <br />
      提醒：
      {item.latest_action_reminders === true
        ? "已开启"
        : item.latest_action_reminders === false
          ? "未开启"
          : "未记录"}
    </span>
  );
}
