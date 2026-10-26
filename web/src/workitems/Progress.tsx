import { useId, useRef, useState, type ReactNode } from "react";
import type { ExecutionState, WorkItemView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { executionLook, queueReasonText } from "../tasks/execution.ts";
import { isFinished } from "../tasks/status.ts";
import {
  currentState,
  progressCounts,
  selectTasks,
  uniqueTasks,
  type TaskTab,
} from "./progress.ts";
import styles from "./WorkItems.module.css";
import controls from "../shell/controls.module.css";

const tabs: { key: TaskTab; label: string }[] = [
  { key: "current", label: "当前" },
  { key: "ended", label: "已结束" },
  { key: "all", label: "全部" },
];
export function WorkItemProgress({ item, children }: { item: WorkItemView; children?: ReactNode }) {
  const { openTask } = useServices();
  const [tab, setTab] = useState<TaskTab>("current");
  const [filter, setFilter] = useState<ExecutionState | null>(null);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const id = useId();
  const tasks = uniqueTasks(item);
  const current = tasks.filter((task) => !isFinished(task));
  const ids = new Set(current.map((task) => task.task_id));
  const stages = (item.stages ?? []).filter((stage) => stage.task_ids?.some((id) => ids.has(id)));
  const ongoing = (item.stages ?? []).filter(
    (stage) => stage.status === "active" && !stage.task_ids?.some((id) => ids.has(id)),
  );
  const counts = progressCounts(tasks);
  const selected = selectTasks(tasks, tab, filter);
  const sizes = {
    current: current.length,
    ended: tasks.length - current.length,
    all: tasks.length,
  };
  return (
    <section className={styles.progress} aria-label="当前进展">
      <div className={styles.progressCounts} aria-label="执行概况">
        {counts.map(([state, count]) => (
          <button
            type="button"
            key={state}
            className={styles.progressCount}
            data-state={state}
            aria-pressed={filter === state}
            onClick={() => {
              setTab("current");
              setFilter(state);
            }}
          >
            {count} {state === "waiting_owner" ? "待你处理" : executionLook({ state }).label}
          </button>
        ))}
        {!counts.some(([state]) => state === "running") && (
          <span className={styles.meta}>暂无执行中的任务</span>
        )}
      </div>
      <div className={styles.currentSteps}>
        <h3>当前步骤</h3>
        {stages.length ? (
          <ul>
            {stages.map((stage) => (
              <li key={stage.stage_id}>{stage.title}</li>
            ))}
          </ul>
        ) : (
          <p>
            {current.length
              ? "任务已安排，阶段尚未关联"
              : item.status === "closed"
                ? "工作项已结束"
                : "本轮暂无执行任务，后续待安排"}
          </p>
        )}
        {!!ongoing.length && (
          <p className={styles.meta}>持续跟进：{ongoing.map((stage) => stage.title).join("、")}</p>
        )}
      </div>
      {children}
      <div className={styles.taskTabs} role="tablist" aria-label="关联任务筛选">
        {tabs.map(({ key, label }, index) => (
          <button
            key={key}
            ref={(node) => {
              tabRefs.current[index] = node;
            }}
            type="button"
            role="tab"
            id={`${id}-${key}`}
            aria-selected={tab === key}
            aria-controls={`${id}-tasks`}
            tabIndex={tab === key ? 0 : -1}
            onClick={() => {
              setTab(key);
              setFilter(null);
            }}
            onKeyDown={(event) => {
              const next =
                event.key === "ArrowRight"
                  ? (index + 1) % tabs.length
                  : event.key === "ArrowLeft"
                    ? (index + tabs.length - 1) % tabs.length
                    : event.key === "Home"
                      ? 0
                      : event.key === "End"
                        ? tabs.length - 1
                        : -1;
              if (next < 0) return;
              event.preventDefault();
              setTab(tabs[next]!.key);
              setFilter(null);
              tabRefs.current[next]?.focus();
            }}
          >
            {label}（{sizes[key]}）
          </button>
        ))}
      </div>
      <div id={`${id}-tasks`} role="tabpanel" aria-labelledby={`${id}-${tab}`} tabIndex={0}>
        {filter && (
          <div className={styles.filterNotice}>
            <span>筛选：{executionLook({ state: filter }).label}</span>
            <button className={controls.link} onClick={() => setFilter(null)}>
              清除状态筛选
            </button>
          </div>
        )}
        <div className={styles.progressTasks}>
          {selected.map((task) => {
            const names = (item.stages ?? [])
              .filter((stage) => stage.task_ids?.includes(task.task_id))
              .map((stage) => stage.title);
            const reason = queueReasonText(task);
            return (
              <button
                key={task.task_id}
                type="button"
                className={styles.progressTask}
                onClick={() => openTask(task.task_id)}
              >
                <span className={styles.progressTaskHead}>
                  <strong>{task.title}</strong>
                  <span data-state={currentState(task)}>
                    {executionLook({ ...task.execution, state: currentState(task) }).label}
                  </span>
                </span>
                {!!names.length && <span className={styles.meta}>{names.join(" · ")}</span>}
                {reason && <span className={styles.taskReason}>{reason}</span>}
              </button>
            );
          })}
          {!selected.length && (
            <p className={styles.empty}>
              {filter
                ? "暂无符合筛选条件的任务"
                : tab === "ended"
                  ? "暂无已结束任务"
                  : tab === "current"
                    ? "暂无未结束任务"
                    : "尚未关联任务"}
            </p>
          )}
        </div>
      </div>
    </section>
  );
}
