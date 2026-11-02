import { useEffect, useMemo, useRef, useState, type PointerEvent } from "react";
import { Button } from "../ui/Button.tsx";
import type { Step } from "./model.ts";
import styles from "./Overview.module.css";

/** Sequence identities keep a selection stable when older pages are prepended. */
export interface SequenceRange {
  start: number;
  end: number;
}
interface Props {
  steps: readonly Step[];
  selected: number | undefined;
  range: SequenceRange | null;
  matches: ReadonlySet<number> | null;
  moreBefore: boolean;
  moreAfter: boolean;
  onPick(step: Step): void;
  onRange(range: SequenceRange | null): void;
}
const lanes = ["输入/事件", "模型", "工具"];
function lane(step: Step) {
  return step.kind === "tool" ? 2 : step.kind === "text" || step.kind === "reasoning" ? 1 : 0;
}
function fraction(el: HTMLElement, x: number) {
  const box = el.getBoundingClientRect();
  return Math.max(0, Math.min(1, (x - box.left) / Math.max(1, box.width)));
}
function bounds(steps: readonly Step[], view: SequenceRange | null) {
  const first = view ? steps.findIndex((step) => step.seq >= view.start) : 0;
  const last = view ? steps.findLastIndex((step) => step.seq <= view.end) : steps.length - 1;
  return first >= 0 && last >= first ? { first, last } : { first: 0, last: steps.length - 1 };
}

/** A compact, read-only map of the already loaded sequence, never a timing estimate. */
export function RolloutOverview({
  steps,
  selected,
  range,
  matches,
  moreBefore,
  moreAfter,
  onPick,
  onRange,
}: Props) {
  const [view, setView] = useState<SequenceRange | null>(null);
  const [draft, setDraft] = useState<SequenceRange | null>(null);
  const plot = useRef<HTMLDivElement>(null);
  const gesture = useRef<{
    pointer: number;
    x: number;
    start: number;
    moved: boolean;
    seq: number | undefined;
    pan: boolean;
    first: number;
    count: number;
  } | null>(null);
  const { first, last } = bounds(steps, view);
  const count = Math.max(0, last - first + 1);
  const visible = useMemo(() => steps.slice(first, last + 1), [steps, first, last]);
  const active = draft ?? range;
  const activeIndexes = active
    ? visible.flatMap((step, index) =>
        step.seq >= active.start && step.seq <= active.end ? [index] : [],
      )
    : [];
  const activeFirst = activeIndexes[0];
  const activeLast = activeIndexes.at(-1);
  const selectedCount = range
    ? steps.filter((step) => step.seq >= range.start && step.seq <= range.end).length
    : 0;
  const applyView = (start: number, length: number) => {
    if (!steps.length || length >= steps.length) {
      setView(null);
      return;
    }
    const size = Math.max(1, Math.min(steps.length, Math.round(length)));
    const left = Math.max(0, Math.min(steps.length - size, Math.round(start)));
    setView({ start: steps[left]!.seq, end: steps[left + size - 1]!.seq });
  };
  useEffect(() => {
    const el = plot.current;
    if (!el) return;
    const wheel = (event: WheelEvent) => {
      if (!steps.length) return;
      event.preventDefault();
      const pivot = fraction(el, event.clientX);
      const next = Math.max(
        Math.min(4, steps.length),
        Math.min(
          steps.length,
          Math.round(count * Math.exp(Math.max(-1, Math.min(1, event.deltaY * 0.002)))),
        ),
      );
      if (next >= steps.length) {
        setView(null);
        return;
      }
      const start = Math.max(
        0,
        Math.min(steps.length - next, Math.round(first + pivot * (count - next))),
      );
      setView({ start: steps[start]!.seq, end: steps[start + next - 1]!.seq });
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  }, [steps, first, count]);
  const stepAt = (position: number) => visible[Math.min(count - 1, Math.floor(position * count))];
  const interval = (from: number, to: number): SequenceRange | null => {
    const left = stepAt(Math.min(from, to));
    const right = stepAt(Math.max(from, to));
    return left && right ? { start: left.seq, end: right.seq } : null;
  };
  const finish = (event: PointerEvent<HTMLDivElement>) => {
    const drag = gesture.current;
    if (!drag || drag.pointer !== event.pointerId) return;
    gesture.current = null;
    setDraft(null);
    if (drag.pan) {
      if (!drag.moved) onRange(null);
      return;
    }
    if (drag.moved) onRange(interval(drag.start, fraction(event.currentTarget, event.clientX)));
    else {
      const step =
        steps.find((item) => item.seq === drag.seq) ??
        stepAt(fraction(event.currentTarget, event.clientX));
      if (step) onPick(step);
    }
  };
  return (
    <div className={styles.root} aria-label="过程概览">
      <div className={styles.caption}>
        <span title="只展示已经读取的语义记录，横轴表示执行顺序，不表示耗时。">
          当前加载范围{steps.length ? ` #${steps[0]!.seq}–${steps.at(-1)!.lastSeq}` : ""}
          <span className={styles.mode}>按顺序</span>
        </span>
        <div className={styles.actions}>
          {range && (
            <>
              <span className={styles.selectionCount}>已选 {selectedCount} 条</span>
              <Button variant="ghost" size="s" onClick={() => onRange(null)}>
                清除选择
              </Button>
            </>
          )}
          <Button
            variant="ghost"
            size="s"
            icon
            disabled={!count || count <= Math.min(4, steps.length)}
            aria-label="放大概览"
            onClick={() => applyView(first + count / 4, Math.max(4, count / 2))}
          >
            <svg viewBox="0 0 16 16" aria-hidden>
              <path d="M3 8h10M8 3v10" />
            </svg>
          </Button>
          <Button
            variant="ghost"
            size="s"
            disabled={!view}
            aria-label="恢复全览"
            onClick={() => setView(null)}
          >
            全览
          </Button>
        </div>
      </div>
      <div className={styles.plot}>
        <div className={styles.labels} aria-hidden>
          {lanes.map((name) => (
            <span key={name}>{name}</span>
          ))}
        </div>
        <div
          ref={plot}
          className={styles.track}
          role="group"
          aria-label="横向执行概览"
          tabIndex={0}
          onPointerDown={(event) => {
            if (!steps.length || ![0, 2].includes(event.button)) return;
            event.preventDefault();
            const node = (event.target as HTMLElement).closest<HTMLElement>("[data-overview-seq]");
            gesture.current = {
              pointer: event.pointerId,
              x: event.clientX,
              start: fraction(event.currentTarget, event.clientX),
              moved: false,
              seq: node ? Number(node.dataset.overviewSeq) : undefined,
              pan: event.button === 2,
              first,
              count,
            };
            event.currentTarget.focus({ preventScroll: true });
            event.currentTarget.setPointerCapture(event.pointerId);
          }}
          onPointerMove={(event) => {
            const drag = gesture.current;
            if (!drag || drag.pointer !== event.pointerId) return;
            if (Math.abs(event.clientX - drag.x) >= 3) drag.moved = true;
            if (drag.pan) {
              if (drag.count < steps.length)
                applyView(
                  drag.first -
                    ((event.clientX - drag.x) / Math.max(1, event.currentTarget.clientWidth)) *
                      drag.count,
                  drag.count,
                );
            } else if (drag.moved)
              setDraft(interval(drag.start, fraction(event.currentTarget, event.clientX)));
          }}
          onPointerUp={finish}
          onPointerCancel={() => {
            gesture.current = null;
            setDraft(null);
          }}
          onLostPointerCapture={() => {
            gesture.current = null;
            setDraft(null);
          }}
          onContextMenu={(event) => event.preventDefault()}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              onRange(null);
              return;
            }
            const index = steps.findIndex((step) => step.seq === selected);
            const next =
              event.key === "Home"
                ? 0
                : event.key === "End"
                  ? steps.length - 1
                  : event.key === "ArrowRight"
                    ? Math.min(steps.length - 1, index + 1)
                    : event.key === "ArrowLeft"
                      ? Math.max(0, index - 1)
                      : -1;
            if (next >= 0 && steps[next]) {
              event.preventDefault();
              onPick(steps[next]);
            }
          }}
        >
          <div className={styles.rails} aria-hidden>
            <i />
            <i />
            <i />
          </div>
          {activeFirst !== undefined && activeLast !== undefined && (
            <div
              className={styles.selection}
              aria-hidden
              style={{
                left: `${(activeFirst / count) * 100}%`,
                width: `${((activeLast - activeFirst + 1) / count) * 100}%`,
              }}
            />
          )}
          {visible.map((step, index) => (
            <button
              key={step.key}
              type="button"
              tabIndex={-1}
              className={styles.block}
              data-overview-seq={step.seq}
              data-lane={lane(step)}
              data-failed={step.failed}
              data-current={step.seq === selected}
              data-focused={!active || (step.seq >= active.start && step.seq <= active.end)}
              data-match={matches === null || matches.has(step.seq)}
              style={{ left: `${(index / count) * 100}%`, width: `${100 / count}%` }}
              aria-label={`记录 #${step.seq}，${step.title}，${step.summary}${step.failed ? "，执行失败" : ""}`}
              title={`#${step.seq} · ${step.title}\n${step.summary.slice(0, 160)}${step.failed ? "\n执行失败" : ""}${step.durationMs !== null ? `\n已记录耗时 ${step.durationMs} ms` : "\n耗时未记录"}`}
              onClick={(event) => {
                if (event.detail === 0) onPick(step);
              }}
            >
              <span />
            </button>
          ))}
          {!steps.length && <span className={styles.empty}>等待执行记录</span>}
          {moreBefore && (
            <span
              className={styles.earlier}
              title="更早的历史尚未加载；向上滚动记录列表会自动读取。"
              aria-label="更早记录尚未加载"
            >
              …
            </span>
          )}
          {moreAfter && (
            <span className={styles.later} title="后续记录尚未加载。" aria-label="后续记录尚未加载">
              …
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
