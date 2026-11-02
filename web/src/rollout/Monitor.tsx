import {
  Fragment,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { RunView, TaskDetail } from "../api/generated/api.ts";
import { back, Link, navigate, useEntryState, type Route } from "../app/router.ts";
import { useServices } from "../app/services.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { captureAnchor, restoreAnchor, type ScrollAnchor } from "../conversation/scrollAnchor.ts";
import { useStore } from "../state/store.ts";
import { runLabel } from "../tasks/runLabel.ts";
import { Button } from "../ui/Button.tsx";
import { Menu } from "../ui/Menu.tsx";
import { WorkItemRead } from "../workitems/live.ts";
import {
  buildSteps,
  fieldRecord,
  firstLine,
  object,
  statusLabel,
  stepForRecord,
  type Step,
} from "./model.ts";
import { useRunWindow, previewStateLabel } from "./useRunWindow.ts";
import { NativeContent } from "./NativeContent.tsx";
import { RolloutOverview, type SequenceRange } from "./Overview.tsx";
import styles from "./Monitor.module.css";

type MonitorRoute = Extract<Route, { page: "rollout" }>;
type Tab = "overview" | "input" | "output" | "raw";
type SelectionContext = {
  native: boolean;
  generation: string;
  sourceChanged: boolean;
  record: string;
  step: Step;
};
type ReadingPosition = {
  live: boolean;
  selection?: Step | undefined;
  native: boolean;
  generation: string;
  after: number;
  anchor: ScrollAnchor;
};
const tabs: readonly { id: Tab; label: string }[] = [
  { id: "overview", label: "概览" },
  { id: "input", label: "输入" },
  { id: "output", label: "输出" },
  { id: "raw", label: "原始记录" },
];

function Glyph({
  name,
}: {
  name: "back" | "down" | "close" | "search" | "fold" | "latest" | "error";
}) {
  const paths: Record<typeof name, ReactNode> = {
    back: <path d="m10 3-5 5 5 5" />,
    down: <path d="m4 6 4 4 4-4" />,
    close: <path d="m4 4 8 8M12 4l-8 8" />,
    search: (
      <>
        <circle cx="6.5" cy="6.5" r="4.5" />
        <path d="m10 10 4 4" />
      </>
    ),
    fold: (
      <>
        <path d="M6 4h8M6 8h8M6 12h8M2 4h1M2 8h1M2 12h1" />
      </>
    ),
    latest: <path d="M8 2v11m-4-4 4 4 4-4" />,
    error: (
      <>
        <circle cx="8" cy="8" r="6" />
        <path d="M8 4.5v4M8 11h.01" />
      </>
    ),
  };
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {paths[name]}
    </svg>
  );
}

function shortRun(runs: readonly RunView[], run: RunView) {
  const role = run.role === "accept" ? "独立验收" : run.role === "issue_manager" ? "协调" : "实施";
  return `${role} · 第 ${runs.findIndex((item) => item.run_id === run.run_id) + 1} 次`;
}

export function RolloutMonitor({ route }: { route: MonitorRoute }) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<TaskDetail>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => {
    void read.open(`/api/v1/tasks/${encodeURIComponent(route.id)}`);
    const stop = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      read.close();
    };
  }, [read, live, route.id]);
  const task = state.value;
  const run = task?.runs?.find((item) => item.run_id === route.run);
  if (!task || !run)
    return (
      <section className={styles.page}>
        <div className={styles.breadcrumb}>
          <Button
            variant="ghost"
            onClick={() =>
              back({ page: "task", id: route.id, ...(route.from ? { from: route.from } : {}) })
            }
          >
            <Glyph name="back" />
            返回任务
          </Button>
        </div>
        <h1 className={styles.loadingTitle}>执行过程</h1>
        <p className={styles.empty}>
          {state.error
            ? "任务暂时无法读取。"
            : task
              ? "此执行不属于当前任务。"
              : "正在读取执行记录…"}
        </p>
        {state.error && <Button onClick={() => void read.refresh()}>重新读取</Button>}
      </section>
    );
  return <MonitorWindow key={run.run_id} route={route} task={task} run={run} />;
}

function role(step: Step) {
  if (step.kind === "tool") return "TOOL";
  if (step.kind === "text" || step.kind === "reasoning") return "ASSISTANT";
  if (step.title === "任务输入") return "USER";
  return step.failed ? "ERROR" : "SYSTEM";
}

function MonitorWindow({
  route,
  task,
  run,
}: {
  route: MonitorRoute;
  task: TaskDetail;
  run: RunView;
}) {
  const [position, rememberPosition] = useEntryState<ReadingPosition | null>(
    `monitor-position-${run.run_id}`,
    null,
  );
  const [initialPosition, setInitialPosition] = useState(() =>
    position?.native === (route.source !== "logs") ? position : null,
  );
  const window = useRunWindow(
    run.run_id,
    route.source !== "logs",
    route.record,
    initialPosition && !initialPosition.live ? initialPosition : undefined,
  );
  const [prefs, save] = useEntryState(`monitor-${run.run_id}`, {
    selected: route.record ?? "",
    query: "",
    foldedTurns: false,
    foldedCalls: false,
    showEvents: false,
    inspector: !!route.record || !matchMedia("(max-width: 760px)").matches,
  });
  const [heldSelection, holdSelection] = useEntryState<SelectionContext | null>(
    `monitor-selection-${run.run_id}`,
    initialPosition?.selection
      ? {
          native: initialPosition.native,
          generation: initialPosition.generation,
          sourceChanged: false,
          record: prefs.selected || String(initialPosition.selection.seq),
          step: initialPosition.selection,
        }
      : null,
  );
  const [tab, setTab] = useEntryState<Tab>(`monitor-tab-${run.run_id}`, "overview");
  const [range, setRange] = useState<SequenceRange | null>(null);
  const overviewTarget = useRef<number | null>(null);
  const inspector = prefs.inspector;
  const [width, setWidth] = useState(384);
  const resize = useRef<{ x: number; width: number } | null>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const pane = useRef<HTMLDivElement>(null);
  const loaded = useRef(false);
  const latestRequested = useRef(false);
  const reading = useRef<ScrollAnchor | null>(initialPosition?.anchor ?? null);
  const paging = useRef<{ page: typeof window.page; anchor: ScrollAnchor } | null>(null);
  const requestedCursor = useRef("");
  const allowPaging = useRef(false);
  const steps = useMemo(() => buildSteps(window.page?.logs ?? []), [window.page]);
  const query = prefs.query.trim().toLocaleLowerCase();
  const visible = useMemo(
    () =>
      steps.filter(
        (step) =>
          (prefs.showEvents ||
            step.kind !== "event" ||
            role(step) === "USER" ||
            step.failed ||
            step.title === "未知格式") &&
          (!query ||
            [step.title, step.text, step.input, step.output, role(step)].some((value) =>
              value.toLocaleLowerCase().includes(query),
            )),
      ),
    [steps, prefs.showEvents, query],
  );
  const groups = useMemo(() => {
    const result: { key: string; turn: string; steps: Step[] }[] = [];
    for (const step of visible) {
      const key = JSON.stringify([step.thread, step.turn]);
      if (result.at(-1)?.key === key) result.at(-1)!.steps.push(step);
      else result.push({ key, turn: step.turn, steps: [step] });
    }
    return result;
  }, [visible]);
  const held =
    window.page &&
    heldSelection?.native === window.native &&
    heldSelection.generation === window.generation &&
    (heldSelection.sourceChanged === window.sourceChanged ||
      (!window.sourceChanged &&
        initialPosition?.native === window.native &&
        initialPosition.generation === window.generation))
      ? heldSelection
      : null;
  const retainedSelection = held?.step;
  const retainedStep = retainedSelection
    ? (steps.find((step) => step.key === retainedSelection.key) ?? retainedSelection)
    : undefined;
  const selected = prefs.selected
    ? (stepForRecord(steps, prefs.selected) ??
      (held?.record === prefs.selected ? retainedStep : undefined))
    : ((initialPosition ? retainedStep : undefined) ??
      steps.findLast((step) => step.failed && !!(step.text || step.input || step.output)) ??
      steps.findLast((step) => step.kind === "tool"));
  useEffect(() => {
    if (!window.page || !selected) return;
    const record = prefs.selected || String(selected.seq);
    if (held?.step === selected && held.record === record) return;
    holdSelection({
      native: window.native,
      generation: window.generation,
      sourceChanged: window.sourceChanged,
      record,
      step: selected,
    });
  }, [
    window.page,
    window.native,
    window.generation,
    window.sourceChanged,
    selected,
    prefs.selected,
    held,
    holdSelection,
  ]);
  const savePosition = useRef(() => {});
  useEffect(() => {
    savePosition.current = () => {
      if (!window.page) {
        rememberPosition(initialPosition?.native === window.native ? initialPosition : null);
        return;
      }
      const anchor = reading.current;
      const row = anchor?.items.flatMap((item) => steps.filter((step) => step.key === item.id))[0];
      rememberPosition(
        anchor && row
          ? {
              live: window.live,
              selection: selected,
              native: window.native,
              generation: window.generation,
              after: Math.max(0, row.seq - 21),
              anchor,
            }
          : null,
      );
    };
  }, [window, initialPosition, steps, selected, rememberPosition]);
  useEffect(() => () => savePosition.current(), []);
  const returnTask = {
    page: "task" as const,
    id: route.id,
    ...(route.from ? { from: route.from } : {}),
  };
  const pick = (step: Step, focus = false) => {
    if (range && (step.seq < range.start || step.seq > range.end)) setRange(null);
    save({ ...prefs, selected: String(step.seq), inspector: true });
    setTab("overview");
    if (focus && matchMedia("(max-width: 760px)").matches)
      requestAnimationFrame(() => closeRef.current?.focus());
  };
  const locate = (step: Step) => {
    window.setLive(false);
    setRange(null);
    overviewTarget.current = step.seq;
    save({
      ...prefs,
      selected: String(step.seq),
      inspector: true,
      foldedTurns: false,
      foldedCalls: false,
      showEvents: prefs.showEvents || step.kind === "event",
      query: "",
    });
    setTab("overview");
    if (matchMedia("(max-width: 760px)").matches)
      requestAnimationFrame(() => closeRef.current?.focus());
  };
  const focusRange = (next: SequenceRange | null) => {
    setRange(next);
    if (!next) return;
    window.setLive(false);
    save({ ...prefs, foldedTurns: false, foldedCalls: false });
    overviewTarget.current =
      visible.find((step) => step.seq >= next.start && step.seq <= next.end)?.seq ?? null;
  };
  const closeInspector = () => {
    save({ ...prefs, inspector: false });
    requestAnimationFrame(() =>
      pane.current
        ?.querySelector<HTMLElement>(`[data-seq="${selected?.seq ?? prefs.selected}"]`)
        ?.focus(),
    );
  };
  const fetchEdge = (direction: "before" | "after") => {
    const el = pane.current;
    if (
      !el ||
      !el.clientHeight ||
      !window.page ||
      window.loading ||
      window.error ||
      query ||
      prefs.foldedTurns ||
      prefs.foldedCalls
    )
      return;
    if (direction === "before" ? !window.page.more_before : !window.page.more_after) return;
    const cursor = `${direction}:${window.page[direction]}`;
    if (requestedCursor.current === cursor) return;
    requestedCursor.current = cursor;
    paging.current = { page: window.page, anchor: captureAnchor(el) };
    if (direction === "before") window.earlier();
    else window.later();
  };
  useLayoutEffect(() => {
    const el = pane.current;
    if (!el || !el.clientHeight || !window.page || window.loading) return;
    if (latestRequested.current) {
      loaded.current = true;
      el.scrollTop = el.scrollHeight;
      latestRequested.current = false;
    } else if (overviewTarget.current !== null) {
      const target = el.querySelector<HTMLElement>(`[data-seq="${overviewTarget.current}"]`);
      target?.scrollIntoView({ block: "center" });
      overviewTarget.current = null;
      loaded.current = true;
    } else if (!loaded.current) {
      loaded.current = true;
      const routedStep = stepForRecord(steps, route.record);
      const target = routedStep && el.querySelector<HTMLElement>(`[data-seq="${routedStep.seq}"]`);
      if (
        initialPosition &&
        !initialPosition.live &&
        !window.sourceChanged &&
        initialPosition.anchor.items.some((item) =>
          [...el.querySelectorAll<HTMLElement>("[data-scroll-id]")].some(
            (node) => node.dataset.scrollId === item.id,
          ),
        )
      ) {
        restoreAnchor(el, initialPosition.anchor);
      } else if (target) {
        target.scrollIntoView({ block: "center" });
        window.setLive(false);
      } else el.scrollTop = el.scrollHeight;
    } else if (paging.current && paging.current.page !== window.page) {
      restoreAnchor(el, paging.current.anchor);
      paging.current = null;
    } else if (window.live) {
      el.scrollTop = el.scrollHeight;
    } else if (reading.current) restoreAnchor(el, reading.current);
    const anchor = captureAnchor(el);
    if (anchor.items.length) reading.current = anchor;
    // A poll can return the same cursor. Only an explicit read retry clears it.
  }, [window, route.record, steps, initialPosition]);
  useEffect(() => {
    const el = pane.current;
    if (!el) return;
    const observer = new ResizeObserver(() => {
      if (!el.clientHeight) return;
      if (window.live) el.scrollTop = el.scrollHeight;
      else if (reading.current) restoreAnchor(el, reading.current);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [window.live]);
  const resume = () => {
    setInitialPosition(null);
    setRange(null);
    paging.current = null;
    requestedCursor.current = "";
    latestRequested.current = true;
    allowPaging.current = false;
    pane.current?.scrollTo({ top: pane.current.scrollHeight });
    window.latest();
    if (route.record) navigate({ ...route, record: "" }, { replace: true });
  };
  const selectSource = (native: boolean) => {
    if (native === window.native) return;
    setInitialPosition(null);
    holdSelection(null);
    loaded.current = false;
    allowPaging.current = false;
    reading.current = null;
    paging.current = null;
    requestedCursor.current = "";
    setRange(null);
    save({ ...prefs, selected: "" });
    window.selectSource(native);
    navigate(
      {
        page: "rollout",
        id: route.id,
        run: route.run,
        ...(route.from ? { from: route.from } : {}),
        ...(!native ? { source: "logs" as const } : {}),
      },
      { replace: true },
    );
  };
  const retry = () => {
    requestedCursor.current = "";
    window.reconnect();
  };
  return (
    <section
      className={styles.page}
      aria-label="执行过程监控"
      data-inspecting={inspector && !!selected}
    >
      <nav className={styles.breadcrumb} aria-label="返回路径">
        <Button variant="ghost" size="s" onClick={() => back(returnTask)} aria-label="返回任务">
          <Glyph name="back" />
        </Button>
        <Link to={returnTask} className={styles.taskTitle}>
          {task.title}
        </Link>
        <span aria-hidden>/</span>
        <span>执行过程</span>
      </nav>
      <header className={styles.heading}>
        <h1>执行过程</h1>
        <Menu
          label="切换执行记录"
          variant="ghost"
          size="s"
          className={styles.runMenu}
          items={(task.runs ?? []).map((item) => ({
            id: item.run_id,
            label: runLabel(task.runs ?? [], item),
            onSelect: () => navigate({ ...route, run: item.run_id, record: "" }, { replace: true }),
          }))}
        >
          {shortRun(task.runs ?? [], run)}
          <Glyph name="down" />
        </Menu>
        <span className={styles.runStatus} data-failed={run.observed === "failed"}>
          {run.observed === "failed" && <Glyph name="error" />}
          {statusLabel(run.observed)}
        </span>
        <Menu
          label="记录来源"
          variant="ghost"
          size="s"
          className={styles.connection}
          items={[
            { id: "native", label: "原生记录", onSelect: () => selectSource(true) },
            { id: "logs", label: "服务端已采集日志", onSelect: () => selectSource(false) },
          ]}
        >
          <i />
          {window.native
            ? window.preview?.origin === "central"
              ? "central 原生记录"
              : window.preview?.origin === "runner"
                ? "runner 原生记录"
                : "原生记录"
            : "已采集日志"}
          <Glyph name="down" />
        </Menu>
      </header>
      <div className={styles.toolbar}>
        <Button
          variant="ghost"
          size="s"
          aria-pressed={prefs.foldedTurns}
          onClick={() => save({ ...prefs, foldedTurns: !prefs.foldedTurns })}
        >
          <Glyph name="fold" />
          {prefs.foldedTurns ? "展开轮次" : "折叠轮次"}
        </Button>
        <Button
          variant="ghost"
          size="s"
          aria-pressed={prefs.foldedCalls}
          onClick={() => save({ ...prefs, foldedCalls: !prefs.foldedCalls })}
        >
          <Glyph name="fold" />
          {prefs.foldedCalls ? "展开调用" : "折叠调用"}
        </Button>
        <Menu
          label="记录显示选项"
          variant="ghost"
          size="s"
          items={[
            {
              id: "system",
              label: prefs.showEvents ? "隐藏系统记录" : "显示系统记录",
              onSelect: () => save({ ...prefs, showEvents: !prefs.showEvents }),
            },
          ]}
        >
          <span aria-hidden>···</span>
        </Menu>
        <label className={styles.search}>
          <Glyph name="search" />
          <input
            type="search"
            aria-label="搜索记录"
            placeholder="搜索记录"
            value={prefs.query}
            onChange={(event) => {
              window.setLive(false);
              save({ ...prefs, query: event.target.value });
            }}
          />
        </label>
      </div>
      <div className={styles.overview}>
        <RolloutOverview
          steps={steps}
          selected={selected?.seq}
          range={range}
          matches={query ? new Set(visible.map((step) => step.seq)) : null}
          moreBefore={window.page?.more_before ?? false}
          moreAfter={window.page?.more_after ?? false}
          onPick={locate}
          onRange={focusRange}
        />
      </div>
      {window.sourceChanged && window.native && (
        <p className={styles.warning} role="status">
          原件或来源发生变化，已重新读取当前记录窗口。
        </p>
      )}
      {(window.error ||
        (window.native &&
          window.preview &&
          !["ready", "empty"].includes(window.preview.state))) && (
        <div className={styles.readError} role="alert">
          {window.native
            ? previewStateLabel(window.preview?.state ?? "unavailable")
            : "已采集日志暂不可更新，已保留已读内容。"}
          <Button variant="link" onClick={retry}>
            重新读取
          </Button>
          {window.native && (
            <Button variant="link" onClick={() => selectSource(false)}>
              查看已采集日志
            </Button>
          )}
        </div>
      )}
      <div className={styles.split} data-inspecting={inspector && !!selected}>
        <div className={styles.ledger}>
          <div
            className={styles.scroller}
            ref={pane}
            data-trace-scroll
            data-scroll-restoration="manual"
            onPointerDown={() => {
              allowPaging.current = true;
            }}
            onWheel={(event) => {
              allowPaging.current = event.deltaY < 0 || !window.live;
              if (event.deltaY < 0) window.setLive(false);
              if (event.deltaY < 0 && event.currentTarget.scrollTop < 64) {
                fetchEdge("before");
              }
            }}
            onTouchMove={() => {
              allowPaging.current = true;
              window.setLive(false);
            }}
            onScroll={(event) => {
              const el = event.currentTarget;
              if (!el.clientHeight) return;
              const anchor = captureAnchor(el);
              if (anchor.items.length) reading.current = anchor;
              if (!loaded.current || paging.current || !allowPaging.current) return;
              if (el.scrollHeight - el.scrollTop - el.clientHeight > 48) window.setLive(false);
              if (el.scrollTop < 64) fetchEdge("before");
              else if (el.scrollHeight - el.scrollTop - el.clientHeight < 64) fetchEdge("after");
            }}
            onKeyDown={(event) => {
              if (
                ["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(
                  event.key,
                )
              ) {
                allowPaging.current = true;
                window.setLive(false);
              }
            }}
          >
            <table className={styles.table} aria-label="执行记录">
              <colgroup>
                <col className={styles.indexColumn} />
                <col className={styles.roleColumn} />
                <col />
              </colgroup>
              <thead>
                <tr>
                  <th scope="col">#</th>
                  <th scope="col">事件</th>
                  <th scope="col">内容</th>
                </tr>
              </thead>
              <tbody>
                {(window.loading || window.page?.more_before) && (
                  <tr className={styles.history}>
                    <td colSpan={3} role="status">
                      {window.loading
                        ? "正在读取记录…"
                        : query
                          ? "搜索当前已加载的记录"
                          : "向上滚动读取更早记录"}
                    </td>
                  </tr>
                )}
                {groups.map((group, index) => (
                  <Fragment key={group.steps[0]!.key}>
                    {(group.turn || groups.length > 1) && (
                      <tr className={styles.group}>
                        <td colSpan={3}>
                          <button
                            aria-expanded={!prefs.foldedTurns || !!query}
                            onClick={() => save({ ...prefs, foldedTurns: !prefs.foldedTurns })}
                          >
                            <Glyph name="down" />
                            {group.turn ? `轮次 ${index + 1}` : "执行事件"}
                            <span>{group.steps.length} 条记录</span>
                          </button>
                        </td>
                      </tr>
                    )}
                    {(!prefs.foldedTurns || query) &&
                      group.steps
                        .filter((step) => !prefs.foldedCalls || query || step.kind !== "tool")
                        .map((step) => (
                          <tr
                            key={step.key}
                            tabIndex={
                              selected?.key === step.key || (!selected && step === visible[0])
                                ? 0
                                : -1
                            }
                            aria-selected={selected?.key === step.key && inspector}
                            data-selected={selected?.key === step.key && inspector}
                            data-failed={step.failed}
                            data-in-range={
                              range ? step.seq >= range.start && step.seq <= range.end : undefined
                            }
                            data-scroll-id={step.key}
                            data-seq={step.seq}
                            aria-label={`${role(step)}，${step.title}，${step.summary}`}
                            onClick={() => pick(step, true)}
                            onKeyDown={(event) => {
                              if (event.key === "Enter" || event.key === " ") {
                                event.preventDefault();
                                pick(step, true);
                              }
                              if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                                event.preventDefault();
                                const rows = [
                                  ...event.currentTarget.parentElement!.querySelectorAll<HTMLElement>(
                                    "[data-seq]",
                                  ),
                                ];
                                const next =
                                  rows[
                                    rows.indexOf(event.currentTarget) +
                                      (event.key === "ArrowDown" ? 1 : -1)
                                  ];
                                next?.focus();
                                const item = steps.find(
                                  (row) => row.seq === Number(next?.dataset.seq),
                                );
                                if (item) pick(item);
                              }
                              if (event.key === "Escape") closeInspector();
                            }}
                          >
                            <td className={styles.index}>{step.seq}</td>
                            <td className={styles.role}>{role(step)}</td>
                            <td className={styles.content} title={step.summary}>
                              <div className={styles.cellFlow}>
                                {step.kind === "tool" ? (
                                  <>
                                    <strong>
                                      {step.title === "执行命令" ? "command" : step.title}
                                    </strong>
                                    <span className={styles.argument}>
                                      {step.summary !== step.title
                                        ? step.summary
                                        : firstLine(step.input || step.output)}
                                    </span>
                                    {step.failed && <span className={styles.failure}>失败</span>}
                                    {step.pending && <span className={styles.pending}>执行中</span>}
                                  </>
                                ) : (
                                  <span>{firstLine(step.text || step.summary)}</span>
                                )}
                                {step.gap && <span className={styles.failure}>记录缺口</span>}
                              </div>
                            </td>
                          </tr>
                        ))}
                    {prefs.foldedCalls &&
                      !query &&
                      group.steps.some((step) => step.kind === "tool") &&
                      !prefs.foldedTurns && (
                        <tr className={styles.folded}>
                          <td colSpan={3}>
                            <Button
                              variant="ghost"
                              onClick={() => save({ ...prefs, foldedCalls: false })}
                            >
                              展开 {group.steps.filter((step) => step.kind === "tool").length}{" "}
                              条工具调用
                            </Button>
                          </td>
                        </tr>
                      )}
                  </Fragment>
                ))}
                {!visible.length && (
                  <tr>
                    <td colSpan={3} className={styles.empty}>
                      {window.loading
                        ? "正在读取过程…"
                        : query
                          ? "当前已加载的记录中没有匹配内容。"
                          : window.native
                            ? "尚无原生过程记录。"
                            : "尚无已采集的记录。"}
                    </td>
                  </tr>
                )}
                {window.page?.more_after && (
                  <tr className={styles.history}>
                    <td colSpan={3}>向下滚动读取后续记录</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <footer className={styles.footer}>
            <span>
              {query
                ? `${visible.length} 条匹配 · 当前窗口`
                : window.live
                  ? "跟随最新"
                  : "阅读历史"}
            </span>
            <Button variant="ghost" size="s" onClick={resume}>
              <Glyph name="latest" />
              回到最新
            </Button>
          </footer>
        </div>
        {selected && inspector && (
          <>
            <div
              className={styles.resize}
              role="separator"
              aria-label="调整记录详情宽度"
              aria-orientation="vertical"
              tabIndex={0}
              aria-valuemin={300}
              aria-valuemax={560}
              aria-valuenow={width}
              onPointerDown={(event) => {
                resize.current = { x: event.clientX, width };
                event.currentTarget.setPointerCapture(event.pointerId);
              }}
              onPointerMove={(event) => {
                if (resize.current)
                  setWidth(
                    Math.max(
                      300,
                      Math.min(560, resize.current.width + resize.current.x - event.clientX),
                    ),
                  );
              }}
              onPointerUp={() => {
                resize.current = null;
              }}
              onPointerCancel={() => {
                resize.current = null;
              }}
              onKeyDown={(event) => {
                if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
                  event.preventDefault();
                  setWidth(
                    Math.max(300, Math.min(560, width + (event.key === "ArrowLeft" ? 16 : -16))),
                  );
                }
              }}
            />
            <aside
              className={styles.inspector}
              style={{ width }}
              aria-label="记录详情"
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  closeInspector();
                }
              }}
            >
              <header className={styles.detailHeading}>
                <strong>{selected.title === "执行命令" ? "command" : selected.title}</strong>
                <span>#{selected.seq}</span>
                <Button
                  ref={closeRef}
                  variant="ghost"
                  size="s"
                  icon
                  aria-label="关闭记录详情"
                  onClick={closeInspector}
                >
                  <Glyph name="close" />
                </Button>
              </header>
              <DetailTabs
                tab={tab}
                onTab={setTab}
                step={selected}
                run={run}
                generation={window.native ? window.generation || undefined : undefined}
              />
            </aside>
          </>
        )}
      </div>
    </section>
  );
}

function DetailTabs({
  tab,
  onTab,
  step,
  run,
  generation,
}: {
  tab: Tab;
  onTab(tab: Tab): void;
  step: Step;
  run: RunView;
  generation?: string | undefined;
}) {
  const id = useId();
  const detail = step.rows.at(-1);
  const envelope = object(detail?.body);
  const data = object(envelope.data ?? envelope);
  const content = (label: string, field: "raw" | "text" | "input" | "output", text: string) =>
    generation ? (
      <NativeContent
        run={run.run_id}
        generation={generation}
        record={fieldRecord(step, field)}
        field={field}
        label={label}
        fallback={text}
      />
    ) : (
      <Content label={label} text={text} />
    );
  const exit = typeof data.exitCode === "number" ? data.exitCode : undefined;
  return (
    <>
      <div className={styles.tabs} role="tablist" aria-label="记录详情内容">
        {tabs.map((item, index) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            id={`${id}-${item.id}`}
            aria-controls={`${id}-panel`}
            aria-selected={tab === item.id}
            tabIndex={tab === item.id ? 0 : -1}
            onClick={() => onTab(item.id)}
            onKeyDown={(event) => {
              const target =
                event.key === "ArrowRight"
                  ? (index + 1) % tabs.length
                  : event.key === "ArrowLeft"
                    ? (index + tabs.length - 1) % tabs.length
                    : event.key === "Home"
                      ? 0
                      : event.key === "End"
                        ? tabs.length - 1
                        : -1;
              if (target < 0) return;
              event.preventDefault();
              onTab(tabs[target]!.id);
              const buttons =
                event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>("button");
              buttons?.[target]?.focus();
            }}
          >
            {item.label}
          </button>
        ))}
      </div>
      <div
        key={`${step.key}-${tab}`}
        className={styles.detailBody}
        role="tabpanel"
        id={`${id}-panel`}
        aria-labelledby={`${id}-${tab}`}
        tabIndex={0}
      >
        {(step.gap || step.truncated) && (
          <p className={styles.warning}>
            {step.gap ? "此处存在记录缺口。" : ""}
            {step.truncated
              ? generation
                ? "列表展示摘要，完整内容按需读取。"
                : "内容已截断，完整输出未被采集。"
              : ""}
          </p>
        )}
        {tab === "overview" ? (
          <>
            <div className={styles.detailStatus} data-failed={step.failed}>
              {step.failed && <Glyph name="error" />}
              <strong>
                {step.failed
                  ? "本条执行失败"
                  : step.pending
                    ? "正在执行"
                    : step.kind === "tool"
                      ? "调用已结束"
                      : "记录内容"}
              </strong>
            </div>
            {step.text &&
              (generation ? (
                content("正文", "text", step.text)
              ) : (
                <div className={styles.prose}>
                  <Markdown text={step.text} />
                </div>
              ))}
            {(step.input || (generation && data.preview_input_record)) &&
              content(step.title === "执行命令" ? "命令" : "输入", "input", step.input)}
            {(step.output || (generation && data.preview_references)) &&
              content("输出", "output", step.output)}
            {!step.text && !step.input && !step.output && (
              <p className={styles.empty}>未采集到正文，可查看原始记录。</p>
            )}
            <dl className={styles.metadata}>
              {exit !== undefined && (
                <div>
                  <dt>退出码</dt>
                  <dd data-failed={exit !== 0}>{exit}</dd>
                </div>
              )}
              {step.durationMs !== null && (
                <div>
                  <dt>原生耗时</dt>
                  <dd>{step.durationMs} ms</dd>
                </div>
              )}
              <div>
                <dt>来源</dt>
                <dd>{generation ? "原生记录" : "服务端已采集日志"}</dd>
              </div>
              <div>
                <dt>记录范围</dt>
                <dd>
                  #{step.seq}
                  {step.lastSeq !== step.seq ? `–${step.lastSeq}` : ""}
                </dd>
              </div>
              {run.spec_version && (
                <div>
                  <dt>要求版本</dt>
                  <dd>第 {run.spec_version} 版</dd>
                </div>
              )}
            </dl>
            <details className={styles.native}>
              <summary>原生身份</summary>
              <dl className={styles.metadata}>
                <div>
                  <dt>Session</dt>
                  <dd>{step.thread || "未记录"}</dd>
                </div>
                <div>
                  <dt>Turn</dt>
                  <dd>{step.turn || "未记录"}</dd>
                </div>
                <div>
                  <dt>Item</dt>
                  <dd>{step.nativeID || "未记录"}</dd>
                </div>
              </dl>
            </details>
          </>
        ) : tab === "input" ? (
          content(
            "输入",
            step.input || data.preview_input_record ? "input" : "text",
            step.input || step.text,
          )
        ) : tab === "output" ? (
          content(
            "输出",
            step.kind === "text" ? "text" : "output",
            step.output || (step.kind === "text" ? step.text : ""),
          )
        ) : (
          content("原始记录", "raw", JSON.stringify(step.rows, null, 2))
        )}
      </div>
    </>
  );
}
function Content({ label, text }: { label: string; text: string }) {
  return (
    <section className={styles.detailSection}>
      <h2>{label}</h2>
      {text ? (
        <pre aria-label={label}>{text}</pre>
      ) : (
        <p className={styles.empty}>未采集到{label}。</p>
      )}
    </section>
  );
}
