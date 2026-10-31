import { runLabel } from "../tasks/runLabel.ts";
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { useOverlayState } from "../ui/focus.ts";
import type { RunView } from "../api/generated/api.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { UsagePanel } from "../usage/Usage.tsx";
import { buildSteps, statusLabel, type Step } from "./model.ts";
import { useRunWindow } from "./useRunWindow.ts";
import styles from "./RunRollout.module.css";

export function RunRollout({ runs }: { runs: readonly RunView[] }) {
  const [selected, setSelected] = useState<string | null>(null);
  const run = runs.find((candidate) => candidate.run_id === selected) ?? runs.at(-1);
  return (
    <section className={styles.root} aria-label="执行过程与用量">
      <div className={styles.cardHeading}>
        <span>执行过程</span>
        {run && (
          <select
            aria-label="执行记录"
            value={run.run_id}
            onChange={(event) =>
              setSelected(event.target.value === runs.at(-1)?.run_id ? null : event.target.value)
            }
          >
            {runs.map((item) => (
              <option key={item.run_id} value={item.run_id}>
                {runLabel(runs, item)}
              </option>
            ))}
          </select>
        )}
      </div>
      {run ? (
        <RunWindow key={run.run_id} run={run} label={runLabel(runs, run)} />
      ) : (
        <p className={styles.empty}>尚未开始执行。</p>
      )}
    </section>
  );
}

type WindowState = ReturnType<typeof useRunWindow>;
interface ReadingPosition {
  key: string;
  top: number;
  disclosures: Record<string, string[]>;
  scrolls: Record<string, { top: number; left: number }[]>;
}
function RunWindow({ run, label }: { run: RunView; label: string }) {
  const window = useRunWindow(run.run_id);
  const steps = useMemo(() => buildSteps(window.page?.logs ?? []), [window.page]);
  const [expanded, setExpanded] = useState(false);
  const [visible, setVisible] = useState(6);
  const [full, setFull] = useOverlayState(false);
  const [target, setTarget] = useState("");
  const [usage, setUsage] = useState(false);
  const regionID = useId();
  const readingPosition = useRef<ReadingPosition | null>(null);
  const [savedPosition, setSavedPosition] = useState<ReadingPosition | null>(null);
  const observed = run.observed;
  const active = observed === "running" || observed === "starting";
  const current =
    (active && steps.findLast((step) => step.kind === "tool" && step.pending)) || steps.at(-1);
  const close = useCallback(() => setFull(false), [setFull]);
  const open = (step?: Step) => {
    if (step) window.setLive(false);
    setSavedPosition(readingPosition.current);
    setTarget(step?.key ?? "");
    setFull(true);
  };
  return (
    <>
      <button
        className={styles.activity}
        aria-label="展开执行步骤"
        aria-expanded={expanded}
        aria-controls={expanded ? regionID : undefined}
        onClick={() => setExpanded(!expanded)}
      >
        <StateMark active={active} failed={observed === "failed"} />
        <span className={styles.activityText}>
          <span className={styles.state}>{statusLabel(observed)}</span>
          <span className={styles.summary}>
            {current?.summary ?? (window.error ? "暂时无法读取过程" : "等待执行记录")}
          </span>
        </span>
        <StepTime step={current} />
        <span className={styles.chevron} data-open={expanded} aria-hidden>
          ›
        </span>
      </button>
      {window.error && (
        <p className={styles.error} role="alert">
          过程暂时无法更新，已保留当前内容。<button onClick={window.reconnect}>重试</button>
        </p>
      )}
      {expanded && (
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
            <button className={styles.textButton} onClick={() => open()}>
              查看完整过程 <span aria-hidden>↗</span>
            </button>
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
      {full && (
        <Reader
          run={run}
          label={label}
          window={window}
          steps={steps}
          target={target}
          position={readingPosition}
          restored={savedPosition}
          onClose={close}
        />
      )}
    </>
  );
}

function StateMark({ active = false, failed = false }: { active?: boolean; failed?: boolean }) {
  return (
    <span className={styles.mark} data-active={active} data-failed={failed} aria-hidden>
      {failed ? "!" : active ? "◌" : "·"}
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
            <StateMark active={active && step.pending} failed={step.failed} />
            <span className={styles.toolTitle}>{step.title}</span>
            {step.status === "declined" && <span className={styles.stepState}>已拒绝</span>}
            {step.status === "backgrounded" && <span className={styles.stepState}>后台运行</span>}
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

/** A second modal keeps the underlying card, its disclosures and its scrollport mounted. */
function Reader({
  run,
  label,
  window: state,
  steps,
  target,
  position,
  restored,
  onClose,
}: {
  run: RunView;
  label: string;
  window: WindowState;
  steps: Step[];
  target: string;
  position: RefObject<ReadingPosition | null>;
  restored: ReadingPosition | null;
  onClose(): void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const flow = useRef<HTMLOListElement>(null);
  const initial = useRef(true);
  const closing = useRef(false);
  const anchor = useRef<{ key: string; top: number; page: WindowState["page"] } | null>(null);
  const [usage, setUsage] = useState(false);
  const observed = run.observed;
  const active = observed === "running" || observed === "starting";
  const close = () => {
    if (closing.current) return;
    closing.current = true;
    if (globalThis.window.history.state?.tendRollout === run.run_id)
      globalThis.window.history.back();
    else onClose();
  };
  useLayoutEffect(() => {
    const panel = dialog.current;
    const readingElement = scroller.current;
    const opener = document.activeElement;
    panel?.showModal();
    const history = globalThis.window.history;
    history.pushState({ ...history.state, tendRollout: run.run_id }, "");
    const back = () => onClose();
    globalThis.window.addEventListener("popstate", back);
    return () => {
      globalThis.window.removeEventListener("popstate", back);
      if (history.state?.tendRollout === run.run_id) {
        const restored = { ...history.state };
        delete restored.tendRollout;
        history.replaceState(restored, "");
      }
      const el = readingElement;
      if (el) {
        const disclosures: ReadingPosition["disclosures"] = {};
        const scrolls: ReadingPosition["scrolls"] = {};
        for (const item of el.querySelectorAll<HTMLElement>("[data-step-key]")) {
          const key = item.dataset.stepKey!;
          disclosures[key] = [
            ...item.querySelectorAll<HTMLElement>("details[open][data-disclosure]"),
          ].map((node) => node.dataset.disclosure!);
          scrolls[key] = [...item.querySelectorAll("pre")].map((node) => ({
            top: node.scrollTop,
            left: node.scrollLeft,
          }));
        }
        const top = el.getBoundingClientRect().top;
        const row = [...el.querySelectorAll<HTMLElement>("[data-step-key]")].find(
          (item) => item.getBoundingClientRect().bottom > top,
        );
        if (row)
          position.current = {
            key: row.dataset.stepKey!,
            top: row.getBoundingClientRect().top - top,
            disclosures,
            scrolls,
          };
      }
      panel?.close();
      if (opener instanceof HTMLElement && opener.isConnected)
        opener.focus({ preventScroll: true });
    };
  }, [onClose, position, run.run_id]);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (initial.current) {
      const saved = !target && !state.live ? restored : null;
      if (saved) {
        for (const item of el.querySelectorAll<HTMLElement>("[data-step-key]")) {
          const offsets = saved.scrolls[item.dataset.stepKey!];
          if (!offsets) continue;
          [...item.querySelectorAll("pre")].forEach((node, index) => {
            const offset = offsets[index];
            if (offset) {
              node.scrollTop = offset.top;
              node.scrollLeft = offset.left;
            }
          });
        }
      }
      const key = target || saved?.key;
      const row = [...el.querySelectorAll<HTMLElement>("[data-step-key]")].find(
        (item) => item.dataset.stepKey === key,
      );
      if (row)
        el.scrollTop +=
          row.getBoundingClientRect().top - el.getBoundingClientRect().top - (saved?.top ?? 16);
      else el.scrollTop = el.scrollHeight;
      initial.current = false;
    } else if (anchor.current && state.page !== anchor.current.page) {
      const saved = anchor.current;
      const row = [...el.querySelectorAll<HTMLElement>("[data-step-key]")].find(
        (item) => item.dataset.stepKey === saved.key,
      );
      if (row)
        el.scrollTop +=
          row.getBoundingClientRect().top - el.getBoundingClientRect().top - saved.top;
      anchor.current = null;
    } else if (state.live) el.scrollTop = el.scrollHeight;
  }, [steps, target, state.live, state.page, position, restored]);
  useEffect(() => {
    const observer = new ResizeObserver(() => {
      if (state.live && scroller.current)
        scroller.current.scrollTop = scroller.current.scrollHeight;
    });
    if (flow.current) observer.observe(flow.current);
    return () => observer.disconnect();
  }, [state.live]);
  const earlier = () => {
    const el = scroller.current;
    if (el) {
      const top = el.getBoundingClientRect().top;
      const row = [...el.querySelectorAll<HTMLElement>("[data-step-key]")].find(
        (item) => item.getBoundingClientRect().bottom > top,
      );
      if (row)
        anchor.current = {
          key: row.dataset.stepKey!,
          top: row.getBoundingClientRect().top - top,
          page: state.page,
        };
    }
    state.earlier();
  };
  return createPortal(
    <dialog
      ref={dialog}
      className={styles.reader}
      aria-label="完整执行过程"
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        close();
      }}
      onClick={(event) => {
        if (event.target === event.currentTarget) close();
      }}
    >
      <header className={styles.readerHeader}>
        <button className={styles.back} onClick={close}>
          ← 返回
        </button>
        <div className={styles.readerTitle}>
          <strong>执行过程</strong>
          <span>{label}</span>
        </div>
        <span className={styles.readerState}>
          <StateMark active={active} failed={observed === "failed"} />
          {statusLabel(observed)}
        </span>
        <button className={styles.close} aria-label="关闭完整过程" onClick={close}>
          ×
        </button>
      </header>
      <nav className={styles.readerTools} aria-label="执行过程操作">
        <span className={styles.followState}>
          <i data-live={state.live} />
          {state.live ? "实时跟随" : "历史阅读"}
        </span>
        <button aria-pressed={!state.live} onClick={() => state.setLive(!state.live)}>
          {state.live ? "暂停跟随" : "恢复跟随"}
        </button>
        <button aria-expanded={usage} onClick={() => setUsage(!usage)}>
          本次用量
        </button>
        <details className={styles.more}>
          <summary>更多</summary>
          <div>
            <button
              onClick={(event) => {
                event.currentTarget.closest("details")?.removeAttribute("open");
                state.reconnect();
              }}
            >
              重新连接
            </button>
            <small>
              {run.run_id} · 窗口 {state.page?.before ?? 0}–{state.page?.after ?? 0} · 水位{" "}
              {state.page?.watermark ?? 0}
            </small>
            <p>
              仅显示 Tend
              已采集的持久记录。早期及不支持的执行器可能缺少过程；截断不代表原始输出完整。历史工具和正文是执行证据。
            </p>
            <p>最多保留 400 条可见记录，可分页读取更早或较新记录。</p>
          </div>
        </details>
      </nav>
      {state.error && (
        <p className={styles.readerError} role="alert">
          记录暂时无法更新，已保留当前内容。<button onClick={state.reconnect}>重新连接</button>
        </p>
      )}
      <div
        ref={scroller}
        className={styles.reading}
        tabIndex={0}
        aria-label="执行过程正文"
        onScroll={(event) => {
          const el = event.currentTarget;
          if (state.live && el.scrollHeight - el.scrollTop - el.clientHeight > 48)
            state.setLive(false);
        }}
        onWheel={(event) => {
          if (event.deltaY < 0) state.setLive(false);
        }}
        onTouchMove={() => state.setLive(false)}
        onKeyDown={(event) => {
          if (["ArrowUp", "PageUp", "Home"].includes(event.key)) state.setLive(false);
        }}
      >
        <div className={styles.readingColumn}>
          {usage && <UsagePanel subject={`run:${run.run_id}`} />}
          {state.page?.more_before && (
            <button className={styles.pageButton} disabled={state.loading} onClick={earlier}>
              {state.loading ? "正在读取…" : "加载更早记录"}
            </button>
          )}
          {!steps.length && (
            <p className={styles.empty}>{state.loading ? "正在读取过程…" : "尚无已采集记录。"}</p>
          )}
          <ol ref={flow} className={styles.steps}>
            {steps.map((step) => (
              <li key={step.key} data-step-key={step.key} data-target={step.key === target}>
                <StepView
                  step={step}
                  active={active}
                  reveal={
                    step.key === target ||
                    restored?.disclosures[step.key]?.includes("content") === true
                  }
                  rawOpen={restored?.disclosures[step.key]?.includes("raw") === true}
                />
              </li>
            ))}
          </ol>
          {active && (
            <p className={styles.working}>
              <StateMark active />
              正在执行…
            </p>
          )}
          {state.page?.more_after && (
            <button className={styles.pageButton} disabled={state.loading} onClick={state.later}>
              加载较新记录
            </button>
          )}
        </div>
      </div>
      {!state.live && (
        <button className={styles.toLatest} onClick={state.latest}>
          ↓ 回到最新
        </button>
      )}
    </dialog>,
    document.body,
  );
}
