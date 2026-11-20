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
  MessageProcessingState,
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
import { issueOwnerSteerNotice, steerNotice } from "./steer.ts";
import { groupedProcessing } from "./processing.ts";
import { BackgroundNotice } from "./BackgroundNotice.tsx";

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
  const urgentReceipt = item.consumed_issue_owner_steer;
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
        {urgentReceipt && state !== "consumed" && <span> · {messageStateLabels.consumed}</span>}
        {presentation === "chat" && " · "}
        {presentation === "chat" && clock(Date.parse(item.created_at))}
        {notice && <span> · {notice}</span>}
      </div>
      {urgentReceipt && (
        <details className={`${styles.state} ${styles.steerReceipt}`}>
          <summary>急件回执</summary>
          <div>内容版本 {urgentReceipt.source_revision}</div>
          <div>
            回执消息 <code>{urgentReceipt.message_id}</code>
          </div>
          <div>
            运行 <code>{urgentReceipt.run_id}</code>
          </div>
          <div>
            原生消息 <code>{urgentReceipt.native_item_id}</code>
          </div>
        </details>
      )}
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
        {message.state === "sent" && `已入账 · ${clock(message.createdAt)}`}
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

export function UrgentMessage({ item, author }: { item: ConversationItem; author?: string }) {
  const steer = item.issue_owner_steer;
  if (!steer) return null;
  const sender = steer.sender.kind === "main_agent" ? "主 Agent" : "操作员";
  const subject = steer.sender.subject;
  return (
    <section data-scroll-id={item.message_id} aria-label="紧急投递">
      <div className={styles.state}>{author ?? (subject ? `${sender} · ${subject}` : sender)}</div>
      <p className={styles.state} role="status">
        {issueOwnerSteerNotice(steer)}
      </p>
      <Markdown text={item.body ?? ""} />
    </section>
  );
}

export function ChatReplies({ items }: { items: readonly ConversationItem[] }) {
  const processing = groupedProcessing(items);
  const prefix = useId();
  return (
    <div className={styles.agent}>
      {items.map((item) => (
        <AgentItem
          key={item.message_id}
          item={item}
          processing={processing.get(item.message_id) ?? null}
          hidden={false}
          id={`${prefix}-item-${item.message_id}`}
          onToolOpenChange={() => {}}
        />
      ))}
    </div>
  );
}

export function AgentTurn({
  entry,
  proseClassName,
  currentIssueId,
  processUnavailable = false,
  process,
}: {
  process?:
    | {
        open: boolean;
        ready: boolean;
        activeTool?: string | undefined;
        latestActivity?: ConversationItem | null | undefined;
        toggle(): void;
        footer: React.ReactNode;
      }
    | undefined;
  processUnavailable?: boolean;
  entry: Extract<Entry, { kind: "agent" }>;
  proseClassName?: string | undefined;
  currentIssueId?: string;
}) {
  const root = useRef<HTMLDivElement>(null);
  const status = conversationStatus(
    entry.items,
    entry.state,
    entry.startedAt ?? null,
    entry.endedAt !== undefined
      ? entry.endedAt
      : entry.done?.tookMs !== undefined &&
          entry.startedAt !== undefined &&
          entry.startedAt !== null
        ? entry.startedAt + entry.done.tookMs
        : null,
    process,
  );
  const tools = entry.items.filter((item) => item.tool);
  const processing = groupedProcessing(entry.items);
  const [localTraceOpen, setTraceOpen] = useState(false);
  const traceOpen = process?.open ?? localTraceOpen;
  const wasActive = useRef(status.active);
  useLayoutEffect(() => {
    if (!process && wasActive.current && !status.active) {
      const trace = root.current?.querySelector("[data-trace]");
      if (trace?.contains(document.activeElement))
        root.current
          ?.querySelector<HTMLElement>("[data-trace-trigger]")
          ?.focus({ preventScroll: true });
      setTraceOpen(false);
    }
    wasActive.current = status.active;
  }, [process, status.active]);
  const prefix = useId();
  const initialVisibleStart = Math.max(0, tools.length - 12);
  const ready = process?.ready;
  const [visibleStart, setVisibleStart] = useState(ready === false ? null : initialVisibleStart);
  // Commit the initial window before the Issue measures its scroll anchor.
  // Keep that window as later pages and tool updates arrive.
  if (ready && visibleStart === null) setVisibleStart(initialVisibleStart);
  const firstVisible = visibleStart ?? initialVisibleStart;
  return (
    <div ref={root} className={styles.agent}>
      <LiveStatus status={status}>
        <button
          type="button"
          data-trace-trigger
          className={styles.traceTrigger}
          aria-expanded={traceOpen}
          aria-controls={`${prefix}-trace`}
          onClick={(event) => {
            event.currentTarget.focus();
            if (!traceOpen && !process) setVisibleStart(initialVisibleStart);
            if (process) process.toggle();
            else setTraceOpen(!traceOpen);
          }}
        >
          查看过程{tools.length ? ` · ${tools.length} 步` : ""} <Chevron />
        </button>
      </LiveStatus>
      {tools
        .flatMap((item) => item.choices ?? [])
        .map((choice) => (
          <ChoiceCard key={choice.choice_id} choice={choice} />
        ))}
      <div data-trace id={`${prefix}-trace`} hidden={!traceOpen} className={styles.trace}>
        <p>这里展示已公开的执行步骤和回复，不包含模型的隐藏推理。</p>
        {!process && !tools.length && (
          <p>
            {processUnavailable
              ? "公开过程暂不可读取，请使用上方的重读入口。"
              : status.active
                ? "尚未记录可查看的公开执行步骤，公开回复会显示在下方。"
                : "这段历史记录没有保存可查看的执行步骤；已保存的公开回复显示在下方。"}
          </p>
        )}
        {firstVisible > 0 && (
          <button
            type="button"
            className={styles.traceTrigger}
            onClick={() => setVisibleStart(Math.max(0, firstVisible - 12))}
          >
            显示更早 {firstVisible} 步
          </button>
        )}
        {tools.map((item, index) => (
          <AgentItem
            key={item.message_id}
            item={item}
            hidden={index < firstVisible}
            hideChoices
            id={`${prefix}-item-${item.message_id}`}
            onToolOpenChange={() => {}}
          />
        ))}
        {process?.footer}
      </div>
      {entry.items
        .filter((item) => !item.tool)
        .map((item) => (
          <AgentItem
            key={item.message_id}
            item={item}
            currentIssueId={currentIssueId}
            processing={processing.get(item.message_id) ?? null}
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
  processing,
}: {
  item: ConversationItem;
  hidden: boolean;
  currentIssueId?: string | undefined;
  processing?: MessageProcessingState | null | undefined;
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
          {item.purpose === "final" && (
            <span className={styles.state} title="Agent 标记的成果消息，不代表验收或交付状态">
              成果
            </span>
          )}
          <div className={proseClassName}>
            <Markdown
              text={item.body ?? ""}
              files={item.attachments ?? []}
              fileScope="conversation/attachments"
            />
          </div>
          {!!item.attachments?.length && <MessageAttachments attachments={item.attachments} />}
          <MessageTags item={item} currentIssueId={currentIssueId} processing={processing} />
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

export function SystemMessage({
  item,
  scrollID,
  presentation = "chat",
}: {
  item: ConversationItem;
  scrollID?: string;
  presentation?: "chat" | "issue";
}) {
  const { openUpgrade } = useServices();
  const title = noticeTitle(item.notice_type);
  if (item.notice_type === "background_batch" && item.conversation_id !== "main")
    return <BackgroundNotice item={item} scrollID={scrollID} presentation={presentation} />;
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
      {item.choices
        ?.filter((choice) => presentation === "issue" || choice.status === "open")
        .map((choice) => (
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
