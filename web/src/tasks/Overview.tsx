import { useServices } from "../app/services.tsx";
import controls from "../shell/controls.module.css";
import list from "../shell/list.module.css";
import { StatusDot } from "../shell/StatusDot.tsx";
import { useStore } from "../state/store.ts";
import { taskLook } from "./status.ts";

export function TaskOverview() {
  const { live, openTask } = useServices();
  const snapshot = useStore(live.snapshot);
  const detail = useStore(live.detail);
  const tasks = snapshot?.tasks ?? [];
  const projects = new Map((snapshot?.projects ?? []).map((p) => [p.project, p.title]));
  for (const t of tasks) {
    if (!projects.has(t.project)) projects.set(t.project, t.project || "未分组");
  }
  return (
    <>
      {!snapshot && <p className={controls.empty}>正在读取任务…</p>}
      {snapshot && tasks.length === 0 && (
        <p className={controls.empty}>还没有任务。在对话里告诉主 Agent 要做什么。</p>
      )}
      {[...projects].map(([id, title]) => {
        const group = tasks.filter((t) => t.project === id);
        if (group.length === 0) return null;
        return (
          <section key={id} aria-label={title}>
            <div className={list.sectionTitle}>
              <span>{title}</span>
              <span>{group.length}</span>
            </div>
            {group.map((t) => {
              const look = taskLook(t);
              return (
                <button
                  key={t.task_id}
                  type="button"
                  className={list.row}
                  aria-current={detail?.taskId === t.task_id ? "true" : undefined}
                  title={`${t.title} · ${look.label}`}
                  onClick={() => openTask(t.task_id)}
                >
                  <StatusDot tone={look.tone} />
                  <span className={list.label}>{t.title}</span>
                  <span className={list.meta}>{look.label}</span>
                </button>
              );
            })}
          </section>
        );
      })}
    </>
  );
}

export function Hosts() {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  return (
    <section aria-label="执行主机占用">
      <div className={list.sectionTitle}>执行主机</div>
      {(snapshot?.hosts ?? []).map((h) => (
        <div
          className={list.row}
          key={h.host_id}
          title={`槽位占用：${h.open_runs} / ${h.slots} · ${h.os} / ${h.arch} · ${h.worker}`}
        >
          <span className={list.label}>{h.host_id}</span>
          <span className={list.meta}>
            {h.open_runs > 0 ? "执行中" : "空闲"} · {h.open_runs} / {h.slots} 槽位
          </span>
        </div>
      ))}
      {snapshot && !snapshot.hosts?.length && (
        <p className={controls.empty}>暂无可用的执行主机。</p>
      )}
    </section>
  );
}
