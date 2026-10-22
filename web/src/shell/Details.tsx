// A task's ledger facts and revision-checked owner controls (D70).
import { useState, type ReactNode } from "react";
import type { TaskDetail } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { useStore } from "../state/store.ts";
import { cancelTask, setControl } from "../tasks/actions.ts";
import { controlLabels, isFinished, ratingLabels, taskLook } from "../tasks/status.ts";
import controls from "./controls.module.css";
import styles from "./Details.module.css";
import { Back, Close } from "./icons.tsx";
import { StatusDot } from "./StatusDot.tsx";

export function Details({ onClose }: { onClose: () => void }) {
  const { live } = useServices();
  const state = useStore(live.detail);
  const task = state?.task;
  return (
    <>
      <div className={styles.head}>
        <button
          type="button"
          className={`${controls.iconBtn} ${styles.back}`}
          onClick={onClose}
          aria-label="返回"
        >
          <Back />
        </button>
        {task && <StatusDot tone={taskLook(task).tone} />}
        <span className={styles.title}>{task?.title ?? "任务详情"}</span>
        <button type="button" className={controls.iconBtn} onClick={onClose} aria-label="关闭">
          <Close />
        </button>
      </div>
      <div className={styles.body}>
        {state?.error && (
          <div role="alert">
            读取失败：{state.error}{" "}
            <button className={controls.link} onClick={() => void live.refreshDetail()}>
              重试
            </button>
          </div>
        )}
        {!task && !state?.error && (
          <p className={controls.empty}>
            {state ? "正在读取任务…" : "点任务行或回复里的任务标签，在这里查看任务。"}
          </p>
        )}
        {task && <TaskContent key={task.task_id} task={task} />}
        {task && (
          <Section title={state?.logs?.more ? "执行日志（尚未读到末尾）" : "最近日志"}>
            {!state?.logs ? (
              <p className={controls.empty}>
                {task.runs?.length
                  ? "正在读取日志，暂时无法读取时会随更新重试。"
                  : "尚未开始执行。"}
              </p>
            ) : (
              <>
                <small>
                  {state.logs.runId} · {state.logs.more ? "已读取" : "最近"}{" "}
                  {state.logs.entries.length} 条
                </small>
                {state.logs.more && (
                  <button className={controls.link} onClick={() => void live.refreshDetail()}>
                    继续读取较新日志
                  </button>
                )}
                <pre className={styles.logs}>
                  {state.logs.entries.length
                    ? state.logs.entries
                        .map((l) => `${l.created_at} ${l.kind}\n${text(l.body)}`)
                        .join("\n\n")
                    : "暂无日志。"}
                </pre>
              </>
            )}
          </Section>
        )}
      </div>
    </>
  );
}

function TaskContent({ task: t }: { task: TaskDetail }) {
  const s = useServices();
  const [busy, setBusy] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [result, setResult] = useState<string | null>(null);
  const execute = async (action: () => ReturnType<typeof cancelTask>) => {
    setBusy(true);
    try {
      const outcome = await action();
      setResult(
        outcome.kind === "done"
          ? "操作已执行"
          : outcome.kind === "stale"
            ? "任务已被修改，已刷新。请确认最新状态后再操作。"
            : outcome.kind === "unknown"
              ? outcome.message
              : `操作未执行：${outcome.message}`,
      );
    } finally {
      setBusy(false);
      setConfirmCancel(false);
    }
  };
  const spec = t.spec && typeof t.spec === "object" ? (t.spec as Record<string, unknown>) : null;
  return (
    <>
      <dl className={styles.kv}>
        <dt>状态</dt>
        <dd>
          {taskLook(t).label} · 第 {t.next_implement_generation} 代
        </dd>
        <dt>项目 / 评级</dt>
        <dd>
          {t.project || "未分组"} · {ratingLabels[t.rating]}
        </dd>
        <dt>控制</dt>
        <dd>
          {controlLabels[t.control_mode]} · 版本 {t.control_revision}
        </dd>
        <dt>任务 ID</dt>
        <dd>
          <code>{t.task_id}</code>
        </dd>
        <dt>工作区</dt>
        <dd>
          {t.workspace || "尚未分配"}
          {t.workspace_state &&
            ` · ${t.workspace_state.host} · ${workspaceLabels[t.workspace_state.status] ?? t.workspace_state.status}`}
        </dd>
      </dl>
      {t.workspace_state?.mounts?.map((m) => (
        <div className={styles.mount} key={`${m.repo}:${m.path}`}>
          <div>{m.repo}</div>
          <code>{m.path}</code>
          <div>
            分支 <code>{m.branch}</code>
          </div>
        </div>
      ))}
      <Section title={`目标与验收条件 · spec ${t.spec_version}`}>
        {typeof spec?.goal === "string" ? <Markdown text={spec.goal} /> : null}
        {spec ? (
          Object.entries(spec)
            .filter(([key]) => key !== "goal")
            .map(([key, value]) => (
              <div key={key}>
                <div className={styles.caption}>{specLabels[key] ?? key}</div>
                <SpecValue value={value} />
              </div>
            ))
        ) : (
          <pre className={styles.fact}>{text(t.spec)}</pre>
        )}
      </Section>
      <Section title="进展">
        {!(t.actions?.length || t.runs?.length) && <p className={controls.empty}>尚无执行记录。</p>}
        {t.actions?.map((a) => (
          <div className={styles.progress} key={a.action_id}>
            <b>
              {kindLabels[a.kind] ?? a.kind} · 第 {a.generation} 代
            </b>
            <span>{statusLabels[a.status] ?? a.status}</span>
            <small>
              {a.action_id} · spec {a.spec_version}
            </small>
          </div>
        ))}
        {t.runs?.map((r) => (
          <div className={styles.progress} key={r.run_id}>
            <b>
              {r.role === "implement" ? "实现" : "验收"} · {r.host}
            </b>
            <span>{statusLabels[r.observed] ?? r.observed}</span>
            <small>
              {r.run_id} · 第 {r.generation} 代
            </small>
          </div>
        ))}
      </Section>
      <Section title="产物与结论">
        {!t.artifacts?.length && <p className={controls.empty}>尚无产物。</p>}
        {t.artifacts?.map((a) => (
          <div key={a.artifact_id}>
            <small>{a.artifact_id}</small>
            <pre className={styles.fact}>{text(a.ref)}</pre>
            {t.acceptances
              ?.filter((v) => v.artifact_id === a.artifact_id)
              .map((v, index) => (
                <div className={styles.verdict} key={index}>
                  <b>{verdictLabels[v.verdict] ?? v.verdict}</b>
                  <pre className={styles.fact}>
                    {text({ issues: v.issues, evidence: v.evidence })}
                  </pre>
                </div>
              ))}
          </div>
        ))}
      </Section>
      {!isFinished(t) && (
        <div className={styles.actions}>
          {t.control_mode === "managed" && (
            <button
              disabled={busy}
              className={controls.chipBtn}
              onClick={() => void execute(() => setControl(s, t, "paused"))}
            >
              暂停
            </button>
          )}
          {t.control_mode !== "managed" && (
            <button
              disabled={busy}
              className={controls.chipBtn}
              onClick={() => void execute(() => setControl(s, t, "managed"))}
            >
              {t.control_mode === "human_control" ? "交还给 tend" : "恢复"}
            </button>
          )}
          {t.control_mode !== "human_control" && (
            <button
              disabled={busy}
              className={controls.chipBtn}
              onClick={() => void execute(() => setControl(s, t, "human_control"))}
            >
              接管
            </button>
          )}
          <button
            disabled={busy}
            className={`${controls.chipBtn} ${styles.danger}`}
            onClick={() => setConfirmCancel(true)}
          >
            取消任务
          </button>
          {confirmCancel && (
            <div className={styles.confirm}>
              <span>确认取消『{t.title}』？</span>
              <button
                disabled={busy}
                className={controls.chipBtn}
                onClick={() => void execute(() => cancelTask(s, t))}
              >
                确认取消
              </button>
              <button
                disabled={busy}
                className={controls.chipBtn}
                onClick={() => setConfirmCancel(false)}
              >
                继续任务
              </button>
            </div>
          )}
        </div>
      )}
      {result && (
        <p role="status" className={styles.caption}>
          {result}
        </p>
      )}
    </>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className={styles.section}>
      <h2>{title}</h2>
      {children}
    </section>
  );
}
function SpecValue({ value }: { value: unknown }) {
  if (Array.isArray(value) && value.every((item) => typeof item === "string")) {
    return (
      <ul className={styles.checks}>
        {value.map((item, i) => (
          <li key={i}>{item}</li>
        ))}
      </ul>
    );
  }
  return <pre className={styles.fact}>{value === "commit" ? "提交代码" : text(value)}</pre>;
}
function text(value: unknown): string {
  return typeof value === "string" ? value : (JSON.stringify(value, null, 2) ?? "—");
}
const workspaceLabels: Record<string, string> = {
  ready: "就绪",
  creating: "创建中",
  unavailable: "不可用",
  unknown: "未知",
};
const specLabels: Record<string, string> = {
  delivery: "交付方式",
  acceptance: "验收条件",
  constraints: "约束",
  context: "上下文",
  non_goals: "范围以外",
};
const kindLabels: Record<string, string> = { implement: "实现", accept: "验收", deliver: "交付" };
const statusLabels: Record<string, string> = {
  ready: "就绪",
  dispatched: "已派发",
  done: "已完成",
  superseded: "已替代",
  waiting: "等待中",
  interrupted: "已中断",
  pending: "待执行",
  running: "执行中",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  queued: "排队中",
  succeeded: "成功",
  accepted: "已接受",
  unknown: "状态未知",
  stopped: "已停止",
  created: "已创建",
  starting: "启动中",
  working: "工作中",
  disconnected: "连接已断开",
  finished: "已结束",
};
const verdictLabels: Record<string, string> = {
  passed: "验收通过",
  changes_required: "需要修改",
  blocked: "验收受阻",
  inconclusive: "验收无结论",
};
