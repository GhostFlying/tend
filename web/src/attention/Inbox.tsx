import { actorLabel } from "./actorLabel.ts";
import { taskLook } from "../tasks/status.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { Button, Tabs, Badge, EmptyState, Textarea } from "../ui/index.ts";
import { back, navigate, Link, useEntryState } from "../app/router.ts";
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
import { useEffect, useMemo, useState } from "react";
import type {
  AttentionView,
  IssueConversationItem,
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
}: {
  initial?: string | undefined;
  onIssue(id: string): void;
}) {
  const { live } = useServices();
  const [kind, setKind] = useEntryState("inboxKind", "");
  const [scope, setScope] = useEntryState<"pending" | "all">("inboxScope", "pending");
  const selected = initial ?? "";
  const [autoSelect, setAutoSelect] = useEntryState("inboxAutoSelect", !initial);
  const [now, setNow] = useState(() => Date.now());
  const setSelected = (id: string, nextScope = scope, nextKind = kind) =>
    navigate(id ? { page: "attention", id } : { page: "inbox" }, {
      replace: !id && !selected,
      state: { inboxAutoSelect: !id, inboxScope: nextScope, inboxKind: nextKind },
    });
  const defer = () =>
    navigate(
      { page: "inbox" },
      { state: { inboxAutoSelect: false, inboxScope: scope, inboxKind: kind } },
    );
  const state = useStore(live.attention.inbox.state);
  const next = useStore(live.attention.next.state);
  useEffect(() => {
    void live.attention.openInbox({
      ...(kind ? { kind } : {}),
      ...(scope === "pending" ? { status: "open" } : {}),
      includeArchived: scope === "all" || kind !== "information",
      actionableOnly: scope === "pending" && kind !== "information",
    });
    void live.attention.openNext();
    return () => live.attention.inbox.close();
  }, [live, kind, scope]);
  const items = (state.value?.items ?? []).filter(
    (item) => scope !== "pending" || item.kind !== "information" || !item.archived_at,
  );
  const firstID = items[0]?.attention_id;
  useEffect(() => {
    const media = window.matchMedia("(min-width: 761px)");
    const selectFirst = () => {
      if (
        media.matches &&
        !selected &&
        firstID &&
        autoSelect &&
        !state.loading &&
        live.attention.inbox.state.get().value === state.value
      ) {
        setAutoSelect(false);
        navigate(
          { page: "attention", id: firstID },
          { replace: true, state: { inboxScope: scope, inboxKind: kind } },
        );
      }
    };
    selectFirst();
    media.addEventListener("change", selectFirst);
    return () => media.removeEventListener("change", selectFirst);
  }, [firstID, selected, state.loading, state.value, live, autoSelect, setAutoSelect, scope, kind]);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(timer);
  }, []);
  return (
    <section className={styles.root} data-selected={!!selected} aria-label="待处理事项">
      <div className={styles.pageHeader}>
        <PageHeader
          title="收件箱"
          {...(selected ? { onBack: () => back({ page: "inbox" }) } : {})}
        />
      </div>
      <p className={`${styles.muted} ${styles.context}`}>需要你决定或亲自推进的事项。</p>
      <Tabs
        className={styles.tabs ?? ""}
        label="记录范围"
        value={scope}
        items={[
          { value: "pending", label: "待处理" },
          { value: "all", label: "全部记录" },
        ]}
        onChange={(value) => {
          setScope(value);
          setAutoSelect(true);
          setSelected("", value);
        }}
      />
      <Tabs
        className={styles.tabs ?? ""}
        label="事项类型"
        value={kind}
        items={[
          { value: "", label: "全部" },
          { value: "decision", label: "待决策" },
          { value: "action", label: "待行动" },
          { value: "information", label: "通知" },
        ]}
        onChange={(key) => {
          setAutoSelect(true);
          setKind(key);
          setSelected("", scope, key);
        }}
      />
      <div className={styles.split} data-selected={!!selected}>
        <aside className={styles.list}>
          {state.error && (
            <p role="status" className={styles.warning}>
              读取失败，已有事项仍保留。
              <Button onClick={() => live.attention.refresh()}>重新读取</Button>
            </p>
          )}
          {!state.value && !state.error && <p>正在读取…</p>}
          {state.value && items.length === 0 && (
            <EmptyState>
              {scope === "pending"
                ? "暂无待处理事项。已处理事项可在全部记录中查看。"
                : "暂无此类记录。"}
            </EmptyState>
          )}
          {items.map((item) => {
            return (
              <article
                className={styles.row}
                key={item.attention_id}
                data-selected={item.attention_id === selected}
              >
                <Button
                  variant="ghost"
                  className={styles.rowOpen}
                  aria-current={item.attention_id === selected}
                  onClick={() => {
                    setAutoSelect(false);
                    setSelected(item.attention_id);
                  }}
                >
                  <span className={styles.rowTitle} data-unread={!item.read_at}>
                    {!item.read_at && <span className={styles.unread} aria-label="未读" />}
                    <span>{attentionContextTitle(item)}</span>
                  </span>
                  <span className={styles.questionSummary}>{attentionHeading(item)}</span>
                  {item.task_context && (
                    <span className={styles.summary}>
                      {item.task_context.has_current_artifact
                        ? "当前版本已有交付物"
                        : "当前版本尚无交付物"}
                      {item.task_context.current_artifact_accepted
                        ? " · 已通过验收"
                        : " · 尚无通过验收的记录"}
                    </span>
                  )}
                  <InboxSummary item={item} />
                </Button>
                <div className={styles.rowMeta}>
                  <Badge>{kindLabel(item.kind)}</Badge>
                  <span>{statusLabel(item.status)}</span>
                  <time
                    dateTime={new Date(item.created_at).toISOString()}
                    title={new Date(item.created_at).toLocaleString("zh-CN")}
                  >
                    {relativeTime(item.created_at, now)}
                  </time>
                  {item.issue_id && (
                    <Link to={{ page: "issue", id: item.issue_id }} title={item.issue_id}>
                      查看 Issue →
                    </Link>
                  )}
                </div>
              </article>
            );
          })}
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
            <Button
              variant="secondary"
              onClick={() =>
                void live.attention.openInbox({
                  ...(kind ? { kind } : {}),
                  ...(scope === "pending" ? { status: "open" } : {}),
                  includeArchived: scope === "all" || kind !== "information",
                  actionableOnly: scope === "pending" && kind !== "information",
                  ...(state.value?.next ? { after: state.value.next } : {}),
                })
              }
            >
              下一页
            </Button>
          )}
        </aside>
        <div className={styles.detail}>
          {selected ? (
            <AttentionDetails
              key={selected}
              id={selected}
              showBack={false}
              onIssue={onIssue}
              onBack={defer}
            />
          ) : (
            <EmptyState>选择一个事项查看。</EmptyState>
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
        <Button className={styles.mobileBack} onClick={onBack}>
          ← 待处理
        </Button>
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
          <Button onClick={() => live.attention.refresh()}>重新读取</Button>
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
  const recordedEffect =
    recordedAnswer?.effect && typeof recordedAnswer.effect === "object"
      ? (recordedAnswer.effect as Record<string, unknown>)
      : undefined;
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
  const failure = attentionFailure(a);
  const currentFailure =
    a.status === "open" &&
    a.task_context?.question_current &&
    a.task_context?.block_reason === "waiting_decision" &&
    a.task_context.execution?.state === "waiting_owner"
      ? failure
      : undefined;
  return (
    <>
      <header className={styles.issueContext}>
        <h2>
          {a.issue_id ? (
            <Button variant="link" onClick={() => onIssue(a.issue_id!)}>
              {attentionContextTitle(a)}
            </Button>
          ) : (
            attentionContextTitle(a)
          )}
        </h2>
        {a.task_title && <p className={styles.taskContext}>执行任务：{a.task_title}</p>}
      </header>
      {a.task_context && (
        <section className={styles.currentResult} aria-label="当前结果">
          <h3>当前结果</h3>
          {currentFailure ? (
            <p>
              <strong>{currentFailure.stage}失败</strong>：{currentFailure.reason}。
            </p>
          ) : (
            <p>{attentionTaskStatus(a)}</p>
          )}
          <p>
            {a.task_context.has_current_artifact ? "当前版本已有交付物。" : "当前版本尚无交付物。"}
            {a.task_context.current_artifact_accepted
              ? "当前交付物已有通过验收的记录。"
              : "当前版本尚无通过验收的记录。"}
          </p>
          {a.task_id && <Link to={{ page: "task", id: a.task_id }}>查看执行与交付记录 →</Link>}
        </section>
      )}
      {(a.task_context?.goal || a.issue_intent) && (
        <section aria-label="事项目标">
          <h3>原本要完成什么</h3>
          <p>{a.task_context?.goal || a.issue_intent}</p>
        </section>
      )}
      <p className={`${styles.muted} ${styles.detailMeta}`}>
        <Badge>{kindLabel(a.kind)}</Badge> <span>·</span> <span>{statusLabel(a.status)}</span>
      </p>
      {a.kind === "decision" && (
        <>
          <h3>{a.status === "open" ? "需要你决定" : "原始问题与答复"}</h3>
          {currentFailure ? (
            <p>接下来如何推进这项任务？</p>
          ) : (
            <p>{a.decision ? question(a.decision).text : a.choice?.question || a.title}</p>
          )}
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
                <span>
                  <span>{a.decision ? optionLabel(a.decision, option) : option}</span>
                  {attentionOptionHelp(a, option) && (
                    <span className={styles.optionHelp}>{attentionOptionHelp(a, option)}</span>
                  )}
                </span>
              </label>
            ))}
          </div>
          <label>
            补充说明（可选）
            <Textarea
              className={styles.textarea}
              value={shownText}
              disabled={blocked}
              onChange={(e) => setText(e.target.value)}
            />
          </label>
        </>
      )}
      {a.kind === "action" && (
        <>
          <h3>{a.status === "open" ? "需要你处理" : "原始事项"}</h3>
          <p>{a.title}</p>
        </>
      )}
      {a.kind === "information" && <p>{a.title}</p>}
      {a.source_message_id && <AttentionOriginalMessage id={a.source_message_id} />}
      {a.kind === "action" && (
        <label>
          <h3>处理结果</h3>
          <Textarea
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
              <Button
                disabled={blocked || !!source.current}
                onClick={() => source.draft.removeFile(i)}
              >
                移除
              </Button>
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
              <Button onClick={() => void source.editRefused()}>取回后修改</Button>
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
      {a.status === "resolved" && recordedEffect && (
        <p
          role="status"
          className={recordedEffect.applied === true ? styles.muted : styles.warning}
        >
          {recordedEffect.applied === true
            ? "答复已记录，后续任务动作已安排；实际进度见当前结果。"
            : `答复已记录，任务动作未执行${typeof recordedEffect.reason === "string" ? `：${recordedEffect.reason}` : "。"}`}
        </p>
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
          <Button variant="primary" disabled={operation.busy} onClick={() => void run("reconcile")}>
            核实原提交结果
          </Button>
        </div>
      )}
      <div className={styles.actions}>
        {a.kind === "decision" && a.status === "open" && (
          <Button
            variant="primary"
            disabled={blocked || (!choice && !text.trim())}
            onClick={() => void run("respond")}
          >
            提交决定
          </Button>
        )}
        {a.kind === "action" && a.status === "open" && (
          <Button
            variant="primary"
            disabled={blocked || !shownText.trim() || !source.content.loaded}
            onClick={() => void run("confirm")}
          >
            提交处理结果
          </Button>
        )}
        <Button variant="secondary" onClick={onBack}>
          {a.status === "open" ? "稍后处理" : "返回列表"}
        </Button>
        <Button disabled={operation.busy || operation.pending} onClick={() => void run("read")}>
          {a.read_at ? "标记未读" : "标记已读"}
        </Button>
        {a.kind === "information" && (
          <Button
            disabled={operation.busy || operation.pending}
            onClick={() => void run("archive")}
          >
            {a.archived_at ? "展开" : "收起"}
          </Button>
        )}
      </div>
      <p className={styles.muted}>
        {a.status === "open"
          ? a.kind === "information"
            ? "这是一条通知，无需回复，不计入待处理数。"
            : "已读不会完成此项；实际执行动作以提交回执为准，答复将保留在全部记录中。"
          : "此项已结束，保留原问题和处理记录，无需再次提交。"}
      </p>
      <p className={styles.muted}>
        提出者：{actorLabel(a.raised_by)} · {new Date(a.created_at).toLocaleString("zh-CN")}
      </p>
      <details className={styles.timing}>
        <summary>原始问题、时间与提醒</summary>
        <p>{a.decision?.question || a.choice?.question || a.title}</p>
        <p className={styles.muted}>
          <AttentionActionTime item={a} />
        </p>
      </details>
    </>
  );
}

export function attentionContextTitle(item: AttentionView): string {
  return item.issue_title || (item.issue_id ? "所属 Issue 名称暂不可用" : "全局事项");
}

export function attentionFailure(
  item: AttentionView,
): { stage: string; reason: string } | undefined {
  if (!item.decision?.effects || !Object.keys(item.decision.effects).length) return undefined;
  const match = /^(实现|验收)失败（(.*)）。这个任务接下来怎么办？$/s.exec(
    question(item.decision).text,
  );
  return match ? { stage: match[1]!, reason: match[2]! } : undefined;
}

export function attentionTaskStatus(item: AttentionView): string {
  const task = item.task_context;
  if (!task) return "执行状态未提供";
  if (task.execution?.state === "waiting_owner" && task.block_reason === "waiting_decision")
    return "等待你的决定";
  return taskLook(task).label;
}

export function attentionOptionHelp(item: AttentionView, option: string): string {
  const effect = item.decision?.effects?.[option];
  if (effect === "implement")
    return "按当前任务要求再次执行，保留已有失败记录；仍受任务控制与次数限制。";
  if (effect === "cancel") return "取消这项执行任务，不等于关闭所属 Issue。";
  if (effect === "accept") return "对当前交付物重新发起独立验收。";
  if (
    option === "revise the spec" &&
    item.decision?.effects &&
    Object.keys(item.decision.effects).length
  ) {
    return "记录修改意图；请补充要改的内容。任务要求仍需后续修改，不会直接重新执行。";
  }
  return "";
}

export function attentionHeading(item: AttentionView): string {
  const text = item.decision ? question(item.decision).text : item.title;
  if (
    item.status === "open" &&
    item.decision?.effects?.["retry the implementation"] === "implement"
  ) {
    if (/^实现失败（.*）。这个任务接下来怎么办？$/s.test(text)) {
      return "实现失败，等待你决定下一步";
    }
  }
  return text;
}

function kindLabel(kind: string): string {
  return (
    ({ decision: "待决策", action: "待行动", information: "通知" } as Record<string, string>)[
      kind
    ] ?? kind
  );
}
function statusLabel(status: string): string {
  return (
    (
      { open: "待处理", resolved: "已处理", withdrawn: "已撤回", blocked: "受阻" } as Record<
        string,
        string
      >
    )[status] ?? status
  );
}
export function inboxSummary(item: AttentionView): string {
  const question = item.choice?.question || item.decision?.question || "";
  if (question.trim() && question.trim() === item.title?.trim()) return "";
  return (
    (question || item.choice?.reason || item.resolution || "")
      .split(/\r?\n/)
      .find((line) => line.trim()) ?? ""
  );
}
function InboxSummary({ item }: { item: AttentionView }) {
  const { client } = useServices();
  const read = useMemo(() => new WorkItemRead<IssueConversationItem>(client), [client]);
  const state = useStore(read.state);
  const summary = inboxSummary(item);
  useEffect(() => {
    if (!summary && item.source_message_id)
      void read.open(`/api/v1/messages/${encodeURIComponent(item.source_message_id)}`);
    return () => read.close();
  }, [read, summary, item.source_message_id]);
  const text =
    summary ||
    state.value?.body?.split(/\r?\n/).find((line) => line.trim()) ||
    (state.error ? "原始说明暂时无法读取" : state.loading ? "正在读取原始说明…" : "");
  if (!text) return null;
  return (
    <span className={styles.summary} title={text}>
      {text}
    </span>
  );
}
export function relativeTime(at: number, now: number): string {
  const minutes = Math.max(0, Math.floor((now - at) / 60_000));
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时前`;
  return `${Math.floor(minutes / 1440)} 天前`;
}
