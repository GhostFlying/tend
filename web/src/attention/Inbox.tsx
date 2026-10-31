import { back, navigate } from "../app/router.ts";
import { PageHeader } from "../shell/PageHeader.tsx";
import { AttentionActionTime } from "./ActionTime.tsx";
import { AttentionOriginalMessage } from "./OriginalMessage.tsx";
import { SourceDetails } from "../conversation/SourceDetails.tsx";
import { optionLabel, question } from "../decisions/text.ts";
import { useSourceTransfer } from "../conversation/useSourceTransfer.ts";
import {
  LocalAttachment,
  OutgoingAttachments,
  MessageAttachments,
} from "../conversation/Attachments.tsx";
import { useEffect, useState } from "react";
import type {
  AttentionView,
  ConfirmAttentionPayload,
  RespondAttentionPayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import { useStore } from "../state/store.ts";
import { attentionCommand } from "./actions.ts";
import type { OpOutcome } from "../app/ops.ts";
import styles from "./Inbox.module.css";

export function Inbox({
  initial,
  onIssue,
  onClose,
}: {
  initial?: string | undefined;
  onIssue(id: string): void;
  onClose(): void;
}) {
  const { live } = useServices();
  const [kind, setKind] = useState("");
  const selected = initial ?? "";
  const setSelected = (id: string) => navigate(id ? { page: "attention", id } : { page: "inbox" });
  const state = useStore(live.attention.inbox.state);
  const next = useStore(live.attention.next.state);
  useEffect(() => {
    void live.attention.openInbox({ ...(kind ? { kind } : {}) });
    void live.attention.openNext();
  }, [live, kind]);
  const items = state.value?.items ?? [];
  return (
    <section className={styles.root} data-selected={!!selected} aria-label="待处理事项">
      <PageHeader title="待处理" onBack={onClose} />
      <p className={styles.muted}>需要你决定或亲自推进的事项。</p>
      <nav className={styles.tabs} aria-label="事项类型">
        {[
          ["", "全部"],
          ["decision", "待决策"],
          ["action", "待行动"],
          ["information", "通知"],
        ].map(([key, label]) => (
          <button
            key={key}
            aria-pressed={kind === key}
            onClick={() => {
              setKind(key!);
              setSelected("");
            }}
          >
            {label}
          </button>
        ))}
      </nav>
      <div className={styles.split} data-selected={!!selected}>
        <aside className={styles.list}>
          {state.error && (
            <p role="status" className={styles.warning}>
              读取失败，已有事项仍保留。
              <button onClick={() => live.attention.refresh()}>重新读取</button>
            </p>
          )}
          {!state.value && !state.error && <p>正在读取…</p>}
          {state.value && items.length === 0 && <p className={styles.muted}>暂无此类事项。</p>}
          {items.map((item) => (
            <button
              className={styles.row}
              key={item.attention_id}
              aria-current={item.attention_id === selected}
              onClick={() => setSelected(item.attention_id)}
            >
              <strong>{item.title}</strong>
              <span className={styles.muted}>
                {item.read_at ? "已读" : "未读"} ·{" "}
                {item.status === "open"
                  ? "未解决"
                  : item.status === "resolved"
                    ? "已解决"
                    : item.status === "withdrawn"
                      ? "已撤回"
                      : item.status}
              </span>
            </button>
          ))}
          {(next.value?.items ?? []).some((item) => item.status === "blocked") && (
            <div className={styles.risks}>
              <p className={styles.muted}>近期风险</p>
              {(next.value?.items ?? [])
                .filter((item) => item.status === "blocked")
                .map((item) => (
                  <p key={item.entry_id}>{item.title}</p>
                ))}
            </div>
          )}
          {state.value?.more && (
            <button
              className={styles.button}
              onClick={() =>
                void live.attention.openInbox({
                  ...(kind ? { kind } : {}),
                  ...(state.value?.next ? { after: state.value.next } : {}),
                })
              }
            >
              下一页
            </button>
          )}
        </aside>
        <div className={styles.detail}>
          {selected ? (
            <AttentionDetails
              key={selected}
              id={selected}
              showBack={false}
              onIssue={onIssue}
              onBack={() => back({ page: "inbox" })}
            />
          ) : (
            <p className={styles.muted}>选择一个事项查看。</p>
          )}
        </div>
      </div>
    </section>
  );
}

export function AttentionDetails({
  id,
  onIssue,
  onBack,
  showBack = true,
}: {
  id: string;
  onIssue(id: string): void;
  onBack(): void;
  showBack?: boolean;
}) {
  const { live } = useServices();
  const state = useStore(live.attention.attention(id).state);
  const a = state.value;
  return (
    <>
      {showBack && (
        <button className={styles.mobileBack} onClick={onBack}>
          ← 待处理
        </button>
      )}
      {a ? (
        <AttentionForm
          key={`${a.attention_id}:${a.revision}`}
          item={a}
          onIssue={onIssue}
          onBack={onBack}
        />
      ) : (
        <p role="status">
          {state.error ? "暂时无法读取此事项。" : "正在读取…"}
          <button onClick={() => live.attention.refresh()}>重新读取</button>
        </p>
      )}
    </>
  );
}

function AttentionForm({
  item: a,
  onIssue,
  onBack,
}: {
  item: AttentionView;
  onIssue(id: string): void;
  onBack(): void;
}) {
  const { live, client } = useServices();
  const operation = useDurableOp(client, `attention:${a.attention_id}`);
  const source = useSourceTransfer(
    `attention:${a.attention_id}`,
    a.issue_id ? `work-item:${a.issue_id}` : "main",
    true,
  );
  const [choice, setChoice] = useState("");
  const [text, setText] = useState("");
  const [result, setResult] = useState<OpOutcome | null>(null);
  const options =
    a.question_options ??
    a.choice?.options ??
    (Array.isArray(a.decision?.options)
      ? a.decision.options.filter((o): o is string => typeof o === "string")
      : []);
  const original = operation.original;
  const recordedAnswer =
    a.decision?.answer && typeof a.decision.answer === "object"
      ? (a.decision.answer as Record<string, unknown>)
      : undefined;
  const recordedOption = typeof recordedAnswer?.option === "string" ? recordedAnswer.option : "";
  const recordedText = typeof recordedAnswer?.text === "string" ? recordedAnswer.text : "";
  const shownText =
    a.status === "resolved"
      ? a.kind === "action"
        ? (a.resolution ?? "")
        : (a.choice?.answer ?? recordedText)
      : original?.operation === "attentions.confirm"
        ? (original.payload as ConfirmAttentionPayload).summary
        : original?.operation === "attentions.respond"
          ? ((original.payload as RespondAttentionPayload).text ?? "")
          : a.kind === "action"
            ? (source.current?.body ?? source.content.text)
            : text;
  const shownChoice =
    a.status === "resolved"
      ? (a.choice?.answer?.split("\n\n")[0] ?? recordedOption)
      : original?.operation === "attentions.respond"
        ? ((original.payload as RespondAttentionPayload).option ?? "")
        : choice;
  const run = async (action: "respond" | "confirm" | "read" | "archive" | "reconcile") => {
    let sourceMessageID: string | undefined;
    if (
      action === "confirm" &&
      (source.content.files.length || source.content.attachments.length || source.current)
    ) {
      try {
        const record = await source.submit();
        if (record?.state !== "sent" || !record.messageId) {
          setResult({ kind: "unknown", message: "原件保存结果待核实；处理事项尚未解决。" });
          return;
        }
        sourceMessageID = record.messageId;
      } catch {
        setResult({ kind: "unknown", message: "原件保存结果待核实；处理事项尚未解决。" });
        return;
      }
    }
    const command =
      action === "reconcile"
        ? undefined
        : action === "respond"
          ? attentionCommand(a, "respond", {
              version: a.question_version ?? a.choice?.version ?? a.decision?.version ?? 0,
              option: choice,
              text: a.choice ? [choice, text].filter(Boolean).join("\n\n") : text,
            })
          : action === "confirm"
            ? attentionCommand(a, "confirm", {
                summary: shownText,
                ...(sourceMessageID ? { source_message_id: sourceMessageID } : {}),
              })
            : attentionCommand(
                a,
                "present",
                action === "read" ? { read: !a.read_at } : { archived: !a.archived_at },
              );
    try {
      const out = await operation.execute(command);
      setResult(out);
      if (out.kind === "done" || out.kind === "stale") live.attention.refresh();
    } catch {
      setResult({ kind: "unknown", message: "尚未取得回执，请核实原请求。" });
    }
  };
  const blocked = operation.busy || operation.pending || source.busy || a.status !== "open";
  return (
    <>
      <h2>{a.title}</h2>
      {a.issue_id && <button onClick={() => onIssue(a.issue_id!)}>查看所属Issue →</button>}
      <p className={styles.muted}>
        {a.read_at ? "已读" : "未读"} ·{" "}
        {a.status === "open"
          ? "未解决"
          : a.status === "resolved"
            ? "已解决"
            : a.status === "withdrawn"
              ? "已撤回"
              : a.status}
      </p>
      <p className={styles.muted}>
        提出者：{a.raised_by.kind === "owner" ? "你" : a.raised_by.subject || a.raised_by.kind} ·
        提出于{new Date(a.created_at).toLocaleString("zh-CN")}
      </p>
      <p className={styles.muted}>
        <AttentionActionTime item={a} />
      </p>
      {a.source_message_id && <AttentionOriginalMessage id={a.source_message_id} />}
      {a.kind === "decision" && (
        <>
          <h3>请选择</h3>
          {a.decision?.question && <p>{question(a.decision).text}</p>}
          {a.choice?.question && <p>{a.choice.question}</p>}
          <div className={styles.options}>
            {options.map((option) => (
              <label key={option} className={styles.option}>
                <input
                  type="radio"
                  name={`answer-${a.attention_id}`}
                  value={option}
                  checked={shownChoice === option}
                  disabled={blocked}
                  onChange={() => setChoice(option)}
                />
                {a.decision ? optionLabel(a.decision, option) : option}
              </label>
            ))}
          </div>
          <label>
            补充说明（可选）
            <textarea
              className={styles.textarea}
              value={shownText}
              disabled={blocked}
              onChange={(e) => setText(e.target.value)}
            />
          </label>
        </>
      )}
      {a.kind === "action" && (
        <label>
          <h3>处理结果</h3>
          <textarea
            className={styles.textarea}
            value={shownText}
            disabled={blocked || !!source.current}
            onChange={(e) => source.draft.write(e.target.value)}
            placeholder="填写处理结果或补充说明…"
          />
        </label>
      )}
      {a.kind === "action" && a.status === "open" && (
        <>
          <label>
            补充原文件
            <input
              type="file"
              multiple
              disabled={blocked || !!source.current || !source.content.loaded}
              onChange={(e) => {
                source.draft.addFiles(Array.from(e.target.files ?? []));
                e.target.value = "";
              }}
            />
          </label>
          {source.content.files.map((file, i) => (
            <div key={i}>
              <LocalAttachment blob={file} name={file.name} />
              <button
                disabled={blocked || !!source.current}
                onClick={() => source.draft.removeFile(i)}
              >
                移除
              </button>
            </div>
          ))}
          {!!source.content.attachments.length && (
            <MessageAttachments attachments={source.content.attachments} />
          )}
          {!!source.current?.uploads?.length && (
            <OutgoingAttachments uploads={source.current.uploads} />
          )}
          {source.current?.state === "failed" && (
            <p className={styles.warning}>
              {source.current.error || "原件来源保存被拒绝。"}
              <button onClick={() => void source.editRefused()}>取回后修改</button>
            </p>
          )}
          {source.current?.state === "unknown" && (
            <p className={styles.warning}>原件保存待核实；再次提交会先核实原请求，尚未解决此项。</p>
          )}
          {(source.error || source.content.error) && (
            <p role="alert">{source.error || source.content.error}</p>
          )}
        </>
      )}
      {a.status === "resolved" && a.resolution_source_id && (
        <SourceDetails id={a.resolution_source_id} />
      )}
      {operation.storageError && (
        <p role="alert" className={styles.warning}>
          {operation.storageError}
        </p>
      )}
      {result && !(operation.pending && result.kind === "unknown") && (
        <p role="status" className={result.kind === "done" ? styles.muted : styles.warning}>
          {result.kind === "done" ? "已记录。" : result.message}
        </p>
      )}
      {operation.pending && (
        <div className={styles.warning}>
          提交结果待确认，请勿重复提交。
          <button
            className={styles.primary}
            disabled={operation.busy}
            onClick={() => void run("reconcile")}
          >
            核实原提交结果
          </button>
        </div>
      )}
      <div className={styles.actions}>
        {a.kind === "decision" && (
          <button
            className={styles.primary}
            disabled={blocked || (!choice && !text.trim())}
            onClick={() => void run("respond")}
          >
            提交决定
          </button>
        )}
        {a.kind === "action" && (
          <button
            className={styles.primary}
            disabled={blocked || !shownText.trim() || !source.content.loaded}
            onClick={() => void run("confirm")}
          >
            提交处理结果
          </button>
        )}
        <button className={styles.button} onClick={onBack}>
          稍后处理
        </button>
        <button disabled={operation.busy || operation.pending} onClick={() => void run("read")}>
          {a.read_at ? "标记未读" : "标记已读"}
        </button>
        <button disabled={operation.busy || operation.pending} onClick={() => void run("archive")}>
          {a.archived_at ? "展开" : "收起"}
        </button>
      </div>
      <p className={styles.muted}>已读和收起不会解决此项；稍后处理仅返回列表。</p>
    </>
  );
}
