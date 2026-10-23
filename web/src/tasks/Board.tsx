import { UpgradeBanner } from "../upgrades/Upgrade.tsx";
import { useEffect, useId } from "react";
import type { Connection } from "../api/client.ts";
import type { TaskSummary } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import controls from "../shell/controls.module.css";
import list from "../shell/list.module.css";
import { Hosts } from "../hosts/Hosts.tsx";
import { hostMaintaining } from "../hosts/status.ts";
import { Back, Gear } from "../shell/icons.tsx";
import { stages, type TaskFeed } from "./lists.ts";
import { blockLabels, controlLabels, ratingLabels } from "./status.ts";
import styles from "./Board.module.css";

export function RecentTasks() {
  const { live, openTask } = useServices();
  const feed = live.tasks.recent;
  const state = useStore(feed.state);
  useEffect(() => {
    feed.start();
  }, [feed]);
  return (
    <section aria-label="最近完成">
      <div className={list.sectionTitle}>最近完成</div>
      {state.error && (
        <p className={controls.empty}>
          读取失败{" "}
          <button className={controls.link} onClick={() => void feed.refresh()}>
            重试最近完成
          </button>
        </p>
      )}
      {state.items.slice(0, 5).map((task) => (
        <button
          type="button"
          key={task.task_id}
          className={list.row}
          title={task.title}
          onClick={() => openTask(task.task_id)}
        >
          <span className={list.label}>{task.title}</span>
          <span className={list.meta}>已完成</span>
        </button>
      ))}
      {!state.items.length && !state.error && (
        <p className={controls.empty}>{state.loading ? "正在读取…" : "暂无已完成任务。"}</p>
      )}
    </section>
  );
}

export function TaskBoard({
  active,
  connection,
  onChat,
  onSettings,
}: {
  active: boolean;
  connection: Connection;
  onChat(): void;
  onSettings(): void;
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
          返回聊天
        </button>
        <h1>任务看板</h1>
        <button
          type="button"
          className={controls.iconBtn}
          aria-label="设置"
          title="设置"
          onClick={onSettings}
        >
          <Gear />
        </button>
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
      <div className={styles.mobileRecent}>
        <details>
          <summary>最近完成</summary>
          <RecentTasks />
        </details>
        <details>
          <summary>
            执行主机
            {!!snapshot?.hosts?.filter(hostMaintaining).length &&
              ` · ${snapshot.hosts.filter(hostMaintaining).length} 台维护`}
          </summary>
          <Hosts connection={connection} />
        </details>
      </div>
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
    <button
      type="button"
      className={styles.card}
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
  );
}
