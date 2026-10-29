import { useEffect, useId, useState } from "react";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { IssueDefinition } from "./Definition.tsx";
import { IssueHierarchy } from "./Hierarchy.tsx";
import styles from "./Issues.module.css";
import taskStyles from "../tasks/Board.module.css";
export function IssueBoard({
  onSelect,
  onChat,
  onRecords,
  active,
}: {
  onSelect(id: string): void;
  onChat(): void;
  onRecords(): void;
  active: boolean;
}) {
  const { live, client } = useServices();
  const connection = useStore(client.connection);
  const state = useStore(live.issues.list.state);
  const [creating, setCreating] = useState(false);
  const [project, setProject] = useState("");
  const [includeClosed, setIncludeClosed] = useState(false);
  const snapshot = useStore(live.snapshot);
  const projectsID = useId();
  useEffect(() => {
    if (active)
      void live.issues.openList({ ...(project ? { project } : { parentID: "" }), includeClosed });
  }, [live, active, project, includeClosed]);
  if (creating)
    return (
      <IssueDefinition
        onBack={() => setCreating(false)}
        onSaved={(id) => {
          setCreating(false);
          if (id) onSelect(id);
        }}
      />
    );
  return (
    <section className={styles.root} aria-label="Issue白板">
      <header className={styles.header}>
        <button className={styles.button} onClick={onChat}>
          返回对话
        </button>
        <h1>Issue</h1>
        <label className={taskStyles.filter}>
          项目
          <input
            aria-label="按项目筛选Issue"
            list={projectsID}
            value={project}
            placeholder="全部项目"
            onChange={(event) => setProject(event.target.value.trim())}
          />
        </label>
        <datalist id={projectsID}>
          {snapshot?.projects?.map((item) => (
            <option key={item.project} value={item.project}>
              {item.title}
            </option>
          ))}
        </datalist>
        {project && (
          <button className={styles.button} onClick={() => setProject("")}>
            全部项目
          </button>
        )}
        <label className={styles.check}>
          <input
            type="checkbox"
            checked={includeClosed}
            onChange={(event) => setIncludeClosed(event.target.checked)}
          />
          包含已结束
        </label>
        <span className={styles.spacer} />
        <button
          className={styles.button}
          disabled={state.loading}
          onClick={() => void live.issues.list.refresh()}
        >
          刷新Issue
        </button>
        <button className={styles.button} onClick={() => setCreating(true)}>
          新建Issue
        </button>
        <button onClick={onRecords}>任务执行记录 →</button>
      </header>
      <div className={styles.body}>
        <p className={styles.muted}>每个Issue有明确负责人，子项分别推进。</p>
        {connection.status !== "online" && (
          <p role="status" className={styles.warning}>
            等待连接恢复 · 当前显示最后读取的Issue
          </p>
        )}
        {state.error && (
          <p role="status" className={styles.warning}>
            暂时无法读取，已有内容仍保留。
            <button onClick={() => void live.issues.list.refresh()}>重新读取</button>
          </p>
        )}
        {state.value ? (
          <>
            <IssueHierarchy
              items={state.value.items ?? []}
              onSelect={onSelect}
              includeClosed={includeClosed}
            />
            {(state.value.items ?? []).length === 0 && <p>暂无Issue。</p>}
            {state.value.more && (
              <button
                className={styles.button}
                onClick={() =>
                  void live.issues.openList({
                    includeClosed,
                    ...(project ? { project } : { parentID: "" }),
                    ...(state.value?.next ? { after: state.value.next } : {}),
                  })
                }
              >
                下一页
              </button>
            )}
          </>
        ) : (
          !state.error && <p>正在读取…</p>
        )}
      </div>
    </section>
  );
}
