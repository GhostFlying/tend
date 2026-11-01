import { ExternalSourceMessage } from "./SourceDetails.tsx";
import { MessageTags } from "./MessageTags.tsx";
import { notify } from "../app/notice.ts";
import { ChoiceCard } from "../workitems/Choice.tsx";
import { useServices } from "../app/services.tsx";
import { noticeTitle } from "../upgrades/status.ts";
// Shared conversation messages and explicitly disclosed tool traces.
import { memo, useId, useLayoutEffect, useRef, useState } from "react";
import type {
  AssistantState,
  IssueConversationItem as ConversationItem,
} from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { Button } from "../ui/Button.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./Messages.module.css";
import { clock, dayLabel, messageStateLabels, systemMessageBody, type Entry } from "./model.ts";
import { Chevron } from "../shell/icons.tsx";
import { LiveStatus } from "../ui/LiveStatus.tsx";
import { conversationStatus } from "../ui/activityStatus.ts";
import { CopyIdentifier } from "../i18n/identifiers.tsx";
import { ToolRow } from "./ToolRow.tsx";
import { MessageImages, OutgoingImages } from "./Images.tsx";
import { MessageAttachments, OutgoingAttachments } from "./Attachments.tsx";
import { outgoingImages } from "./images.ts";
import { recoveryNotice } from "./recovery.ts";
import { steerNotice } from "./steer.ts";

export function Divider({ at }: { at: number }) {
  return <div className={styles.divider}>{dayLabel(at)}</div>;
}

export function OwnerMessage({
  item,
  presentation = "chat",
}: {
  item: ConversationItem;
  presentation?: "chat" | "issue";
}) {
  const state = item.state ?? "queued";
  const notice = steerNotice(item.steer);
  return (
    <div className={styles.user} data-layout={presentation} data-scroll-id={item.message_id}>
      {item.attachments?.length ? (
        <MessageAttachments attachments={item.attachments} />
      ) : (
        !!item.images?.length && <MessageImages images={item.images} />
      )}
      {item.body && <div className={styles.bubble}>{item.body}</div>}
      {presentation === "chat" && <MessageTags item={item} />}
      {item.recovery && (
        <div className={styles.state} role="status">
          {recoveryNotice(item.recovery)}
        </div>
      )}
      <div className={styles.state} data-state={state}>
        {item.control?.withdrawn_at ? "已撤回" : messageStateLabels[state]}
        {presentation === "chat" && " · "}
        {presentation === "chat" && clock(Date.parse(item.created_at))}
        {notice && <span> · {notice}</span>}
      </div>
    </div>
  );
}

/** A message of this device that tend has not shown in the conversation yet. */
export function OutgoingRow({
  message,
  queue,
}: {
  message: OutgoingMessage;
  queue: SendQueue | null;
}) {
  const { requestId } = message;
  const images = outgoingImages(message);
  return (
    <div className={styles.user} data-scroll-id={`outbox:${requestId}`}>
      {!!message.uploads?.length && <OutgoingAttachments uploads={message.uploads} />}
      {!!images.length && <OutgoingImages images={images} />}
      {message.body && <div className={styles.bubble}>{message.body}</div>}
      <div className={styles.state} data-state={message.state}>
        {message.state === "sending" &&
          (message.uploads?.length && !message.attachmentsFrozen ? "正在上传附件…" : "发送中…")}
        {message.state === "sending" && message.submitAttempted === false && (
          <>
            {" "}
            ·{" "}
            <Button
              type="button"
              variant="link"
              onClick={() =>
                void queue?.discard(requestId).catch((error) => notify(String(error), "warn"))
              }
            >
              取消发送
            </Button>
          </>
        )}
        {message.state === "sent" && `已送达 · ${clock(message.createdAt)}`}
        {message.state === "unknown" && (
          <>
            {message.uploads?.length && !message.attachmentsFrozen
              ? "附件上传结果待核对"
              : "发送结果未知"}{" "}
            ·{" "}
            <Button type="button" variant="link" onClick={() => void queue?.reconcile(requestId)}>
              查看
            </Button>{" "}
            ·{" "}
            <Button type="button" variant="link" onClick={() => void queue?.resend(requestId)}>
              重发
            </Button>{" "}
            ·{" "}
          </>
        )}
        {message.state === "failed" && (
          <>
            发送失败{message.error ? `：${message.error}` : ""} ·{" "}
            <Button
              type="button"
              variant="link"
              onClick={() =>
                void queue?.discard(requestId).catch((error) => notify(String(error), "warn"))
              }
            >
              删除
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

export function AgentTurn({
  entry,
  proseClassName,
  currentIssueId,
}: {
  entry: Extract<Entry, { kind: "agent" }>;
  proseClassName?: string | undefined;
  currentIssueId?: string;
}) {
  const root = useRef<HTMLDivElement>(null);
  const status = conversationStatus(
    entry.items,
    entry.state,
    entry.startedAt ?? null,
    entry.done?.tookMs !== undefined && entry.startedAt !== undefined
      ? entry.startedAt + entry.done.tookMs
      : null,
  );
  const tools = entry.items.filter((item) => item.tool);
  const [traceOpen, setTraceOpen] = useState(false);
  const wasActive = useRef(status.active);
  useLayoutEffect(() => {
    if (wasActive.current && !status.active) {
      const trace = root.current?.querySelector("[data-trace]");
      if (trace?.contains(document.activeElement))
        root.current
          ?.querySelector<HTMLElement>("[data-trace-trigger]")
          ?.focus({ preventScroll: true });
      setTraceOpen(false);
    }
    wasActive.current = status.active;
  }, [status.active]);
  const prefix = useId();
  const [visibleStart, setVisibleStart] = useState(Math.max(0, tools.length - 12));
  return (
    <div ref={root} className={styles.agent}>
      <LiveStatus status={status}>
        {!!tools.length && (
          <button
            type="button"
            data-trace-trigger
            className={styles.traceTrigger}
            aria-expanded={traceOpen}
            aria-controls={`${prefix}-trace`}
            onClick={(event) => {
              event.currentTarget.focus();
              if (!traceOpen) setVisibleStart(Math.max(0, tools.length - 12));
              setTraceOpen(!traceOpen);
            }}
          >
            查看过程 · {tools.length} 步 <Chevron />
          </button>
        )}
      </LiveStatus>
      {tools
        .flatMap((item) => item.choices ?? [])
        .map((choice) => (
          <ChoiceCard key={choice.choice_id} choice={choice} />
        ))}
      {!!tools.length && (
        <div data-trace id={`${prefix}-trace`} hidden={!traceOpen} className={styles.trace}>
          {visibleStart > 0 && (
            <button
              type="button"
              className={styles.traceTrigger}
              onClick={() => setVisibleStart((start) => Math.max(0, start - 12))}
            >
              显示更早 {visibleStart} 步
            </button>
          )}
          {tools.map((item, index) => (
            <AgentItem
              key={item.message_id}
              item={item}
              hidden={index < visibleStart}
              hideChoices
              id={`${prefix}-item-${item.message_id}`}
              onToolOpenChange={() => {}}
            />
          ))}
        </div>
      )}
      {entry.items
        .filter((item) => !item.tool)
        .map((item) => (
          <AgentItem
            key={item.message_id}
            item={item}
            currentIssueId={currentIssueId}
            proseClassName={proseClassName}
            hidden={false}
            id={`${prefix}-item-${item.message_id}`}
            onToolOpenChange={() => {}}
          />
        ))}
      {!currentIssueId && entry.done && (
        <div className={styles.foot} title={new Date(entry.done.at).toISOString()}>
          {clock(entry.done.at)}
        </div>
      )}
    </div>
  );
}

const assistantLabels: Record<Exclude<AssistantState, "completed">, string> = {
  streaming: "正在生成…",
  interrupted: "回复已停止",
  failed: "回复失败，保留已生成内容",
  unknown: "回复结果未知，保留已生成内容",
};

const AgentItem = memo(function AgentItem({
  item,
  hidden,
  id,
  onToolOpenChange,
  hideChoices = false,
  proseClassName,
  currentIssueId,
}: {
  item: ConversationItem;
  hidden: boolean;
  currentIssueId?: string | undefined;
  hideChoices?: boolean;
  proseClassName?: string | undefined;
  id: string;
  onToolOpenChange(id: string, open: boolean): void;
}) {
  const status = item.assistant_state;
  if (item.author === "external")
    return (
      <div
        id={id}
        hidden={hidden}
        data-scroll-id={item.message_id}
        data-tool-item={item.tool ? item.message_id : undefined}
      >
        <ExternalSourceMessage item={item} />
      </div>
    );
  return (
    <div
      id={id}
      hidden={hidden}
      data-scroll-id={item.message_id}
      data-tool-item={item.tool ? item.message_id : undefined}
    >
      {!hideChoices &&
        item.choices?.map((choice) => <ChoiceCard key={choice.choice_id} choice={choice} />)}
      {item.tool ? (
        <ToolRow
          tool={item.tool}
          onOpenChange={(open) => onToolOpenChange(item.message_id, open)}
        />
      ) : (
        <>
          <div className={proseClassName}>
            <Markdown text={item.body ?? ""} />
          </div>
          <MessageTags item={item} currentIssueId={currentIssueId} />
          {status && status !== "completed" && status !== "streaming" && (
            <div className={styles.assistantState} data-state={status} role="status">
              {assistantLabels[status]}
            </div>
          )}
        </>
      )}
    </div>
  );
});

export function SystemMessage({ item, scrollID }: { item: ConversationItem; scrollID?: string }) {
  const { openUpgrade } = useServices();
  const title = noticeTitle(item.notice_type);
  if (item.notice_type === "background_batch" && item.conversation_id !== "main")
    return (
      <details className={styles.background} data-scroll-id={scrollID ?? item.message_id}>
        <summary>
          <Chevron /> 后台核对来源{" "}
          <time title={item.created_at}>· {clock(Date.parse(item.created_at))}</time>
        </summary>
        <Markdown text={systemMessageBody(item)} />
        {item.recovery && <div role="status">{recoveryNotice(item.recovery)}</div>}
      </details>
    );
  return (
    <section
      className={styles.system}
      data-scroll-id={scrollID ?? item.message_id}
      aria-label={`${title}通知`}
    >
      <div className={styles.systemHead}>
        {title} <time dateTime={item.created_at}>{clock(Date.parse(item.created_at))}</time>
      </div>
      {item.notice_type?.startsWith("upgrade_") ? (
        <>
          <p>{item.notice_type === "upgrade_completed" ? "已更新到新版本" : title}</p>
          <details className={styles.systemDetails}>
            <summary>详细信息</summary>
            <Markdown text={systemMessageBody(item)} />
            {item.upgrade_id && <CopyIdentifier value={item.upgrade_id} />}
          </details>
        </>
      ) : (
        <Markdown text={systemMessageBody(item)} />
      )}
      {item.recovery && (
        <div className={styles.state} role="status">
          {recoveryNotice(item.recovery)}
        </div>
      )}
      {item.return_link && <MessageTags item={item} />}
      {item.state && (
        <div className={styles.state} data-state={item.state} role="status">
          {messageStateLabels[item.state]}
        </div>
      )}
      {item.choices?.map((choice) => (
        <ChoiceCard key={choice.choice_id} choice={choice} />
      ))}
      {item.upgrade_id && (
        <Button
          variant="link"
          className={styles.systemLink}
          onClick={() => openUpgrade(item.upgrade_id!)}
        >
          查看发布详情
        </Button>
      )}
    </section>
  );
}
