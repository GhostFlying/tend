import { PageHeader } from "../shell/PageHeader.tsx";
import { Sheet } from "../ui/Modal.tsx";
import { navigate, useRoute, type IssueFilters } from "../app/router.ts";
import {
  memo,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type DragEventHandler,
  type ReactNode,
} from "react";
import type {
  IssueAssignee,
  IssueBlocker,
  IssueSummary,
  IssueWorkStatus,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Close } from "../shell/icons.tsx";
import { IssueDefinition } from "./Definition.tsx";
import { IssueHierarchy } from "./Hierarchy.tsx";
import { AssigneeName } from "./AssigneeName.tsx";
import {
  boardColumns,
  blockerLabels,
  executionLabel,
  notificationLabel,
  workStatus,
} from "./boardModel.ts";
import { useBoardActions } from "./useBoardActions.ts";
import {
  Button,
  Input,
  Select,
  Checkbox,
  SegmentedControl,
  Tabs,
  Badge,
  EmptyState,
} from "../ui/index.ts";
import styles from "./Board.module.css";

export function IssueBoard({
  onSelect,
  onInbox,
  active,
}: {
  onSelect(id: string): void;
  onInbox?(id?: string): void;
  active: boolean;
}) {
  const { live, client, openTask } = useServices();
  const connection = useStore(client.connection);
  const snapshot = useStore(live.snapshot);
  const catalog = useStore(live.agents.hosted.state);
  const board = live.issues.board;
  const summary = useStore(board.summary);
  const tree = useStore(live.issues.list.state);
  const route = useRoute();
  const filters = route.page === "issues" ? route : { page: "issues" as const };
  const view = filters.view ?? "board";
  const project = filters.project ?? "";
  const assigneeKey = filters.assignee ?? "";
  const closed = filters.closed ?? true;
  const changeFilter = (patch: IssueFilters) =>
    navigate({ ...filters, ...patch }, { replace: true });
  const setView = (view: "board" | "tree") => changeFilter({ view });
  const setProject = (project: string) => changeFilter({ project, assignee: "" });
  const setAssigneeKey = (assignee: string) => changeFilter({ assignee });
  const setClosed = (closed: boolean) => changeFilter({ closed });
  const projectList = useId();
  const [creating, setCreating] = useState(false);
  const [filterOpen, setFilterOpen] = useState(false);
  const [cancelledExpanded, setCancelledExpanded] = useState(() => {
    try {
      return localStorage.getItem("tend.board.cancelled-expanded") === "true";
    } catch {
      return false;
    }
  });
  const mobileTabs = useRef<HTMLDivElement>(null);
  const [mobileColumn, setMobileColumn] = useState<IssueWorkStatus>("in_progress");
  const [menu, setMenu] = useState<IssueSummary | null>(null);
  const [over, setOver] = useState<IssueWorkStatus | null>(null);
  const dragged = useRef<IssueSummary | null>(null);
  useEffect(() => {
    try {
      localStorage.setItem("tend.board.cancelled-expanded", String(cancelledExpanded));
    } catch {
      /* Keep the session preference when storage is unavailable. */
    }
  }, [cancelledExpanded]);
  useLayoutEffect(() => {
    mobileTabs.current
      ?.querySelector('[aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [mobileColumn]);
  const actions = useBoardActions();
  const { feedback, setFeedback } = actions;
  const online = connection.status === "online";
  const filter = useMemo(() => {
    let assignee: IssueAssignee | undefined;
    try {
      const candidate = JSON.parse(assigneeKey || "null") as IssueAssignee | null;
      if (
        candidate &&
        ["owner", "agent"].includes(candidate.kind) &&
        typeof candidate.ref === "string"
      )
        assignee = candidate;
    } catch {
      /* Invalid shared filters fall back to all assignees. */
    }
    return {
      ...(project ? { project } : {}),
      ...(assignee ? { assignee } : {}),
      includeClosed: closed,
    };
  }, [project, assigneeKey, closed]);
  useEffect(() => {
    if (active && !creating) {
      if (view === "board") board.open(filter);
      else
        void live.issues.openList({
          ...(project ? { project } : {}),
          parentID: "",
          includeClosed: closed,
        });
    }
    return () => {
      board.close();
    };
  }, [board, live, active, view, filter, creating, project, closed]);
  useEffect(() => {
    if (!feedback || feedback.outcome.kind !== "done") return;
    const timeout = setTimeout(() => setFeedback(null), 4000);
    return () => clearTimeout(timeout);
  }, [feedback, setFeedback]);
  const counts = summary.counts;
  const total = counts
    ? Object.values(counts).reduce<number>((sum, n) => sum + (n ?? 0), 0)
    : undefined;
  const blockers = readBlockers(
    actions.feedback?.outcome.kind === "refused" ? actions.feedback.outcome.details : undefined,
  );
  const blockedItem = actions.feedback?.item;
  const name = (a: IssueAssignee) =>
    a.kind === "owner"
      ? "你"
      : (catalog.value?.projection.agents?.find(
          (agent) =>
            `${agent.identity.execution_environment_id}/${agent.identity.local_id}` === a.ref,
        )?.display_name ?? "负责人 Agent");
  const openBlocker = (blocker: IssueBlocker) => {
    actions.setFeedback(null);
    if (blocker.code === "attention" && onInbox) onInbox(blocker.object_id);
    else if (blocker.code.startsWith("task_")) openTask(blocker.object_id);
    else if (blocker.code.startsWith("child_") || blocker.code === "issue_dependency")
      onSelect(blocker.object_id);
    else if (blockedItem) onSelect(blockedItem.issue_id);
  };
  const filterControls = (
    <>
      <SegmentedControl
        label="Issue 视图"
        value={view}
        onChange={setView}
        items={[
          { value: "board", label: "看板" },
          { value: "tree", label: "层级" },
        ]}
      />
      <Input
        className={styles.projectFilter}
        aria-label="按项目筛选 Issue"
        list={projectList}
        placeholder="全部项目"
        value={project}
        onChange={(e) => setProject(e.target.value)}
      />
      <datalist id={projectList}>
        {(snapshot?.projects ?? []).map((item) => (
          <option key={item.project} value={item.project}>
            {item.title}
          </option>
        ))}
      </datalist>
      {view === "board" && (
        <Select
          className={styles.assigneeFilter}
          aria-label="按负责人筛选 Issue"
          value={assigneeKey}
          onChange={(e) => setAssigneeKey(e.target.value)}
        >
          <option value="">全部负责人</option>
          {(summary.assignees ?? []).map((a) => (
            <option key={JSON.stringify(a)} value={JSON.stringify(a)}>
              {name(a)}
            </option>
          ))}
        </Select>
      )}
      <Checkbox label="包含已关闭" checked={closed} onChange={(e) => setClosed(e.target.checked)} />
      <Button
        variant="ghost"
        icon
        aria-label="刷新"
        title="刷新"
        onClick={() => (view === "board" ? board.refresh() : void live.issues.list.refresh())}
      >
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.8"
          aria-hidden="true"
        >
          <path d="M20 7a8 8 0 1 0 1 7M20 3v5h-5" />
        </svg>
      </Button>
    </>
  );
  return (
    <section className={styles.root} aria-label="Issue 白板">
      <PageHeader className={styles.header} title="所有 Issue">
        {view === "board" && total !== undefined && <Badge>{total}</Badge>}
        <Button
          className={styles.create}
          variant="primary"
          onClick={() => setCreating(true)}
          disabled={!online}
        >
          新建 Issue
        </Button>
        <Button className={styles.filterButton} onClick={() => setFilterOpen(true)}>
          筛选
        </Button>
      </PageHeader>
      <div className={styles.toolbar}>{!filterOpen && filterControls}</div>
      <div className={styles.mobileFilter}>
        <span>{view === "board" ? "看板" : "层级"} · 仅顶层 Issue</span>
      </div>
      {filterOpen && (
        <Sheet open title="筛选 Issue" onClose={() => setFilterOpen(false)}>
          <div className={styles.filterSheet}>{filterControls}</div>
          <Button variant="primary" onClick={() => setFilterOpen(false)}>
            完成
          </Button>
        </Sheet>
      )}
      {!online && (
        <p className={styles.notice} role="status">
          等待连接恢复 · 当前显示最后读取的 Issue
        </p>
      )}
      {actions.error && (
        <p className={styles.notice} role="alert">
          {actions.error}
        </p>
      )}
      {view === "board" ? (
        <>
          <div ref={mobileTabs} className={styles.mobileTabs}>
            <Tabs<IssueWorkStatus>
              label="工作状态"
              value={mobileColumn}
              onChange={setMobileColumn}
              items={boardColumns.map(({ id, label }) => ({
                value: id,
                label: (
                  <>
                    <span className={styles.dot} data-status={id} />
                    {label}
                    <small>{counts?.[id] ?? "…"}</small>
                  </>
                ),
              }))}
            />
          </div>
          <div className={styles.columns} data-cancelled-expanded={cancelledExpanded}>
            {boardColumns.map(({ id, label }) => (
              <BoardColumn
                key={id}
                status={id}
                label={label}
                count={counts?.[id]}
                mobile={mobileColumn === id}
                over={over === id}
                closed={closed}
                collapsed={id === "cancelled" && !cancelledExpanded}
                onToggle={
                  id === "cancelled" ? () => setCancelledExpanded(!cancelledExpanded) : undefined
                }
                onDragOver={(event) => {
                  if (dragged.current && online) {
                    event.preventDefault();
                    event.dataTransfer.dropEffect = "move";
                    setOver(id);
                  }
                }}
                onDrop={(event) => {
                  event.preventDefault();
                  const item = dragged.current;
                  dragged.current = null;
                  setOver(null);
                  if (item && workStatus(item) !== id) void actions.move(item, id);
                }}
              >
                {(item) => (
                  <BoardCard
                    key={item.issue_id}
                    item={item}
                    state={actions.states[item.issue_id]}
                    disabled={
                      !online ||
                      !actions.ready ||
                      !!actions.states[item.issue_id] ||
                      item.assignment_state !== "active"
                    }
                    onSelect={onSelect}
                    onMenu={setMenu}
                    onReconcile={() => void actions.move(item)}
                    onDragStart={(event) => {
                      dragged.current = item;
                      event.dataTransfer.setData("text/plain", item.issue_id);
                      event.dataTransfer.effectAllowed = "move";
                    }}
                    onDragEnd={() => {
                      dragged.current = null;
                      setOver(null);
                    }}
                  />
                )}
              </BoardColumn>
            ))}
          </div>
        </>
      ) : (
        <div className={styles.tree}>
          {tree.error && (
            <p className={styles.notice}>
              暂时无法读取，已有内容仍保留。
              <Button onClick={() => void live.issues.list.refresh()}>重新读取</Button>
            </p>
          )}
          {tree.value ? (
            <>
              <IssueHierarchy
                items={tree.value.items ?? []}
                onSelect={onSelect}
                includeClosed={closed}
              />
              {!tree.value.items?.length && <p className={styles.empty}>暂无 Issue。</p>}
              {tree.value.more && (
                <Button
                  className={styles.button}
                  onClick={() =>
                    void live.issues.openList({
                      parentID: "",
                      project,
                      includeClosed: closed,
                      ...(tree.value?.next ? { after: tree.value.next } : {}),
                    })
                  }
                >
                  下一页
                </Button>
              )}
            </>
          ) : (
            <p className={styles.empty}>正在读取…</p>
          )}
        </div>
      )}
      {menu && (
        <BoardDialog title="修改工作状态" onClose={() => setMenu(null)}>
          <p className={styles.dialogSubtitle}>{menu.title}</p>
          <div className={styles.statusOptions}>
            {boardColumns.map(({ id, label }) => (
              <Button
                key={id}
                disabled={!online || !!actions.states[menu.issue_id]}
                aria-current={workStatus(menu) === id}
                onClick={() => {
                  setMenu(null);
                  if (workStatus(menu) !== id) void actions.move(menu, id);
                }}
              >
                <span className={styles.dot} data-status={id} />
                {label}
                <span className={styles.check}>{workStatus(menu) === id ? "✓" : ""}</span>
              </Button>
            ))}
          </div>
          <p className={styles.dialogNote}>
            状态保存后，通知按系统设置合并发送。有阻塞时保留原状态。
          </p>
        </BoardDialog>
      )}
      {!!blockers.length && blockedItem && (
        <BoardDialog title="状态变更受阻" onClose={() => actions.setFeedback(null)}>
          <p className={styles.dialogSubtitle}>{blockedItem.title}</p>
          <p className={styles.notice}>还有 {blockers.length} 项需要处理，本次移动未保存。</p>
          <div className={styles.blockers}>
            {blockers.map((b, i) => (
              <Button key={`${b.code}:${b.object_id}:${i}`} onClick={() => openBlocker(b)}>
                {blockerLabels[b.code] ?? "相关事项尚未核对"}
                <span>查看 ›</span>
              </Button>
            ))}
          </div>
        </BoardDialog>
      )}
      {actions.feedback && !blockers.length && (
        <div
          className={styles.feedback}
          role="status"
          data-warning={actions.feedback.outcome.kind !== "done"}
        >
          <span>
            {actions.feedback.outcome.kind === "done"
              ? "状态已保存，通知将合并发送。"
              : actions.feedback.outcome.kind === "stale"
                ? "状态已被更新，已重新读取；请根据最新状态调整。"
                : actions.feedback.outcome.message}
          </span>
          {actions.feedback.outcome.kind === "unknown" && (
            <Button onClick={() => void actions.move(actions.feedback!.item)}>核对结果</Button>
          )}
          <Button aria-label="关闭提示" onClick={() => actions.setFeedback(null)}>
            <Close />
          </Button>
        </div>
      )}
      {creating && (
        <Sheet open title="新建 Issue" onClose={() => setCreating(false)}>
          <IssueDefinition
            onBack={() => setCreating(false)}
            onSaved={(id) => {
              setCreating(false);
              if (id) onSelect(id);
            }}
          />
        </Sheet>
      )}
    </section>
  );
}

function BoardColumn({
  status,
  label,
  count,
  mobile,
  over,
  closed,
  collapsed,
  onToggle,
  children,
  ...drag
}: {
  status: IssueWorkStatus;
  label: string;
  count?: number | undefined;
  mobile: boolean;
  over: boolean;
  closed: boolean;
  collapsed: boolean;
  onToggle?: (() => void) | undefined;
  children(item: IssueSummary): ReactNode;
  onDragOver: DragEventHandler;
  onDrop: DragEventHandler;
}) {
  const { live } = useServices();
  const column = live.issues.board.columns.get(status)!;
  const state = useStore(column.read.state);
  const hiddenClosed = !closed && (status === "done" || status === "cancelled");
  return (
    <section
      className={styles.column}
      data-mobile={mobile}
      data-collapsed={collapsed}
      data-over={over}
      aria-label={`${label}列`}
      {...drag}
    >
      <h2>
        <span className={styles.dot} data-status={status} />
        {onToggle ? (
          <Button
            variant="ghost"
            size="s"
            className={styles.columnToggle}
            aria-expanded={!collapsed}
            onClick={onToggle}
          >
            <span className={styles.columnLabel}>{label}</span>
            <Badge>{count ?? "…"}</Badge>
          </Button>
        ) : (
          <>
            {label}
            <span>{count ?? "…"}</span>
          </>
        )}
      </h2>
      <div className={styles.cardList}>
        {state.error && (
          <p className={styles.readError} role="status">
            读取失败，保留上次记录。
            <Button onClick={() => void column.read.refresh()}>重新读取</Button>
          </p>
        )}
        {(state.value?.items ?? []).map(children)}
        {!state.value && !state.error && <p className={styles.empty}>正在读取…</p>}
        {state.value && !state.value.items?.length && (
          <EmptyState className={styles.empty}>
            {hiddenClosed ? "勾选“包含已关闭”后查看" : "暂无 Issue"}
          </EmptyState>
        )}
        {state.value?.more && (
          <Button
            className={styles.more}
            disabled={state.loading}
            onClick={() => void column.more()}
          >
            {state.loading ? "正在读取…" : "加载更多"}
          </Button>
        )}
      </div>
    </section>
  );
}

const BoardCard = memo(function BoardCard({
  item,
  state,
  disabled,
  onSelect,
  onMenu,
  onReconcile,
  onDragStart,
  onDragEnd,
}: {
  item: IssueSummary;
  state?: "saving" | "unknown" | undefined;
  disabled: boolean;
  onSelect(id: string): void;
  onMenu(item: IssueSummary): void;
  onReconcile(): void;
  onDragStart: DragEventHandler;
  onDragEnd: DragEventHandler;
}) {
  const b = item.board;
  return (
    <article
      className={styles.card}
      draggable={!disabled}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      data-issue-id={item.issue_id}
      aria-busy={state === "saving"}
    >
      <div className={styles.cardHead}>
        <span title={item.project}>{item.project || "未分组"}</span>
        <Button
          variant="ghost"
          aria-label={`修改工作状态：${item.title}`}
          disabled={disabled}
          onClick={() => onMenu(item)}
        >
          ···
        </Button>
      </div>
      <Button variant="ghost" className={styles.cardTitle} onClick={() => onSelect(item.issue_id)}>
        {item.title}
      </Button>
      <p className={styles.stage}>
        {b?.stages_total
          ? `${b.stage_title || "阶段计划"} · ${b.stages_done}/${b.stages_total} 已完成`
          : "尚未制定阶段"}
      </p>
      {!!b?.stages_total && (
        <div className={styles.progress} aria-label={`阶段完成 ${b.stages_done}/${b.stages_total}`}>
          {Array.from({ length: b.stages_total }, (_, index) => (
            <span key={index} data-complete={index < b.stages_done} />
          ))}
        </div>
      )}
      <div className={styles.cardFooter}>
        <span className={styles.assignee}>
          <span className={styles.avatar} aria-hidden>
            <AssigneeName assignee={item.assignee} initial />
          </span>
          <AssigneeName assignee={item.assignee} />
        </span>
        <Badge
          className={styles.execution}
          tone={b?.execution.unknown || b?.execution.waiting_owner ? "warn" : "neutral"}
        >
          {executionLabel(b?.execution)}
        </Badge>
      </div>
      {(item.state === "paused" || item.state === "cancelling" || !!b?.attention_count) && (
        <div className={styles.flags}>
          {item.state === "paused" && <Badge tone="warn">暂停新执行</Badge>}
          {item.state === "cancelling" && <Badge tone="warn">取消收尾中</Badge>}
          {!!b?.attention_count && <Badge tone="warn">{b.attention_count} 项待处理</Badge>}
        </div>
      )}
      {state === "saving" ? (
        <p className={styles.pending}>正在保存状态…</p>
      ) : state === "unknown" ? (
        <Button className={styles.reconcile} onClick={onReconcile}>
          结果待确认 · 核对原操作
        </Button>
      ) : (
        !!b?.notification_due_at && <NotificationClock due={b.notification_due_at} />
      )}
    </article>
  );
});

function NotificationClock({ due }: { due: number }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  return (
    <p className={styles.pending}>
      <span aria-hidden>◷</span> {notificationLabel(due, now)}
    </p>
  );
}

function BoardDialog({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose(): void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const focus = document.activeElement;
    ref.current?.showModal();
    return () => {
      if (focus instanceof HTMLElement && focus.isConnected) focus.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={styles.dialog}
      aria-label={title}
      onCancel={onClose}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className={styles.dialogHead}>
        <h2>{title}</h2>
        <Button onClick={onClose} aria-label="关闭">
          <Close />
        </Button>
      </div>
      {children}
    </dialog>
  );
}

function readBlockers(details: unknown): IssueBlocker[] {
  if (
    !details ||
    typeof details !== "object" ||
    !("blockers" in details) ||
    !Array.isArray(details.blockers)
  )
    return [];
  return details.blockers.filter(
    (b): b is IssueBlocker =>
      !!b && typeof b === "object" && typeof b.code === "string" && typeof b.object_id === "string",
  );
}
