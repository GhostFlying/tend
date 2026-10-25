import { RepositoryEntry } from "../repos/Repositories.tsx";
import { UpgradeBanner } from "../upgrades/Upgrade.tsx";
import { useEffect, useId, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { TaskSummary } from "../api/generated/api.ts";
import { TaskExecution } from "./Execution.tsx";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import controls from "../shell/controls.module.css";
import list from "../shell/list.module.css";
import { Hosts } from "../hosts/Hosts.tsx";
import { hostMaintaining } from "../hosts/status.ts";
import { Back } from "../shell/icons.tsx";
import { stages, type TaskFeed } from "./lists.ts";
import { blockLabels, controlLabels, ratingLabels } from "./status.ts";
import styles from "./Board.module.css";

export function RecentTasks({
  limit = 5,
  detailed = false,
}: {
  limit?: number;
  detailed?: boolean;
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
        <p className={controls.empty}>
          读取失败{" "}
          <button className={controls.link} onClick={() => void feed.refresh()}>
            重试最近完成
          </button>
        </p>
      )}
      {state.items.slice(0, limit).map((task) => (
        <button
          type="button"
          key={task.task_id}
          className={detailed ? styles.recentTask : list.row}
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
                <span className={styles.recentParent}>
                  {task.primary_work_item_id
                    ? `所属工作项：${task.primary_work_item_title ?? task.primary_work_item_id}`
                    : "未关联工作项"}
                </span>
              </span>
            </>
          ) : (
            <>
              <span className={list.label}>{task.title}</span>
              <span className={list.meta}>已完成</span>
            </>
          )}
        </button>
      ))}
      {!state.items.length && !state.error && (
        <p className={controls.empty}>{state.loading ? "正在读取…" : "暂无已完成任务。"}</p>
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
  compact = false,
}: {
  connection: Connection;
  onRepos(): void;
  compact?: boolean;
}) {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  return (
    <div className={styles.mobileRecent} data-compact={compact}>
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
      <RepositoryEntry onOpen={onRepos} />
      <details>
        <summary>最近完成</summary>
        <div className={styles.mobileList}>
          <RecentTasks />
        </div>
      </details>
    </div>
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
  useEffect(() => {
    if (active) live.tasks.open();
    else live.tasks.close();
  }, [live, active]);
  return (
    <>
      <header className={styles.head}>
        <button type="button" className={controls.chipBtn} onClick={onChat}>
          <Back width={14} height={14} />
          {backLabel}
        </button>
        <h1>执行记录</h1>
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
          <button type="button" className={controls.link} onClick={() => live.tasks.setProject("")}>
            全部项目
          </button>
        )}
        <button type="button" className={controls.chipBtn} onClick={() => live.tasks.refresh()}>
          刷新看板
        </button>
      </header>
      <UpgradeBanner connection={connection} />
      <BoardResources connection={connection} onRepos={onRepos} />
      <div className={styles.board} role="region" aria-label="分阶段任务看板" tabIndex={0}>
        {stages.map((stage, i) => (
          <Column key={stage.id} title={stage.title} feed={live.tasks.columns[i]!} />
        ))}
      </div>
    </>
  );
}
function Column({ title, feed }: { title: string; feed: TaskFeed }) {
  const state = useStore(feed.state);
  return (
    <section className={styles.column} aria-label={title} aria-busy={state.loading}>
      <div className={styles.columnHead}>
        <h2>{title}</h2>
        <span>已加载 {state.items.length}</span>
      </div>
      <div className={styles.cards}>
        {state.loading && <p className={styles.note}>正在更新…</p>}
        {state.error && (
          <div role="alert" className={styles.note}>
            {state.error}
            <button className={controls.link} onClick={() => void feed.refresh()}>
              重试{title}
            </button>
          </div>
        )}
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
            {state.paging ? "正在读取…" : `加载更多${title}`}
          </button>
        )}
      </div>
    </section>
  );
}
function TaskCard({ task }: { task: TaskSummary }) {
  const { openTask } = useServices();
  return (
    <article className={styles.card}>
      <button
        type="button"
        className={styles.cardOpen}
        onClick={() => openTask(task.task_id)}
        title={task.title}
      >
        <strong>{task.title}</strong>
        <span className={styles.meta}>
          {task.project || "未分组"} · {ratingLabels[task.rating]} · spec {task.spec_version}
        </span>
        <span className={styles.badges}>
          {task.control_mode !== "managed" && <span>{controlLabels[task.control_mode]}</span>}
          {task.block_reason !== "none" && <span>{blockLabels[task.block_reason]}</span>}
        </span>
      </button>
      <TaskExecution task={task} />
    </article>
  );
}
