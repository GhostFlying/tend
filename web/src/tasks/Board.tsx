import { ResourceNavigation } from "../shell/Resources.tsx";
import { UpgradeBanner } from "../upgrades/Upgrade.tsx";
import { useEffect, useId, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { TaskSummary } from "../api/generated/api.ts";
import { TaskExecution } from "./Execution.tsx";
import { executionLook } from "./execution.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Button } from "../ui/Button.tsx";
import { EmptyState } from "../ui/Surfaces.tsx";
import list from "../shell/list.module.css";
import { Hosts } from "../hosts/Hosts.tsx";
import { hostMaintaining } from "../hosts/status.ts";
import { Back } from "../shell/icons.tsx";
import { taskTabs, type TaskFeed } from "./lists.ts";
import { controlLabels, ratingLabels, taskLook, isFinished } from "./status.ts";
import styles from "./Board.module.css";

export function RecentTasks({
  limit = 5,
  detailed = false,
  expanded = false,
}: {
  limit?: number;
  detailed?: boolean;
  expanded?: boolean;
}) {
  const { live, openTask } = useServices();
  const feed = live.tasks.recent;
  const state = useStore(feed.state);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!detailed) return;
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(timer);
  }, [detailed]);
  useEffect(() => {
    feed.start();
  }, [feed]);
  return (
    <section aria-label={detailed ? "最近完成的任务" : "最近完成"}>
      <div className={list.sectionTitle}>
        <span>{detailed ? "最近完成的任务" : "最近完成"}</span>
        {detailed && <span>最近 {Math.min(limit, state.items.length)} 条</span>}
      </div>
      {state.error && (
        <EmptyState>
          读取失败{" "}
          <Button variant="link" onClick={() => void feed.refresh()}>
            重试最近完成
          </Button>
        </EmptyState>
      )}
      {state.items.slice(0, limit).map((task) => (
        <button
          type="button"
          key={task.task_id}
          className={styles.recentTask}
          data-expanded={expanded || undefined}
          title={task.title}
          onClick={() => openTask(task.task_id)}
        >
          {detailed ? (
            <>
              <span className={styles.completedIcon} aria-hidden>
                ✓
              </span>
              <span className={styles.recentBody}>
                <span className={styles.recentHead}>
                  <strong>{task.title}</strong>
                  {task.completed_at ? (
                    <time
                      dateTime={new Date(task.completed_at).toISOString()}
                      title={new Date(task.completed_at).toLocaleString()}
                    >
                      {completionTime(task.completed_at, now)}
                    </time>
                  ) : (
                    <span className={styles.recentTime}>时间未记录</span>
                  )}
                </span>
                <span className={styles.recentParent}>{parentLabel(task)}</span>
              </span>
            </>
          ) : (
            <>
              <span className={styles.recentBody}>
                <span className={styles.recentHead}>
                  <strong>{task.title}</strong>
                  <span className={styles.recentTime}>已完成</span>
                </span>
                <span className={styles.recentParent}>{parentLabel(task)}</span>
              </span>
            </>
          )}
        </button>
      ))}
      {!state.items.length && !state.error && (
        <EmptyState>{state.loading ? "正在读取…" : "暂无已完成任务。"}</EmptyState>
      )}
    </section>
  );
}

function completionTime(at: number, now: number): string {
  const minutes = Math.max(0, Math.floor((now - at) / 60_000));
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时前`;
  return new Date(at).toLocaleDateString();
}

/** Shared mobile resources, independent of either board's project filter. */
export function BoardResources({
  connection,
  onRepos,
}: {
  connection: Connection;
  onRepos(): void;
}) {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  return (
    <details className={styles.mobileRecent}>
      <summary>资源与记录</summary>
      <div className={styles.resourceContent}>
        <ResourceNavigation onRepos={onRepos} records onRecords={() => {}} />
        <details>
          <summary>
            执行主机
            {snapshot && ` · ${snapshot.hosts?.length ?? 0} 台`}
            {!!snapshot?.hosts?.filter(hostMaintaining).length &&
              ` · ${snapshot.hosts.filter(hostMaintaining).length} 台维护`}
          </summary>
          <div className={styles.mobileList}>
            <Hosts connection={connection} />
          </div>
        </details>
        <details>
          <summary>最近完成的任务</summary>
          <div className={styles.mobileList}>
            <RecentTasks />
          </div>
        </details>
      </div>
    </details>
  );
}

export function TaskBoard({
  active,
  connection,
  onChat,
  onRepos,
  backLabel = "返回聊天",
}: {
  active: boolean;
  connection: Connection;
  onChat(): void;
  backLabel?: string;
  onRepos(): void;
}) {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const project = useStore(live.tasks.project);
  const id = useId();
  const [tab, setTab] = useState(0);
  useEffect(() => {
    if (active) live.tasks.open(tab);
    else live.tasks.close();
  }, [live, active, tab]);
  return (
    <>
      <header className={styles.head}>
        <Button type="button" variant="secondary" size="s" onClick={onChat}>
          <Back width={14} height={14} />
          {backLabel}
        </Button>
        <h1>任务执行</h1>
        <label className={styles.filter}>
          项目
          <input
            aria-label="按项目筛选"
            list={id}
            value={project}
            placeholder="全部项目"
            onChange={(e) => live.tasks.setProject(e.target.value.trim())}
          />
        </label>
        <datalist id={id}>
          {snapshot?.projects?.map((p) => (
            <option key={p.project} value={p.project}>
              {p.title}
            </option>
          ))}
        </datalist>
        {project && (
          <Button type="button" variant="link" onClick={() => live.tasks.setProject("")}>
            全部项目
          </Button>
        )}
        <Button type="button" variant="secondary" size="s" onClick={() => live.tasks.refresh()}>
          刷新任务
        </Button>
      </header>
      <UpgradeBanner connection={connection} />
      <BoardResources connection={connection} onRepos={onRepos} />
      <section className={styles.board} aria-label="任务执行列表">
        <p className={styles.note}>按任务查看执行与历史；Issue计划在Issue白板中跟进。</p>
        <div className={styles.tabs} role="tablist" aria-label="任务范围">
          {taskTabs.map((entry, index) => (
            <button
              key={entry.id}
              id={`${id}-${entry.id}`}
              type="button"
              role="tab"
              aria-selected={tab === index}
              aria-controls={`${id}-tasks`}
              tabIndex={tab === index ? 0 : -1}
              onClick={() => setTab(index)}
              onKeyDown={(event) => {
                const next =
                  event.key === "ArrowRight"
                    ? (index + 1) % taskTabs.length
                    : event.key === "ArrowLeft"
                      ? (index + taskTabs.length - 1) % taskTabs.length
                      : event.key === "Home"
                        ? 0
                        : event.key === "End"
                          ? taskTabs.length - 1
                          : undefined;
                if (next === undefined) return;
                event.preventDefault();
                setTab(next);
                document.getElementById(`${id}-${taskTabs[next]!.id}`)?.focus();
              }}
            >
              {entry.title}
            </button>
          ))}
        </div>
        <div
          className={styles.taskPanel}
          id={`${id}-tasks`}
          role="tabpanel"
          aria-labelledby={`${id}-${taskTabs[tab]!.id}`}
          tabIndex={0}
        >
          <TaskRows feed={live.tasks.feeds[tab]!} />
        </div>
      </section>
    </>
  );
}
function TaskRows({ feed }: { feed: TaskFeed }) {
  const state = useStore(feed.state);
  return (
    <>
      <p className={styles.note} role="status">
        已加载 {state.items.length} 个任务{state.next ? " · 还有更多" : ""}
        {state.loading ? " · 正在更新…" : ""}
      </p>
      {state.error && (
        <div role="alert" className={styles.note}>
          {state.error}
          <Button variant="link" onClick={() => void feed.refresh()}>
            重试任务列表
          </Button>
        </div>
      )}
      <div className={styles.cards} aria-busy={state.loading}>
        {state.items.map((task) => (
          <TaskCard key={task.task_id} task={task} />
        ))}
        {!state.items.length && !state.loading && !state.error && (
          <p className={styles.note}>暂无任务</p>
        )}
        {state.next && (
          <button
            type="button"
            className={styles.more}
            disabled={state.loading || state.paging || !!state.error}
            onClick={() => void feed.more()}
          >
            {state.paging ? "正在读取…" : "加载更多任务"}
          </button>
        )}
      </div>
    </>
  );
}
function parentLabel(task: TaskSummary): string {
  return task.primary_issue_id
    ? `所属Issue：${task.primary_issue_title ?? task.primary_issue_id}`
    : task.primary_work_item_id
      ? `历史关联：${task.primary_work_item_title ?? task.primary_work_item_id}`
      : "未关联Issue";
}
function TaskCard({ task }: { task: TaskSummary }) {
  const { openTask, openIssue } = useServices();
  const look = task.execution ? executionLook(task.execution) : taskLook(task);
  return (
    <article className={styles.card}>
      <div className={styles.cardBody}>
        <div className={styles.taskHeading}>
          <button
            type="button"
            className={styles.cardOpen}
            onClick={() => openTask(task.task_id)}
            title={task.title}
          >
            <strong>{task.title}</strong>
          </button>
          <span className={styles.state} data-tone={look.tone}>
            {look.label}
          </span>
          {task.control_mode !== "managed" && (
            <span className={styles.meta}>{controlLabels[task.control_mode]}</span>
          )}
        </div>
        <div className={styles.taskMeta}>
          <span className={styles.meta}>
            {task.project || "未分组"} · {ratingLabels[task.rating]} · spec {task.spec_version}
          </span>
          {task.primary_issue_id ? (
            <button className={styles.parentLink} onClick={() => openIssue(task.primary_issue_id!)}>
              {parentLabel(task)} ›
            </button>
          ) : (
            <span className={styles.meta}>{parentLabel(task)}</span>
          )}
        </div>
        <TaskExecution task={task} showState={false} />
      </div>
      <button className={styles.resultLink} onClick={() => openTask(task.task_id)}>
        {isFinished(task)
          ? task.lifecycle === "completed"
            ? "查看结果与验收 ›"
            : "查看取消记录 ›"
          : "查看执行详情 ›"}
      </button>
    </article>
  );
}
