import { useEffect, useMemo, useState } from "react";
import type { Connection } from "../api/client.ts";
import type {
  FollowupOccurrence,
  FollowupRule,
  FollowupSpec,
  WorkItemView,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { FollowupActions, followupStorage } from "./followupActions.ts";
import { followupDate, localTime, utcTime } from "./followupTime.ts";
import controls from "../shell/controls.module.css";
import styles from "./Followups.module.css";

const labels: Record<string, string> = {
  active: "等待触发",
  paused: "已暂停",
  finished: "调度已结束",
  cancelled: "已取消",
  queued: "待主 Agent 处理",
  starting: "正在启动",
  processing: "正在检查",
  completed: "本轮结束",
  interrupted: "已中断",
  failed: "失败",
  unknown: "结果待核对",
  delivered: "已投递",
  at: "到时检查",
  every: "固定间隔",
  condition: "条件满足时检查",
  manual: "手动检查",
  expired: "等待期限已到",
};
export function Followups({ item, connection }: { item: WorkItemView; connection: Connection }) {
  const { client, live } = useServices();
  const source = live.workItems.followups;
  const state = useStore(source.state);
  const history = useStore(live.workItems.followupHistory.state);
  const [editing, setEditing] = useState<FollowupRule | "new" | null>(null);
  const [reason, setReason] = useState("");
  const [sourceMessage, setSourceMessage] = useState<string | null>(null);
  const action = useMemo(
    () =>
      new FollowupActions(client, item.work_item_id, followupStorage(), () =>
        live.workItems.refresh(),
      ),
    [client, item.work_item_id, live],
  );
  const progress = useStore(action.state);
  useEffect(() => {
    void source.open(`/api/v1/work-items/${encodeURIComponent(item.work_item_id)}/followups`);
    const update = () => action.reload();
    window.addEventListener("storage", update);
    return () => {
      source.close();
      live.workItems.followupHistory.close();
      window.removeEventListener("storage", update);
    };
  }, [source, item.work_item_id, action, live]);
  const writable =
    state.value?.enabled &&
    !state.error &&
    item.status !== "closed" &&
    connection.status === "online" &&
    !progress.busy &&
    !progress.pending.length;
  const control = (r: FollowupRule, verb: string) =>
    action.execute({
      operation: `work_items.followups_${verb}`,
      target: { type: "followup", id: r.rule_id },
      expected_control_revision: r.revision,
      reason: reason.trim() || `owner requested ${verb}`,
      payload: {},
    });
  const openHistory = (id: string, rev = 0, occ = "") =>
    live.workItems.followupHistory.open(
      `/api/v1/followups/${encodeURIComponent(id)}/history?after_revision=${rev}&after_occurrence=${encodeURIComponent(occ)}`,
    );
  return (
    <section className={styles.section} aria-label="跟进安排">
      <div className={styles.heading}>
        <h3>跟进安排</h3>
        <button className={controls.button} disabled={!writable} onClick={() => setEditing("new")}>
          新增安排
        </button>
      </div>
      <p className={styles.muted}>
        到期后由主 Agent 核对；安排触发或检查结束，不代表工作项已经完成。
      </p>
      {state.error && <p role="alert">无法读取跟进能力：{state.error}。当前不能新增或调整安排。</p>}
      {state.value && !state.value.enabled && (
        <p role="status">托管主 Agent 未启用；保留历史，可暂停或取消既有安排。</p>
      )}
      {progress.message && <p role="status">{progress.message}</p>}
      {progress.pending.map((e) => (
        <div key={e.request_id} className={styles.notice}>
          待核对操作：{e.operation}
          <div className={styles.actions}>
            <button disabled={progress.busy} onClick={() => void action.check(e)}>
              核对结果
            </button>
            <button disabled={progress.busy} onClick={() => void action.retry(e)}>
              重试原请求
            </button>
          </div>
        </div>
      ))}
      <label className={styles.field}>
        调整说明（可选）
        <input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="为什么调整这项安排"
        />
      </label>
      {(state.value?.items ?? []).map((r) => (
        <article className={styles.rule} key={r.rule_id}>
          <div className={styles.heading}>
            <strong>{r.purpose}</strong>
            <span>{labels[r.status] ?? r.status}</span>
          </div>
          <p>{r.instruction}</p>
          <dl>
            <dt>等待</dt>
            <dd>{r.kind === "condition" ? conditionText(r) : labels[r.kind]}</dd>
            <dt>{r.kind === "condition" ? "最晚核对" : "下次核对"}</dt>
            <dd>{r.status === "active" ? followupDate(r.next_at, r.timezone) : "—"}</dd>
            <dt>期限 / 次数</dt>
            <dd>
              {followupDate(r.expires_at, r.timezone)} · 已触发 {r.trigger_count}
              {r.kind === "every" ? ` / ${r.max_triggers}` : ""}
            </dd>
          </dl>
          {r.reason && <p className={styles.muted}>{r.reason}</p>}
          {state.value?.latest?.[r.rule_id] && (
            <p className={styles.muted}>
              最近检查：
              {labels[state.value.latest[r.rule_id]!.status] ??
                state.value.latest[r.rule_id]!.status}{" "}
              ·{" "}
              {state.value.latest[r.rule_id]!.checkin?.note ??
                followupDate(state.value.latest[r.rule_id]!.created_at, r.timezone)}
            </p>
          )}
          {r.follow_on_id && (
            <p className={styles.muted}>
              关联后续：
              {item.follow_ons?.find((x) => x.follow_on_id === r.follow_on_id)?.text ??
                r.follow_on_id}
            </p>
          )}
          <div className={styles.actions}>
            <button disabled={!writable || r.status === "cancelled"} onClick={() => setEditing(r)}>
              编辑
            </button>
            {r.status === "active" && (
              <button
                disabled={
                  connection.status !== "online" || progress.busy || !!progress.pending.length
                }
                onClick={() => void control(r, "pause")}
              >
                暂停
              </button>
            )}
            {r.status === "paused" && (
              <button disabled={!writable} onClick={() => void control(r, "resume")}>
                恢复
              </button>
            )}
            <button
              disabled={!writable || r.status === "cancelled"}
              onClick={() => void control(r, "trigger")}
            >
              现在检查
            </button>
            <button
              disabled={
                connection.status !== "online" ||
                progress.busy ||
                !!progress.pending.length ||
                r.status === "cancelled" ||
                item.status === "closed"
              }
              onClick={() => void control(r, "cancel")}
            >
              取消安排
            </button>
            <button onClick={() => void openHistory(r.rule_id)}>历史</button>
          </div>
        </article>
      ))}
      {!state.loading && !state.value?.items?.length && !state.error && (
        <p className={styles.muted}>尚无跟进安排。任务完成等既有事件仍会即时通知主 Agent。</p>
      )}
      <div className={styles.actions}>
        <button
          onClick={() =>
            void source.open(
              `/api/v1/work-items/${encodeURIComponent(item.work_item_id)}/followups`,
            )
          }
        >
          第一页
        </button>
        {state.value?.more && (
          <button
            onClick={() =>
              void source.open(
                `/api/v1/work-items/${encodeURIComponent(item.work_item_id)}/followups?after=${encodeURIComponent(state.value?.next ?? "")}`,
              )
            }
          >
            下一页安排
          </button>
        )}
      </div>
      {editing && (
        <FollowupForm
          key={editing === "new" ? "new" : `${editing.rule_id}:${editing.revision}`}
          item={item}
          rule={editing === "new" ? undefined : editing}
          disabled={!writable}
          onCancel={() => setEditing(null)}
          onSave={async (spec) => {
            const saved = await action.execute({
              operation: `work_items.followups_${editing === "new" ? "create" : "update"}`,
              target: {
                type: editing === "new" ? "work_item" : "followup",
                id: editing === "new" ? item.work_item_id : editing.rule_id,
              },
              expected_control_revision: editing === "new" ? item.revision : editing.revision,
              reason: reason.trim() || "owner adjusted followup",
              payload: editing === "new" ? spec : { spec },
            });
            if (saved) setEditing(null);
          }}
        />
      )}
      {history.error && <p role="alert">历史读取失败：{history.error}</p>}
      {history.value && (
        <details open className={styles.history}>
          <summary>{history.value.rule.purpose} · 执行历史</summary>
          <p>调度状态和业务完成相互独立；静默检查保留在这里。</p>
          {foldChecks(history.value.occurrences ?? []).map(({ entry: o, count, entries }) => (
            <div className={styles.rule} key={o.occurrence_id}>
              <p>
                {followupDate(o.created_at, history.value?.rule.timezone)} ·{" "}
                {labels[o.status] ?? o.status} · {labels[o.reason] ?? o.reason}
              </p>
              {o.checkin && (
                <p>
                  静默核对：{o.checkin.note}
                  {count > 1 ? `（连续 ${count} 次，展开来源可追溯）` : ""}
                </p>
              )}
              <details>
                <summary>证据与来源</summary>
                <pre>{o.evidence || "没有附加证据"}</pre>
                <p>
                  规则修订 {o.revision} · {o.occurrence_id}
                </p>
                {entries.map((entry) => (
                  <div key={entry.occurrence_id}>
                    <p>
                      {followupDate(entry.created_at, history.value?.rule.timezone)} ·{" "}
                      {entry.occurrence_id}
                    </p>
                    <pre>{entry.evidence}</pre>
                    {entry.background_message_id && (
                      <button onClick={() => setSourceMessage(entry.background_message_id!)}>
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
            {history.value.revisions?.map((r) => (
              <p key={r.revision}>
                修订 {r.revision} · {labels[r.status]} · {r.reason} ·{" "}
                {followupDate(r.updated_at, r.timezone)}
              </p>
            ))}
          </details>
          {(history.value.next_revision || history.value.next_occurrence) && (
            <button
              onClick={() =>
                void openHistory(
                  history.value!.rule.rule_id,
                  history.value!.next_revision ?? history.value!.revisions?.at(-1)?.revision ?? 0,
                  history.value!.next_occurrence ??
                    history.value!.occurrences?.at(-1)?.occurrence_id ??
                    "",
                )
              }
            >
              下一页历史
            </button>
          )}
        </details>
      )}
      {sourceMessage && <BatchSources key={sourceMessage} message={sourceMessage} />}
    </section>
  );
}
function conditionText(r: FollowupSpec) {
  const c = r.condition;
  if (!c) return "";
  return c.kind === "tasks_completed"
    ? `固定 ${c.task_ids?.length ?? 0} 个任务全部完成`
    : c.kind === "delivery_state"
      ? `交付 ${c.delivery_id} ${c.delivery_state === "merged" ? "合入" : "关闭"}`
      : c.kind === "decision_answered"
        ? `待决 ${c.decision_id} 已回答`
        : `选择 ${c.choice_id} 已回答`;
}
function foldChecks(entries: FollowupOccurrence[]) {
  const out: { entry: (typeof entries)[number]; count: number; entries: FollowupOccurrence[] }[] =
    [];
  for (const entry of entries) {
    const last = out.at(-1);
    if (
      entry.checkin &&
      last?.entry.checkin?.note === entry.checkin.note &&
      last.entry.status === entry.status
    ) {
      last.count++;
      last.entries.push(entry);
    } else out.push({ entry, count: 1, entries: [entry] });
  }
  return out;
}

function FollowupForm({
  item,
  rule,
  disabled,
  onCancel,
  onSave,
}: {
  item: WorkItemView;
  rule: FollowupRule | undefined;
  disabled: boolean | undefined;
  onCancel(): void;
  onSave(spec: FollowupSpec): Promise<void>;
}) {
  const [purpose, setPurpose] = useState(rule?.purpose ?? "");
  const [instruction, setInstruction] = useState(rule?.instruction ?? "");
  const [kind, setKind] = useState(rule?.kind ?? "at");
  const [zone, setZone] = useState(
    rule?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC",
  );
  const [at, setAt] = useState(() => localTime(rule?.at ?? Date.now() + 3600000, zone));
  const [expiry, setExpiry] = useState(() =>
    localTime(rule?.expires_at ?? Date.now() + 7 * 86400000, zone),
  );
  const [interval, setInterval] = useState(String((rule?.every_seconds ?? 3600) / 60));
  const [max, setMax] = useState(String(rule?.max_triggers ?? 20));
  const [condition, setCondition] = useState(rule?.condition?.kind ?? "tasks_completed");
  const [tasks, setTasks] = useState(
    rule?.condition?.task_ids ?? item.tasks?.map((t) => t.task_id) ?? [],
  );
  const [target, setTarget] = useState(
    rule?.condition?.delivery_id ??
      rule?.condition?.choice_id ??
      rule?.condition?.decision_id ??
      "",
  );
  const [deliveryState, setDeliveryState] = useState(rule?.condition?.delivery_state ?? "merged");
  const [silent, setSilent] = useState(rule?.expiry_silent ?? false);
  const [followOn, setFollowOn] = useState(rule?.follow_on_id ?? "");
  const [error, setError] = useState("");
  const { live } = useServices();
  const snap = useStore(live.snapshot);
  const decisions =
    snap?.decisions?.filter((d) => item.tasks?.some((t) => t.task_id === d.task_id)) ?? [];
  return (
    <form
      className={styles.form}
      aria-label={rule ? "编辑跟进安排" : "新增跟进安排"}
      onSubmit={(e) => {
        e.preventDefault();
        setError("");
        try {
          const spec: FollowupSpec = {
            purpose: purpose.trim(),
            instruction: instruction.trim(),
            kind,
            timezone: zone,
            ...(followOn ? { follow_on_id: followOn } : {}),
          };
          if (kind === "at") spec.at = utcTime(at, zone);
          else {
            spec.expires_at = utcTime(expiry, zone);
            if (kind === "every") {
              spec.every_seconds = Number(interval) * 60;
              spec.max_triggers = Number(max);
              if (
                !Number.isInteger(spec.every_seconds) ||
                spec.every_seconds < 300 ||
                !Number.isInteger(spec.max_triggers) ||
                spec.max_triggers < 1
              )
                throw new Error("间隔至少 5 分钟，次数须为正整数");
            } else {
              spec.expiry_silent = silent;
              spec.condition =
                condition === "tasks_completed"
                  ? { kind: condition, task_ids: tasks }
                  : condition === "delivery_state"
                    ? { kind: condition, delivery_id: target, delivery_state: deliveryState }
                    : condition === "decision_answered"
                      ? { kind: condition, decision_id: target }
                      : { kind: condition, choice_id: target };
            }
          }
          void onSave(spec);
        } catch (err) {
          setError(err instanceof Error ? err.message : String(err));
        }
      }}
    >
      <h4>{rule ? "编辑安排" : "新增安排"}</h4>
      <label className={styles.field}>
        目的
        <input
          required
          maxLength={500}
          value={purpose}
          onChange={(e) => setPurpose(e.target.value)}
        />
      </label>
      <label className={styles.field}>
        检查内容
        <textarea
          required
          maxLength={8000}
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
        />
      </label>
      <label className={styles.field}>
        触发方式
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="at">指定时间一次</option>
          <option value="every">固定间隔</option>
          <option value="condition">账本条件</option>
        </select>
      </label>
      <label className={styles.field}>
        IANA 时区
        <input
          required
          value={zone}
          onChange={(e) => setZone(e.target.value)}
          placeholder="Asia/Shanghai 或 UTC"
        />
      </label>
      {kind === "at" ? (
        <label className={styles.field}>
          核对时间
          <input
            required
            type="datetime-local"
            value={at}
            onChange={(e) => setAt(e.target.value)}
          />
        </label>
      ) : (
        <label className={styles.field}>
          等待期限
          <input
            required
            type="datetime-local"
            value={expiry}
            onChange={(e) => setExpiry(e.target.value)}
          />
        </label>
      )}
      {kind === "every" && (
        <>
          <label className={styles.field}>
            固定间隔（分钟，非当地每日时间）
            <input
              type="number"
              min={5}
              value={interval}
              onChange={(e) => setInterval(e.target.value)}
            />
          </label>
          <label className={styles.field}>
            累计触发上限
            <input
              type="number"
              min={1}
              max={1000}
              value={max}
              onChange={(e) => setMax(e.target.value)}
            />
          </label>
        </>
      )}
      {kind === "condition" && (
        <>
          <label className={styles.field}>
            等待条件
            <select
              value={condition}
              onChange={(e) => {
                setCondition(e.target.value);
                setTarget("");
              }}
            >
              <option value="tasks_completed">固定任务全部完成</option>
              <option value="delivery_state">交付状态</option>
              <option value="decision_answered">任务待决已回答</option>
              <option value="choice_answered">工作项选择已回答</option>
            </select>
          </label>
          {condition === "tasks_completed" ? (
            <fieldset>
              <legend>登记时固定的任务集合</legend>
              {item.tasks?.map((t) => (
                <label className={styles.check} key={t.task_id}>
                  <input
                    type="checkbox"
                    checked={tasks.includes(t.task_id)}
                    onChange={(e) =>
                      setTasks(
                        e.target.checked
                          ? [...tasks, t.task_id]
                          : tasks.filter((id) => id !== t.task_id),
                      )
                    }
                  />
                  {t.title}
                </label>
              ))}
            </fieldset>
          ) : (
            <label className={styles.field}>
              目标
              <select required value={target} onChange={(e) => setTarget(e.target.value)}>
                <option value="">请选择</option>
                {target &&
                  !(condition === "delivery_state"
                    ? item.deliveries?.some((d) => d.delivery_id === target)
                    : condition === "choice_answered"
                      ? item.choices?.some(
                          (c) =>
                            c.choice_id === target &&
                            (c.status === "open" || c.status === "answered"),
                        )
                      : decisions.some((d) => d.decision_id === target)) && (
                    <option value={target}>原目标：{target}（当前列表未包含）</option>
                  )}
                {condition === "delivery_state"
                  ? item.deliveries?.map((d) => (
                      <option key={d.delivery_id} value={d.delivery_id}>
                        {d.url}
                      </option>
                    ))
                  : condition === "choice_answered"
                    ? item.choices
                        ?.filter((c) => c.status === "open" || c.status === "answered")
                        .map((c) => (
                          <option key={c.choice_id} value={c.choice_id}>
                            {c.question}
                          </option>
                        ))
                    : decisions.map((d) => (
                        <option key={d.decision_id} value={d.decision_id}>
                          {d.question}
                        </option>
                      ))}
              </select>
            </label>
          )}
          {condition === "delivery_state" && (
            <label className={styles.field}>
              目标状态
              <select value={deliveryState} onChange={(e) => setDeliveryState(e.target.value)}>
                <option value="merged">已合入</option>
                <option value="closed">已关闭</option>
              </select>
            </label>
          )}
          <label className={styles.check}>
            <input type="checkbox" checked={silent} onChange={(e) => setSilent(e.target.checked)} />
            期限到达仍未满足时静默结束安排
          </label>
        </>
      )}
      <label className={styles.field}>
        关联后续事项
        <select value={followOn} onChange={(e) => setFollowOn(e.target.value)}>
          <option value="">无</option>
          {item.follow_ons?.map((f) => (
            <option key={f.follow_on_id} value={f.follow_on_id}>
              {f.text}
            </option>
          ))}
        </select>
      </label>
      {error && <p role="alert">{error}</p>}
      <div className={styles.actions}>
        <button type="submit" disabled={disabled}>
          保存安排
        </button>
        <button type="button" onClick={onCancel}>
          放弃编辑
        </button>
      </div>
    </form>
  );
}

function BatchSources({ message }: { message: string }) {
  const { live } = useServices();
  const read = live.workItems.backgroundSources;
  const state = useStore(read.state);
  const path = `/api/v1/background/${encodeURIComponent(message)}/sources`;
  useEffect(() => {
    void read.open(path);
    return () => read.close();
  }, [read, path]);
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
