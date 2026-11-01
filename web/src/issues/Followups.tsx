import { useEffect, useMemo, useState } from "react";
import type {
  BackgroundSourcePage,
  Envelope,
  FollowupSpec,
  IssueDeliveryView,
  IssueFollowupOccurrence,
  IssueFollowupRule,
  IssueView,
  WorkItemChoice,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { useDurableOp } from "../app/useDurableOp.ts";
import type { OpOutcome } from "../app/ops.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { createIssueFollowupCommand, issueFollowupCommand } from "./actions.ts";
import { FollowupForm } from "./FollowupForm.tsx";
import { followupDate } from "./followupTime.ts";
import styles from "./Followups.module.css";

const labels: Record<string, string> = {
  active: "等待触发",
  paused: "已暂停",
  finished: "调度已结束",
  cancelled: "已取消",
  queued: "待负责人处理",
  starting: "正在启动",
  processing: "正在检查",
  completed: "本轮结束",
  interrupted: "已中断",
  failed: "失败",
  unknown: "结果待核对",
  delivered: "已投递",
  at: "到时检查",
  every: "固定间隔",
  cron: "日历定时",
  condition: "条件满足时检查",
  manual: "手动检查",
  expired: "等待期限已到",
};

export function IssueFollowups({
  issue,
  deliveries,
}: {
  issue: IssueView;
  deliveries: readonly IssueDeliveryView[];
}) {
  const { client, live } = useServices();
  const connection = useStore(client.connection);
  const state = useStore(live.issues.followups.state);
  const history = useStore(live.issues.followupHistory.state);
  const context = useStore(live.issues.context.state);
  const operation = useDurableOp(client, `issue-followup-new:${issue.issue_id}`);
  const [creating, setCreating] = useState(false);
  const [creationSnapshot, setCreationSnapshot] = useState<IssueView | null>(null);
  const [restored, setRestored] = useState<Readonly<Envelope> | null>(null);
  const [result, setResult] = useState<OpOutcome | null>(null);
  const [selected, setSelected] = useState("");
  const [sourceMessage, setSourceMessage] = useState("");
  if (operation.original && operation.original.request_id !== restored?.request_id)
    setRestored(operation.original);
  useEffect(() => {
    void live.issues.openFollowups(issue.issue_id);
    void live.issues.openContext(issue.issue_id);
    return () => {
      live.issues.followups.close();
      live.issues.followupHistory.close();
      live.issues.context.close();
    };
  }, [live, issue.issue_id]);
  const choices = (context.value?.sources ?? []).flatMap((source) => source.choices ?? []);
  const writable =
    !!state.value?.enabled &&
    !state.error &&
    connection.status === "online" &&
    issue.assignment_state === "active" &&
    ["active", "paused"].includes(issue.state);
  const originalSpec = (restored?.payload as { spec?: FollowupSpec } | undefined)?.spec;
  const matchingHistory =
    history.value?.rule.issue_id === issue.issue_id && history.value.rule.rule_id === selected
      ? history.value
      : null;
  const showForm = creating || operation.pending || !!restored;
  return (
    <section className={styles.section} aria-label="跟进安排">
      <div className={styles.heading}>
        <h3>跟进安排</h3>
        <button
          disabled={!writable || operation.busy || operation.pending}
          onClick={() => {
            setCreationSnapshot(issue);
            setCreating(true);
          }}
        >
          新增安排
        </button>
      </div>
      <p className={styles.muted}>
        到期后由 Issue 负责人核对；安排触发或检查结束，不代表 Issue 已经完成。
      </p>
      {state.error && <p role="alert">无法读取跟进能力：{state.error}。当前不能新增或调整安排。</p>}
      {operation.storageError && <p role="alert">{operation.storageError}</p>}
      {result && result.kind !== "done" && <p role="status">{result.message}</p>}
      {operation.pending && (
        <p role="status">
          待核对原创建请求；正文与请求身份保留。
          <button
            disabled={operation.busy}
            onClick={async () => {
              const out = await operation.execute();
              setResult(out);
              if (out.kind === "done") {
                setRestored(null);
                setCreating(false);
                live.issues.refresh();
              }
            }}
          >
            核对结果
          </button>
        </p>
      )}
      {showForm && !operation.busy && (
        <FollowupForm
          item={issue}
          rule={undefined}
          initial={originalSpec}
          disabled={!writable || operation.pending}
          deliveries={deliveries.map((item) => item.delivery)}
          choices={choices}
          onCancel={() => {
            if (!operation.pending) {
              setCreating(false);
              setRestored(null);
            }
          }}
          onSave={async (spec) => {
            const command = restored
              ? { ...restored, payload: { ...(restored.payload as object), spec } }
              : createIssueFollowupCommand(creationSnapshot ?? issue, { spec });
            const out = await operation.execute(command);
            setResult(out);
            if (out.kind === "done") {
              setCreating(false);
              setRestored(null);
              live.issues.refresh();
            }
          }}
        />
      )}
      {(state.value?.items ?? []).map((rule) => (
        <Rule
          key={rule.rule_id}
          issue={issue}
          rule={rule}
          writable={writable}
          online={connection.status === "online"}
          deliveries={deliveries}
          choices={choices}
          onHistory={() => {
            setSelected(rule.rule_id);
            void live.issues.openFollowupHistory(issue.issue_id, rule.rule_id);
          }}
        />
      ))}
      {state.value && !state.value.items?.length && (
        <p className={styles.muted}>尚无跟进安排。任务完成等既有事件仍会即时通知负责人。</p>
      )}
      <div className={styles.actions}>
        <button onClick={() => void live.issues.openFollowups(issue.issue_id)}>第一页</button>
        {state.value?.more && (
          <button onClick={() => void live.issues.openFollowups(issue.issue_id, state.value?.next)}>
            下一页安排
          </button>
        )}
      </div>
      {selected && (
        <details open className={styles.history}>
          <summary>跟进执行历史</summary>
          {history.error ? (
            <p role="alert">历史读取失败：{history.error}</p>
          ) : matchingHistory ? (
            <>
              <p>调度状态和业务完成相互独立；静默检查保留在这里。</p>
              {foldChecks(matchingHistory.occurrences ?? []).map(({ entry, entries }) => (
                <div className={styles.rule} key={entry.occurrence_id}>
                  <p>
                    {followupDate(entry.created_at, matchingHistory.rule.timezone)} ·{" "}
                    {labels[entry.status] ?? entry.status} · {labels[entry.reason] ?? entry.reason}
                  </p>
                  {entry.checkin && (
                    <p>
                      静默核对：{entry.checkin.note}
                      {entries.length > 1 ? `（连续 ${entries.length} 次，展开来源可追溯）` : ""}
                    </p>
                  )}
                  <details>
                    <summary>证据与来源</summary>
                    <pre>{entry.evidence || "没有附加证据"}</pre>
                    <p>
                      规则修订 {entry.revision} · {entry.occurrence_id}
                    </p>
                    {entries.map((row) => (
                      <div key={row.occurrence_id}>
                        <p>
                          {followupDate(row.created_at, matchingHistory.rule.timezone)} ·{" "}
                          {row.occurrence_id}
                        </p>
                        <pre>{row.evidence}</pre>
                        {row.background_message_id && (
                          <button onClick={() => setSourceMessage(row.background_message_id!)}>
                            读取批次来源
                          </button>
                        )}
                      </div>
                    ))}
                  </details>
                </div>
              ))}
              <details>
                <summary>规则变更记录</summary>
                {matchingHistory.revisions?.map((rule) => (
                  <p key={rule.revision}>
                    修订 {rule.revision} · {labels[rule.status]} · {rule.reason} ·{" "}
                    {followupDate(rule.updated_at, rule.timezone)}
                  </p>
                ))}
              </details>
              {matchingHistory.more && (
                <button
                  onClick={() =>
                    void live.issues.openFollowupHistory(issue.issue_id, selected, {
                      ...(matchingHistory.next_revision === undefined
                        ? {}
                        : { afterRevision: matchingHistory.next_revision }),
                      ...(matchingHistory.next_occurrence
                        ? { afterOccurrence: matchingHistory.next_occurrence }
                        : {}),
                    })
                  }
                >
                  下一页历史
                </button>
              )}
            </>
          ) : (
            <p>正在读取历史…</p>
          )}
        </details>
      )}
      {sourceMessage && <BatchSources key={sourceMessage} message={sourceMessage} />}
    </section>
  );
}

function Rule({
  issue,
  rule,
  writable,
  online,
  deliveries,
  choices,
  onHistory,
}: {
  issue: IssueView;
  rule: IssueFollowupRule;
  writable: boolean;
  online: boolean;
  deliveries: readonly IssueDeliveryView[];
  choices: readonly WorkItemChoice[];
  onHistory(): void;
}) {
  const { client, live } = useServices();
  const operation = useDurableOp(client, `issue-followup:${rule.rule_id}`);
  const [editing, setEditing] = useState<IssueFollowupRule | null>(null);
  const [editingEpoch, setEditingEpoch] = useState(issue.assignment_epoch);
  const [reason, setReason] = useState("");
  const [restored, setRestored] = useState<Readonly<Envelope> | null>(null);
  const [result, setResult] = useState<OpOutcome | null>(null);
  if (
    operation.original &&
    operation.original.operation === "issues.followups_update" &&
    restored?.request_id !== operation.original.request_id
  )
    setRestored(operation.original);
  const run = async (verb: "pause" | "resume" | "cancel" | "trigger" | "reconcile") => {
    const out = await operation.execute(
      verb === "reconcile"
        ? undefined
        : {
            ...issueFollowupCommand(issue, rule, verb),
            reason: reason.trim() || `owner requested ${verb}`,
          },
    );
    setResult(out);
    if (out.kind === "done" || out.kind === "stale") live.issues.refresh();
  };
  const blocked = operation.busy || operation.pending || !!operation.storageError;
  const originalSpec = (restored?.payload as { spec?: FollowupSpec } | undefined)?.spec;
  return (
    <article className={styles.rule}>
      <div className={styles.heading}>
        <strong>{rule.purpose}</strong>
        <span>{labels[rule.status] ?? rule.status}</span>
      </div>
      <p>{rule.instruction}</p>
      <dl>
        <dt>等待</dt>
        <dd>
          {rule.kind === "condition"
            ? conditionText(rule)
            : rule.kind === "cron"
              ? `${rule.cron} · ${rule.timezone}`
              : (labels[rule.kind] ?? rule.kind)}
        </dd>
        <dt>{rule.kind === "condition" ? "最晚核对" : "下次核对"}</dt>
        <dd>{rule.status === "active" ? followupDate(rule.next_at, rule.timezone) : "—"}</dd>
        <dt>期限 / 次数</dt>
        <dd>
          {followupDate(rule.expires_at, rule.timezone)} · 已触发 {rule.trigger_count}
          {rule.kind === "every" || rule.kind === "cron" ? ` / ${rule.max_triggers}` : ""}
        </dd>
      </dl>
      {rule.reason && <p className={styles.muted}>{rule.reason}</p>}
      {rule.deadline_id ? (
        <p className={styles.muted}>此安排由时间约定管理。</p>
      ) : (
        <div className={styles.actions}>
          <button
            disabled={!writable || blocked || rule.status === "cancelled" || rule.kind === "cron"}
            title={rule.kind === "cron" ? "日历定时安排请通过 CLI 编辑" : undefined}
            onClick={() => {
              setEditingEpoch(issue.assignment_epoch);
              setEditing(rule);
            }}
          >
            编辑
          </button>
          {rule.status === "active" && (
            <button disabled={!online || blocked} onClick={() => void run("pause")}>
              暂停
            </button>
          )}
          {rule.status === "paused" && (
            <button disabled={!writable || blocked} onClick={() => void run("resume")}>
              恢复
            </button>
          )}
          <button
            disabled={!writable || blocked || rule.status === "cancelled"}
            onClick={() => void run("trigger")}
          >
            现在检查
          </button>
          <button
            disabled={
              !online ||
              blocked ||
              !["active", "paused"].includes(issue.state) ||
              rule.status === "cancelled"
            }
            onClick={() => void run("cancel")}
          >
            取消安排
          </button>
        </div>
      )}
      {!rule.deadline_id && (
        <label className={styles.field}>
          调整说明（可选）
          <input value={reason} onChange={(event) => setReason(event.target.value)} />
        </label>
      )}
      <button onClick={onHistory}>历史</button>
      {operation.pending && (
        <div className={styles.notice}>
          待核对操作：{operation.original?.operation}
          <button disabled={operation.busy || !online} onClick={() => void run("reconcile")}>
            核对结果
          </button>
          <button disabled={operation.busy || !online} onClick={() => void run("reconcile")}>
            重试原请求
          </button>
        </div>
      )}
      {(operation.storageError || (result && result.kind !== "done")) && (
        <p role="status">
          {operation.storageError || (result && result.kind !== "done" ? result.message : "")}
        </p>
      )}
      {(editing || restored) && !operation.busy && (
        <FollowupForm
          item={issue}
          rule={editing ?? rule}
          initial={originalSpec}
          disabled={!writable || operation.pending}
          deliveries={deliveries.map((item) => item.delivery)}
          choices={choices}
          onCancel={() => {
            if (!operation.pending) {
              setEditing(null);
              setRestored(null);
            }
          }}
          onSave={async (spec) => {
            const command = restored
              ? { ...restored, payload: { ...(restored.payload as object), spec } }
              : {
                  ...issueFollowupCommand({ assignment_epoch: editingEpoch }, editing!, "update", {
                    spec,
                  }),
                  reason: reason.trim() || "owner adjusted followup",
                };
            const out = await operation.execute(command);
            setResult(out);
            if (out.kind === "done") {
              setEditing(null);
              setRestored(null);
              live.issues.refresh();
            }
          }}
        />
      )}
    </article>
  );
}
function conditionText(rule: FollowupSpec) {
  const c = rule.condition;
  if (!c) return "";
  return c.kind === "tasks_completed"
    ? `固定 ${c.task_ids?.length ?? 0} 个任务全部完成`
    : c.kind === "delivery_state"
      ? `关联交付${c.delivery_state === "merged" ? "合入" : "关闭"}`
      : c.kind === "decision_answered"
        ? "关联决策已回答"
        : "关联选择已回答";
}
function foldChecks(entries: IssueFollowupOccurrence[]) {
  const out: { entry: IssueFollowupOccurrence; entries: IssueFollowupOccurrence[] }[] = [];
  for (const entry of entries) {
    const last = out.at(-1);
    if (
      entry.checkin &&
      last?.entry.checkin?.note === entry.checkin.note &&
      last.entry.status === entry.status
    )
      last.entries.push(entry);
    else out.push({ entry, entries: [entry] });
  }
  return out;
}
function BatchSources({ message }: { message: string }) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<BackgroundSourcePage>(client), [client]);
  const state = useStore(read.state);
  const path = `/api/v1/background/${encodeURIComponent(message)}/sources`;
  useEffect(() => {
    void read.open(path);
    const refresh = () => void read.refresh();
    const a = live.snapshot.subscribe(refresh),
      b = live.conversation.subscribe(refresh);
    return () => {
      a();
      b();
      read.close();
    };
  }, [live, read, path]);
  return (
    <details open>
      <summary>批次原始来源</summary>
      {state.error && <p role="alert">来源读取失败：{state.error}</p>}
      {state.value?.sources?.map((source) => (
        <article className={styles.rule} key={source.seq}>
          <p>
            {source.cancelled_at ? "已撤销来源，仅作历史参考" : "留存来源"} · {source.kind}
          </p>
          <pre className={styles.sources}>{source.body}</pre>
          <details>
            <summary>准确事实与身份</summary>
            <p>
              {source.event_id} · {source.message_id}
            </p>
            <pre className={styles.sources}>{JSON.stringify(source.data, null, 2)}</pre>
          </details>
        </article>
      ))}
      {state.value?.more && (
        <button onClick={() => void read.open(`${path}?after=${state.value!.next}`)}>
          下一页来源
        </button>
      )}
    </details>
  );
}
