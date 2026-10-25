import { useEffect, useId, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { WorkItemView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Back } from "../shell/icons.tsx";
import { queueReasonText } from "../tasks/execution.ts";
import { ExecutionSummary, TaskExecution } from "../tasks/Execution.tsx";
import { BoardResources } from "../tasks/Board.tsx";
import { UpgradeBanner } from "../upgrades/Upgrade.tsx";
import { columns, columnLabel, concern } from "./model.ts";
import { StageList } from "./Details.tsx";
import controls from "../shell/controls.module.css";
import list from "../shell/list.module.css";
import styles from "./WorkItems.module.css";

export function WorkItemOverview() {
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
        .slice(0, 6)
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
  onRepos,
  onRecords,
}: {
  active: boolean;
  connection: Connection;
  onChat(): void;
  onRepos(): void;
  onRecords(): void;
}) {
  const { live } = useServices();
  const items = live.workItems;
  const state = useStore(items.board.state);
  const project = useStore(items.project);
  const snapshot = useStore(live.snapshot);
  const [mobileColumn, setMobileColumn] = useState<string>("unplanned");
  const filterId = useId();
  useEffect(() => {
    if (active) items.openBoard();
    else items.closeBoard();
    return () => items.closeBoard();
  }, [items, active]);
  const rows = state.value?.items ?? [];
  return (
    <>
      <header className={styles.head}>
        <button type="button" className={controls.chipBtn} onClick={onChat}>
          <Back width={14} height={14} />
          返回聊天
        </button>
        <h1>工作项</h1>
        <label className={styles.filter}>
          项目
          <input
            aria-label="按项目筛选工作项"
            list={filterId}
            value={project}
            placeholder="全部项目"
            onChange={(event) => items.setProject(event.target.value)}
          />
        </label>
        <datalist id={filterId}>
          {snapshot?.projects?.map((p) => (
            <option key={p.project} value={p.project}>
              {p.title}
            </option>
          ))}
        </datalist>
        {project && (
          <button type="button" className={controls.link} onClick={() => items.setProject("")}>
            全部项目
          </button>
        )}
        <button type="button" className={controls.chipBtn} onClick={() => items.refresh()}>
          刷新工作项
        </button>
        <button type="button" className={controls.chipBtn} onClick={onRecords}>
          执行记录
        </button>
      </header>
      <UpgradeBanner connection={connection} />
      <BoardResources connection={connection} onRepos={onRepos} compact />
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
      <p className={styles.description}>工作项持续跟进目标。任务完成后，未定的后续仍会留在这里。</p>
      <div className={styles.mobileTabs} role="tablist" aria-label="工作项状态">
        {columns.map((column) => (
          <button
            type="button"
            role="tab"
            id={`${filterId}-${column.id}`}
            aria-controls={`${filterId}-panel-${column.id}`}
            aria-selected={mobileColumn === column.id}
            key={column.id}
            onClick={() => setMobileColumn(column.id)}
          >
            {column.title}
            <span>{rows.filter((item) => item.column === column.id).length}</span>
          </button>
        ))}
      </div>
      <div className={styles.board} role="region" aria-label="工作项白板" aria-busy={state.loading}>
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
                <span>{cards.length}</span>
              </div>
              <div className={styles.cards}>
                {cards.map((item) => (
                  <WorkItemCard key={item.work_item_id} item={item} />
                ))}
                {!cards.length && (
                  <p className={styles.empty}>{state.loading ? "正在读取…" : "暂无工作项"}</p>
                )}
              </div>
            </section>
          );
        })}
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
      <span className={styles.meta}>
        {item.project || "未分组"} · 修订 {item.revision}
      </span>
      <ExecutionSummary counts={item.execution} />
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
          <summary>{stages.length} 个阶段</summary>
          <StageList item={item} compact />
        </details>
      )}
      <button
        type="button"
        className={styles.cardMore}
        onClick={() => openWorkItem(item.work_item_id)}
      >
        背景、后续与交付 ›
      </button>
    </article>
  );
}
