import { useState } from "react";
import type {
  FollowupSpec,
  IssueView,
  IssueFollowupRule,
  IssueDelivery,
  WorkItemChoice,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { localTime, utcTime } from "./followupTime.ts";
import styles from "./Followups.module.css";

export function FollowupForm({
  item,
  rule,
  disabled,
  onCancel,
  onSave,
  deliveries,
  choices,
  initial,
}: {
  item: IssueView;
  rule: IssueFollowupRule | undefined;
  disabled: boolean | undefined;
  onCancel(): void;
  onSave(spec: FollowupSpec): Promise<void>;
  deliveries: readonly IssueDelivery[];
  choices: readonly WorkItemChoice[];
  initial?: FollowupSpec | undefined;
}) {
  const base = initial ?? rule;
  const [purpose, setPurpose] = useState(base?.purpose ?? "");
  const [instruction, setInstruction] = useState(base?.instruction ?? "");
  const [kind, setKind] = useState(base?.kind ?? "at");
  const [zone, setZone] = useState(
    base?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC",
  );
  const [at, setAt] = useState(() => localTime(base?.at ?? Date.now() + 3600000, zone));
  const [expiry, setExpiry] = useState(() =>
    localTime(base?.expires_at ?? Date.now() + 7 * 86400000, zone),
  );
  const [interval, setInterval] = useState(String((base?.every_seconds ?? 3600) / 60));
  const [max, setMax] = useState(String(base?.max_triggers ?? 20));
  const [condition, setCondition] = useState(base?.condition?.kind ?? "tasks_completed");
  const [tasks, setTasks] = useState(
    base?.condition?.task_ids ?? item.tasks?.map((t) => t.task_id) ?? [],
  );
  const [target, setTarget] = useState(
    base?.condition?.delivery_id ??
      base?.condition?.choice_id ??
      base?.condition?.decision_id ??
      "",
  );
  const [deliveryState, setDeliveryState] = useState(base?.condition?.delivery_state ?? "merged");
  const [silent, setSilent] = useState(base?.expiry_silent ?? false);
  const [followOn, setFollowOn] = useState(base?.follow_on_id ?? "");
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
              <option value="choice_answered">原选择已回答</option>
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
                    ? deliveries?.some((d) => d.delivery_id === target)
                    : condition === "choice_answered"
                      ? choices?.some(
                          (c) =>
                            c.choice_id === target &&
                            (c.status === "open" || c.status === "answered"),
                        )
                      : decisions.some((d) => d.decision_id === target)) && (
                    <option value={target}>原目标：{target}（当前列表未包含）</option>
                  )}
                {condition === "delivery_state"
                  ? deliveries?.map((d) => (
                      <option key={d.delivery_id} value={d.delivery_id}>
                        {d.url}
                      </option>
                    ))
                  : condition === "choice_answered"
                    ? choices
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
          {item.plan.follow_ons?.map((f) => (
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
