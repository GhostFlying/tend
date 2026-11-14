import { useEffect, useRef, useState } from "react";
import type {
  BackgroundSourcePage,
  IssueConversationItem,
  BackgroundSourcePreview,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Link } from "../app/router.ts";
import { Chevron } from "../shell/icons.tsx";
import { Markdown } from "./Markdown.tsx";
import { clock, systemMessageBody } from "./model.ts";
import { recoveryNotice } from "./recovery.ts";
import styles from "./Messages.module.css";

const sourceLabels: Record<string, string> = {
  "issue.changed": "Issue 信息变化",
  "issue.status_changed": "Issue 工作状态变化",
  "issue.manager_claimed": "负责人已领取 Issue",
  "task.completed": "任务完成",
  "task.context_changed": "任务上下文变化",
  "task.preparation_blocked": "任务准备受阻",
  "decision.opened": "新增待答问题",
  "decision.answered": "问题已有答复",
  "work_item_choice.answered": "问题已有答复",
  "attention.responsibility_changed": "待办责任变化",
  "conversation.associations_changed": "消息关联变化",
  "followup.triggered": "跟进条件触发",
  "webhook.received": "收到外部事件",
  "consultation.promoted": "讨论已关联 Issue",
};

function sourceLabel(kind: string): string {
  return sourceLabels[kind] ?? kind;
}

export function backgroundHeading(item: IssueConversationItem): string {
  const recipient = item.background?.presentation?.recipient;
  const name = recipient?.display_name || recipient?.identity.local_id;
  const count = item.background?.source_count;
  return `${name ? `${name} · ` : ""}${count === undefined ? "后台变化" : `${count} 项后台变化`}`;
}

export function backgroundState(item: IssueConversationItem): string {
  const status = item.background?.presentation?.recipient?.status;
  if (status === "starting") return "已安排，等待接收确认";
  if (status === "rejected") return "接收未成功";
  if (status === "started") return "已接收，正在处理";
  if (status === "unknown") return "执行结果待核对";
  if (status === "interrupted") return "处理已停止";
  if (status === "failed") return "本次处理失败";
  if (status === "completed" && item.background?.checkin) return "已完成本轮核对";
  if (status === "completed") return "本轮执行已结束";
  if (item.background?.status === "pending") return "待处理，尚无接收记录";
  return "接收者与处理进度未记录";
}

function SourceLink({ source }: { source: BackgroundSourcePreview }) {
  if (source.object_type === "task" && source.object_id)
    return <Link to={{ page: "task", id: source.object_id }}>查看任务</Link>;
  if (source.object_type === "issue" && source.object_id)
    return <Link to={{ page: "issue", id: source.object_id }}>查看 Issue</Link>;
  return null;
}

export function BackgroundNotice({
  item,
  scrollID,
  presentation,
}: {
  item: IssueConversationItem;
  scrollID?: string | undefined;
  presentation: "chat" | "issue";
}) {
  const { client } = useServices();
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState<BackgroundSourcePage>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const previews = item.background?.presentation?.sources ?? [];
  const kinds = [...new Set(previews.map((source) => sourceLabel(source.kind)))];
  const load = (after = 0) => {
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    setLoading(true);
    setError("");
    void client
      .get<BackgroundSourcePage>(
        `/api/v1/background/${encodeURIComponent(item.message_id)}/sources?after=${after}`,
        controller.signal,
      )
      .then((next) => {
        if (controller.signal.aborted) return;
        setPage((previous) => ({
          ...next,
          sources: after ? [...(previous?.sources ?? []), ...(next.sources ?? [])] : next.sources,
        }));
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(String(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
  };
  useEffect(() => () => pending.current?.abort(), [item.message_id]);
  const icon = (
    <span className={styles.backgroundIcon} aria-hidden="true">
      <Chevron />
    </span>
  );
  return (
    <details
      className={styles.background}
      data-layout={presentation}
      data-scroll-id={scrollID ?? item.message_id}
      onToggle={(event) => {
        const next = event.currentTarget.open;
        setOpen(next);
        if (next && !page && !loading) load();
      }}
    >
      <summary>
        {presentation === "chat" && icon}
        <span>
          {backgroundHeading(item)}
          {!!kinds.length && `：${kinds.join("、")}`}{" "}
          <time title={item.created_at}>· {clock(Date.parse(item.created_at))}</time>
        </span>
        {presentation === "issue" && icon}
      </summary>
      <div className={styles.backgroundDetails}>
        <p role="status">{backgroundState(item)}</p>
        {!!previews.length && (
          <ul>
            {previews.map((source) => (
              <li key={source.seq}>
                <strong>{sourceLabel(source.kind)}</strong> · <SourceLink source={source} />
                <p>{source.summary || "来源未附文字；展开原始记录查看。"}</p>
              </li>
            ))}
          </ul>
        )}
        {item.background?.presentation?.more && <p>以上为部分变化，原始来源保留完整分页。</p>}
        {!previews.length && <Markdown text={systemMessageBody(item)} />}
        {open && (
          <div aria-label="后台变化原始来源">
            {page?.sources?.map((source) => (
              <details key={source.seq}>
                <summary>
                  {sourceLabel(source.kind)}
                  {source.cancelled_at ? " · 已撤销" : ""} · 原始来源 {source.seq}
                </summary>
                {source.cancelled_at ? <p>保留历史原件，不再等待本批次处理。</p> : null}
                <Markdown text={source.body} />
              </details>
            ))}
            {page && !page.sources?.length && <p>该批次没有保留的来源条目。</p>}
            {error && (
              <p role="alert">
                暂时无法读取完整来源；已保存的变化概要仍可查看。{" "}
                <button type="button" onClick={() => load(page?.next ?? 0)}>
                  重新读取
                </button>
              </p>
            )}
            {loading && <p role="status">正在读取原始来源…</p>}
            {page?.more && !error && (
              <button type="button" disabled={loading} onClick={() => load(page.next)}>
                查看更多原始来源
              </button>
            )}
          </div>
        )}
        {item.recovery && <div role="status">{recoveryNotice(item.recovery)}</div>}
      </div>
    </details>
  );
}
