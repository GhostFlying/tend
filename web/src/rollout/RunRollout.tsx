import { LiveStatus } from "../ui/LiveStatus.tsx";
import { runActivityStatus } from "../ui/activityStatus.ts";
import { navigate, useRoute } from "../app/router.ts";
import { Menu } from "../ui/Menu.tsx";
import { runLabel } from "../tasks/runLabel.ts";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import type { TaskDetail, RunView } from "../api/generated/api.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { UsagePanel } from "../usage/Usage.tsx";
import { buildSteps, type Step } from "./model.ts";
import { useRunWindow } from "./useRunWindow.ts";
import styles from "./RunRollout.module.css";

export function TaskRunStatus({ id, entryOnly = false }: { id: string; entryOnly?: boolean }) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<TaskDetail>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => {
    void read.open(`/api/v1/tasks/${encodeURIComponent(id)}`);
    const stop = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      read.close();
    };
  }, [id, read, live]);
  const runs = state.value?.runs;
  return runs?.length ? <RunRollout runs={runs} taskID={id} entryOnly={entryOnly} /> : null;
}

export function RunStatusLine({ id, observed }: { id: string; observed: string }) {
  return <RunStatusWindow key={id} id={id} observed={observed} />;
}

function RunStatusWindow({ id, observed }: { id: string; observed: string }) {
  const window = useRunWindow(id);
  return <LiveStatus status={runActivityStatus(window.page?.logs ?? [], observed)} />;
}

export function RunRollout({
  runs,
  taskID,
  entryOnly = false,
}: {
  runs: readonly RunView[];
  taskID: string;
  entryOnly?: boolean;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  const run = runs.find((candidate) => candidate.run_id === selected) ?? runs.at(-1);
  return (
    <section className={entryOnly ? styles.entryOnly : styles.root} aria-label="执行过程与用量">
      {!entryOnly && (
        <div className={styles.cardHeading}>
          <span>执行过程</span>
          {run && (
            <Menu
              label="执行记录"
              variant="ghost"
              size="s"
              items={runs.map((item) => ({
                id: item.run_id,
                label: runLabel(runs, item),
                onSelect: () =>
                  setSelected(item.run_id === runs.at(-1)?.run_id ? null : item.run_id),
              }))}
            >
              {runLabel(runs, run)}{" "}
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden>
                <path d="m4 6 4 4 4-4" stroke="currentColor" strokeWidth="1.5" />
              </svg>
            </Menu>
          )}
        </div>
      )}
      {run ? (
        <RunWindow key={run.run_id} run={run} taskID={taskID} entryOnly={entryOnly} />
      ) : (
        <p className={styles.empty}>尚未开始执行。</p>
      )}
    </section>
  );
}

function RunWindow({
  run,
  taskID,
  entryOnly,
}: {
  run: RunView;
  taskID: string;
  entryOnly: boolean;
}) {
  const route = useRoute();
  const window = useRunWindow(run.run_id);
  const steps = useMemo(() => buildSteps(window.page?.logs ?? []), [window.page]);
  const [expanded, setExpanded] = useState(false);
  const [visible, setVisible] = useState(6);
  const [usage, setUsage] = useState(false);
  const regionID = useId();
  const toggle = useRef<HTMLButtonElement>(null);
  const observed = run.observed;
  const active = observed === "running" || observed === "starting";
  const status = runActivityStatus(window.page?.logs ?? [], observed);
  const wasActive = useRef(active);
  useLayoutEffect(() => {
    if (wasActive.current && !active) {
      if (document.getElementById(regionID)?.contains(document.activeElement))
        toggle.current?.focus({ preventScroll: true });
      setExpanded(false);
    }
    wasActive.current = active;
  }, [active, regionID]);
  const open = (step?: Step) => {
    const from = route.page === "issue" ? route.id : route.page === "task" ? route.from : undefined;
    navigate({
      page: "rollout",
      id: taskID,
      run: run.run_id,
      ...(from ? { from } : {}),
      ...(step ? { record: String(step.lastSeq), source: "logs" as const } : {}),
    });
  };
  return (
    <>
      <div className={entryOnly ? styles.shortcut : styles.cardActions}>
        {!entryOnly && (
          <span>
            {run.observed === "failed" ? "本轮执行失败" : active ? "正在执行" : "执行记录已保留"}
          </span>
        )}
        <button className={styles.textButton} onClick={() => open()}>
          {" "}
          {entryOnly ? "查看过程" : "查看完整过程"} <span aria-hidden>↗</span>
        </button>
      </div>
      {!entryOnly && (
        <button
          ref={toggle}
          className={styles.activity}
          aria-label="展开执行步骤"
          aria-expanded={expanded}
          aria-controls={expanded ? regionID : undefined}
          onClick={() => setExpanded(!expanded)}
        >
          <span className={styles.activityText}>
            <LiveStatus status={status} />
            {active && <span className={styles.summary}>{status.steps} 个步骤</span>}
          </span>
          <span className={styles.chevron} data-open={expanded} aria-hidden>
            ›
          </span>
        </button>
      )}
      {!entryOnly && window.error && (
        <p className={styles.error} role="alert">
          过程暂时无法更新，已保留当前内容。<button onClick={window.reconnect}>重试</button>
        </p>
      )}
      {!entryOnly && expanded && (
        <div id={regionID} className={styles.compact}>
          {steps.length > visible && (
            <button className={styles.textButton} onClick={() => setVisible((count) => count + 6)}>
              显示更早步骤
            </button>
          )}
          {!steps.length && (
            <p className={styles.empty}>{window.loading ? "正在读取过程…" : "尚无已采集记录。"}</p>
          )}
          <ol className={styles.steps}>
            {steps.slice(-visible).map((step) => (
              <li key={step.key}>
                <StepView step={step} compact active={active} inspect={() => open(step)} />
              </li>
            ))}
          </ol>
          <div className={styles.cardFooter}>
            <button
              className={styles.textButton}
              aria-expanded={usage}
              onClick={() => setUsage(!usage)}
            >
              本次用量
            </button>
          </div>
          {usage && <UsagePanel subject={`run:${run.run_id}`} />}
        </div>
      )}
    </>
  );
}

function StepIcon({ step, active }: { step: Step; active: boolean }) {
  const type = step.kind === "tool" ? step.title : step.kind;
  let path = (
    <>
      <path d="M4 5h8M4 8h6M4 11h4" />
      <path d="M11 11l2 2 3-4" />
    </>
  );
  if (type === "执行命令")
    path = (
      <>
        <path d="m3 4 4 4-4 4M9 13h5" />
      </>
    );
  else if (type === "搜索")
    path = (
      <>
        <circle cx="7" cy="7" r="4.5" />
        <path d="m10.5 10.5 4 4" />
      </>
    );
  else if (type === "修改文件")
    path = (
      <>
        <path d="m10 3 3 3-7.5 7.5H2.5v-3zM9 4l3 3M10 14h4" />
      </>
    );
  else if (type === "读取文件" || type === "查看图片")
    path = (
      <>
        <path d="M9.5 2H4a1 1 0 0 0-1 1v11a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V5.5zM9 2v4h4" />
      </>
    );
  return (
    <span
      className={styles.mark}
      data-active={active && step.pending}
      data-failed={step.failed}
      aria-hidden
    >
      {step.failed ? (
        <svg viewBox="0 0 16 16">
          <circle cx="8" cy="8" r="6" />
          <path d="m6 6 4 4m0-4-4 4" />
        </svg>
      ) : (
        <svg viewBox="0 0 16 16">{path}</svg>
      )}
    </span>
  );
}
function StepTime({ step }: { step: Step | undefined }) {
  if (step?.durationMs === null || step?.durationMs === undefined) return null;
  const ms = step.durationMs;
  const seconds = Math.floor(ms / 1000);
  return (
    <span className={styles.elapsed} title="执行器报告的步骤耗时">
      {ms < 1000
        ? `${ms}毫秒`
        : `${seconds >= 60 ? `${Math.floor(seconds / 60)}分` : ""}${seconds % 60}秒`}
    </span>
  );
}

function StepView({
  step,
  compact = false,
  reveal = false,
  rawOpen = false,
  active,
  inspect,
}: {
  step: Step;
  compact?: boolean;
  reveal?: boolean;
  rawOpen?: boolean;
  active: boolean;
  inspect?: () => void;
}) {
  const warning = step.truncated && (
    <p className={styles.warning}>内容已截断，原始完整输出未被采集。</p>
  );
  return (
    <article className={styles.step} data-kind={step.kind} data-failed={step.failed}>
      {step.gap && (
        <p className={styles.warning} role="status">
          此处存在记录缺口。
        </p>
      )}
      {step.kind === "text" && !compact ? (
        <div className={styles.prose}>
          <Markdown
            text={
              step.text ||
              (active && step.pending ? "正在等待 Agent 回复…" : "未采集到 Agent 回复正文。")
            }
          />
          {warning}
        </div>
      ) : (
        <details className={styles.disclosure} data-disclosure="content" open={reveal || undefined}>
          <summary>
            <StepIcon step={step} active={active} />
            <span className={styles.toolTitle}>{step.title}</span>
            {step.status === "declined" && <span className={styles.stepState}>已拒绝</span>}
            {step.status === "backgrounded" && <span className={styles.stepState}>后台运行</span>}
            {step.summary !== step.title && (
              <span className={styles.toolSeparator} aria-hidden>
                ·
              </span>
            )}
            <span className={styles.stepSummary}>
              {step.summary === step.title ? "" : step.summary}
            </span>
            <StepTime step={step} />
            <span className={styles.chevron} aria-hidden>
              ›
            </span>
          </summary>
          <div className={styles.stepBody}>
            {step.text && <Markdown text={step.text} />}
            {step.input && (
              <div className={styles.io}>
                <span>{step.title === "执行命令" ? "命令" : "输入"}</span>
                <pre data-scroll="input">{step.input}</pre>
              </div>
            )}
            {step.output && (
              <div className={styles.io}>
                <span>输出</span>
                <pre data-scroll="output">{step.output}</pre>
              </div>
            )}
            {!step.text && !step.input && !step.output && (
              <p className={styles.empty}>
                {active && step.pending ? "等待返回结果…" : "未采集到正文，可查看原始记录。"}
              </p>
            )}
            {warning}
            <StepDiagnostics step={step} open={rawOpen} />
          </div>
        </details>
      )}
      {compact && inspect && (
        <button
          className={styles.inspect}
          onClick={inspect}
          aria-label={`在完整过程中查看：${step.summary}`}
        >
          ↗
        </button>
      )}
      {!compact && step.kind === "text" && <StepDiagnostics step={step} quiet open={rawOpen} />}
    </article>
  );
}
function StepDiagnostics({
  step,
  quiet = false,
  open = false,
}: {
  step: Step;
  quiet?: boolean;
  open?: boolean;
}) {
  return (
    <details className={styles.diagnostics} data-disclosure="raw" open={open || undefined}>
      <summary aria-label={quiet ? "查看原始记录" : undefined}>{quiet ? "⋯" : "原始记录"}</summary>
      <small>
        记录 {step.seq}–{step.lastSeq}
        {step.thread ? ` · Thread ${step.thread}` : ""}
        {step.turn ? ` · Turn ${step.turn}` : ""} · {step.nativeID || "无原生 ID"}
      </small>
      <pre>{JSON.stringify(step.rows, null, 2)}</pre>
    </details>
  );
}
