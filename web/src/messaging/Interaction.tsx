import type { MessagingInteraction, MessagingMessage } from "../api/generated/api.ts";
import { Link } from "../app/router.ts";
import { Button } from "../ui/Button.tsx";
import styles from "./Messaging.module.css";

export function interactionStatus(i: MessagingInteraction): string {
  if (i.state === "open") return "等你回答";
  if (i.state === "superseded") return "问题已更新，请回答新版问题";
  if (i.state !== "answered") return "此请求已撤回或不再适用";
  return i.handled_channel === "telegram"
    ? "已在 Telegram 处理"
    : i.handled_channel === "web"
      ? "已在 Web 处理"
      : "已回答";
}
export function Interaction({
  message,
  busy,
  onAnswer,
  onReply,
}: {
  message: MessagingMessage;
  busy: boolean;
  onAnswer(message: MessagingMessage, option: string): void;
  onReply(message: MessagingMessage): void;
}) {
  const i = message.interaction!;
  return (
    <section
      className={styles.question}
      aria-label={i.question}
      data-interaction={i.interaction_id}
    >
      {message.reason?.startsWith("attention_reminder:") && (
        <p className={styles.muted}>
          {message.reason.endsWith(":near") ? "即将到达约定行动时间" : "已到约定行动时间"} ·
          阅读或归档不会完成此事项
        </p>
      )}
      <div className={styles.questionStatus} data-open={i.state === "open"}>
        {interactionStatus(i)}
      </div>
      <h2>{i.question}</h2>
      {i.context && <p className={styles.context}>{i.context}</p>}
      {i.issue_id && (
        <Link className={styles.related} to={{ page: "issue", id: i.issue_id }}>
          查看相关 Issue
        </Link>
      )}
      {i.state === "open" ? (
        <>
          <div className={styles.options}>
            {(i.options ?? []).map((option, index) => (
              <Button
                key={option.option_id}
                variant={
                  option.effect === "cancel" ? "danger" : index === 0 ? "primary" : "secondary"
                }
                disabled={busy}
                onClick={() => onAnswer(message, option.option_id)}
              >
                {option.label}
              </Button>
            ))}
          </div>
          {(i.options ?? []).some((o) => o.effect === "implement") && (
            <p className={styles.muted}>重新实现会保留现有要求，启动新一轮执行。</p>
          )}
          <Button variant="link" disabled={busy} onClick={() => onReply(message)}>
            {i.object_type === "attention" ? "填写完成说明" : "文字回答"}
          </Button>
          <p className={styles.muted}>
            {i.object_type === "attention"
              ? "请明确说明已经完成的事项。"
              : "也可以引用这条消息，直接回复你的决定或补充要求。"}
          </p>
        </>
      ) : (
        <>
          {i.answer_summary && <p className={styles.answer}>你的回答：{i.answer_summary}</p>}
          {i.handled_at ? (
            <time className={styles.muted}>{new Date(i.handled_at).toLocaleString()}</time>
          ) : null}
          {i.effect && (
            <p className={styles.muted}>
              {i.effect.applied
                ? "所选操作已应用；后续执行结果会继续反馈。"
                : `操作未应用${i.effect.reason ? `：${i.effect.reason}` : ""}`}
            </p>
          )}
        </>
      )}
    </section>
  );
}
