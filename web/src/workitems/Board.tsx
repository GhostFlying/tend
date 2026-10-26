import { useEffect, useId, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { WorkItemView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Back, Chevron, Close } from "../shell/icons.tsx";
import { queueReasonText } from "../tasks/execution.ts";
import { ExecutionSummary, TaskExecution } from "../tasks/Execution.tsx";
import { BoardResourceEntry } from "./Resources.tsx";
import { UpgradeBanner } from "../upgrades/Upgrade.tsx";
import { columns, columnLabel, concern } from "./model.ts";
import { StageList } from "./Details.tsx";
import controls from "../shell/controls.module.css";
import list from "../shell/list.module.css";
import styles from "./WorkItems.module.css";

export function WorkItemOverview({ onAll }: { onAll(): void }) {
  const { live, openWorkItem } = useServices();
  const source = live.workItems.overview;
  const state = useStore(source.state);
  useEffect(() => live.workItems.startOverview(), [live]);
  const items = state.value?.items ?? [];
  const concerns = items.filter(
    (item) => item.column === "attention" || item.column === "unplanned",
  );
  return (
    <section aria-label="工作项概览">
      <div className={list.sectionTitle}>
        持续跟进 <span>· {items.length}</span>
      </div>
      {state.error && (
        <p className={controls.empty} role="alert">
          读取工作项失败{" "}
          <button className={controls.link} onClick={() => void source.refresh()}>
            重试
          </button>
        </p>
      )}
      {[
        ...concerns.filter((item) => item.column === "attention"),
        ...concerns.filter((item) => item.column === "unplanned"),
        ...items.filter((item) => !concerns.includes(item)),
      ]
        .slice(0, 3)
        .map((item) => (
          <button
            type="button"
            key={item.work_item_id}
            className={styles.overview}
            onClick={() => openWorkItem(item.work_item_id)}
          >
            <span className={styles.overviewTitle}>
              <strong>{item.title}</strong>
              <span>{columnLabel(item.column)}</span>
            </span>
            <span className={styles.overviewHint}>{concern(item)}</span>
            <ExecutionSummary counts={item.execution} />
          </button>
        ))}
      {items.length > 3 && (
        <button
          type="button"
          className={styles.overviewMore}
          onClick={() => {
            live.workItems.setProject("");
            onAll();
          }}
        >
          查看全部 {items.length} 个工作项 ›
        </button>
      )}
      {!items.length && !state.error && (
        <p className={controls.empty}>
          {state.loading ? "正在读取工作项…" : "暂无持续跟进的工作项。"}
        </p>
      )}
    </section>
  );
}

export function WorkItemBoard({
  active,
  connection,
  onChat,
  onRecords,
}: {
  active: boolean;
  connection: Connection;
  onChat(): void;
  onRecords(): void;
}) {
  const { live } = useServices();
  const items = live.workItems;
  const state = useStore(items.board.state);
  const project = useStore(items.project);
  const snapshot = useStore(live.snapshot);
  const [mobileColumn, setMobileColumn] = useState<string>("unplanned");
  const filterId = useId();
  const filter = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (active) items.openBoard();
    else items.closeBoard();
    return () => items.closeBoard();
  }, [items, active]);
  const rows = state.value?.items ?? [];
  return (
    <>
      <header className={styles.head}>
        <button
          type="button"
          className={`${controls.chipBtn} ${styles.desktopOnly}`}
          onClick={onChat}
        >
          <Back width={14} height={14} />
          返回聊天
        </button>
        <h1>工作项</h1>
        <button
          type="button"
          className={`${controls.chipBtn} ${styles.desktopOnly}`}
          onClick={onRecords}
        >
          任务执行
        </button>
        <BoardResourceEntry connection={connection} onRecords={onRecords} />
      </header>
      <div className={styles.reading}>
        <UpgradeBanner connection={connection} />
        <div className={styles.intro}>
          <div className={styles.projectRow}>
            <label className={styles.filter}>
              项目
              <input
                ref={filter}
                aria-label="按项目筛选工作项"
                list={filterId}
                value={project}
                placeholder="全部项目"
                onChange={(event) => items.setProject(event.target.value)}
              />
              {project && (
                <button
                  type="button"
                  className={styles.clearFilter}
                  aria-label="清除项目筛选"
                  onClick={() => {
                    items.setProject("");
                    filter.current?.focus();
                  }}
                >
                  <Close width={18} height={18} />
                </button>
              )}
            </label>
            <datalist id={filterId}>
              {snapshot?.projects?.map((p) => (
                <option key={p.project} value={p.project}>
                  {p.title}
                </option>
              ))}
            </datalist>

            <button
              type="button"
              className={styles.refreshButton}
              aria-label="刷新工作项"
              onClick={() => items.refresh()}
            >
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                aria-hidden="true"
              >
                <path d="M20 7v5h-5M20 12a8 8 0 1 0-2 6" />
              </svg>
            </button>
          </div>
          <p className={styles.description}>持续跟进目标，本轮完成后仍保留未定的后续。</p>
        </div>
        {connection.status !== "online" && (
          <p className={styles.notice} role="status">
            等待连接恢复 · 当前显示最后读取的工作项
          </p>
        )}
        {state.error && (
          <p className={styles.notice} role="alert">
            读取工作项失败：{state.error}{" "}
            <button className={controls.link} onClick={() => void items.board.refresh()}>
              重试工作项
            </button>
          </p>
        )}
        <div className={styles.mobileTabs} role="tablist" aria-label="工作项状态">
          {columns.map((column) => (
            <button
              type="button"
              role="tab"
              id={`${filterId}-${column.id}`}
              aria-controls={`${filterId}-panel-${column.id}`}
              aria-selected={mobileColumn === column.id}
              tabIndex={mobileColumn === column.id ? 0 : -1}
              onKeyDown={(event) => {
                const index = columns.findIndex((entry) => entry.id === column.id);
                const offset =
                  event.key === "ArrowLeft"
                    ? -1
                    : event.key === "ArrowRight"
                      ? 1
                      : event.key === "ArrowUp"
                        ? -3
                        : event.key === "ArrowDown"
                          ? 3
                          : undefined;
                const next =
                  event.key === "Home"
                    ? 0
                    : event.key === "End"
                      ? columns.length - 1
                      : offset === undefined
                        ? undefined
                        : (index + offset + columns.length) % columns.length;
                if (next === undefined) return;
                event.preventDefault();
                const selected = columns[next]!;
                setMobileColumn(selected.id);
                document.getElementById(`${filterId}-${selected.id}`)?.focus();
              }}
              key={column.id}
              onClick={() => setMobileColumn(column.id)}
            >
              {column.title}
              <span>{rows.filter((item) => item.column === column.id).length}</span>
            </button>
          ))}
        </div>
        <div
          className={styles.board}
          role="region"
          aria-label="工作项白板"
          aria-busy={state.loading}
        >
          {columns.map((column) => {
            const cards = rows.filter((item) => item.column === column.id);
            return (
              <section
                key={column.id}
                className={styles.column}
                data-mobile-active={mobileColumn === column.id}
                id={`${filterId}-panel-${column.id}`}
                aria-label={column.title}
              >
                <div className={styles.columnHead}>
                  <h2>{column.title}</h2>
                  <span>
                    {cards.length}
                    <span className={styles.mobileOnly}> 个工作项</span>
                  </span>
                </div>
                <div className={styles.cards}>
                  {cards.map((item) => (
                    <WorkItemCard key={item.work_item_id} item={item} />
                  ))}
                  {!cards.length && (
                    <div className={styles.empty}>
                      {!state.loading && !state.error && (
                        <span className={styles.emptyIcon}>
                          <svg
                            width="24"
                            height="24"
                            viewBox="0 0 24 24"
                            fill="none"
                            stroke="currentColor"
                            strokeWidth="1.6"
                            aria-hidden="true"
                          >
                            <rect x="4" y="4" width="16" height="16" rx="3" />
                            <path d="M8 9h8M8 13h5" />
                          </svg>
                        </span>
                      )}
                      <p className={styles.emptyTitle}>
                        {state.loading
                          ? "正在读取工作项…"
                          : state.error
                            ? "工作项尚未读取成功"
                            : !project && !rows.length
                              ? "暂无持续跟进的工作项"
                              : `暂无${column.title}的工作项`}
                      </p>
                      {!state.loading && !state.error && (
                        <>
                          <p className={styles.meta}>
                            {!project && !rows.length
                              ? "可以回到对话，讨论要推进的目标。"
                              : "其他项目和状态的工作项仍保留在账本中。"}
                          </p>
                          <button
                            className={styles.emptyAction}
                            onClick={() => {
                              if (project) {
                                items.setProject("");
                                filter.current?.focus();
                              } else if (!rows.length) onChat();
                              else {
                                setMobileColumn("unplanned");
                                document.getElementById(`${filterId}-unplanned`)?.focus();
                              }
                            }}
                          >
                            {project ? "查看全部项目" : !rows.length ? "回到对话" : "查看待安排"}
                          </button>
                        </>
                      )}
                    </div>
                  )}
                </div>
              </section>
            );
          })}
        </div>
      </div>
    </>
  );
}
function WorkItemCard({ item }: { item: WorkItemView }) {
  const { openWorkItem, openTask } = useServices();
  const stages = item.stages ?? [];
  const tasks = item.tasks ?? [];
  const openFollowOns = item.follow_ons?.filter((next) => next.status === "open").length ?? 0;
  return (
    <article className={styles.card}>
      <button
        type="button"
        className={styles.cardTitle}
        onClick={() => openWorkItem(item.work_item_id)}
      >
        {item.title}
      </button>
      <div className={styles.cardMeta}>
        <span className={styles.meta}>
          {item.project || "未分组"} · 修订 {item.revision}
        </span>
        <span className={styles.cardState} data-column={item.column}>
          {columnLabel(item.column)}
        </span>
      </div>
      <div className={styles.cardExecution}>
        <ExecutionSummary counts={item.execution} />
      </div>
      <p className={styles.concern} data-attention={item.column === "attention"}>
        {concern(item)}
      </p>
      <div className={styles.facts}>
        <span>
          {tasks.filter((task) => task.lifecycle === "completed").length}/{tasks.length} 任务完成
        </span>
        {openFollowOns > 0 && <span>{openFollowOns} 项后续待处理</span>}
        {!!item.deliveries?.length && <span>{item.deliveries.length} 项交付跟踪</span>}
      </div>
      {tasks.some((task) => queueReasonText(task)) && (
        <div className={styles.waitingTasks} aria-label="执行等待与原因">
          {tasks
            .filter((task) => queueReasonText(task))
            .slice(0, 2)
            .map((task) => (
              <div key={task.task_id}>
                <button
                  type="button"
                  className={styles.waitingTaskTitle}
                  onClick={() => openTask(task.task_id)}
                >
                  {task.title}
                </button>
                <TaskExecution task={task} />
              </div>
            ))}
          {tasks.filter((task) => queueReasonText(task)).length > 2 && (
            <span className={styles.meta}>更多等待原因可在阶段中查看</span>
          )}
        </div>
      )}
      {!!stages.length && (
        <details className={styles.cardStages}>
          <summary>
            {stages.length} 个阶段
            <Chevron width={16} height={16} />
          </summary>
          <StageList item={item} compact />
        </details>
      )}
      <button
        type="button"
        className={styles.cardMore}
        onClick={() => openWorkItem(item.work_item_id)}
      >
        背景、后续与交付 <Chevron width={16} height={16} />
      </button>
    </article>
  );
}
