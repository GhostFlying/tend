// Display actual resource identity separately from the requested workspace spec.
import type { TaskDetail } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Button } from "../ui/Button.tsx";
import styles from "./Deliverables.module.css";

const modeLabels: Record<string, string> = {
  new: "新建工作区",
  reuse: "复用工作区",
  temporary: "独立临时目录",
};
const statusLabels: Record<string, string> = {
  assigned: "已分配",
  ready: "就绪",
  creating: "创建中",
  unavailable: "不可用",
  unknown: "未知",
  retained: "保留中",
  removing: "清理中",
  removed: "已清理",
  cleaning: "清理中",
  deleted: "已清理",
};
export function WorkspaceFacts({ task }: { task: TaskDetail }) {
  const { openTask } = useServices();
  const resource = task.workspace_state;
  const spec =
    task.spec && typeof task.spec === "object" ? (task.spec as Record<string, unknown>) : {};
  const desired =
    spec.workspace && typeof spec.workspace === "object"
      ? (spec.workspace as Record<string, unknown>)
      : {};
  const mode = resource?.mode || (typeof desired.mode === "string" ? desired.mode : "new");
  const source =
    resource?.source_task_id ||
    (typeof desired.source_task_id === "string" ? desired.source_task_id : "");
  return (
    <section className={styles.section} aria-label="工作区资源">
      <h2>工作区资源</h2>
      <p>
        {modeLabels[mode] ?? mode}
        {resource?.kind === "directory" && mode !== "temporary" ? " · 普通目录" : ""}
        {resource?.status && ` · ${statusLabels[resource.status] ?? resource.status}`}
      </p>
      {source && (
        <p>
          复用来源{" "}
          <Button type="button" variant="link" onClick={() => openTask(source)}>
            {source}
          </Button>
        </p>
      )}
      {resource ? (
        <dl className={styles.facts}>
          {resource.host && (
            <>
              <dt>主机</dt>
              <dd>{resource.host}</dd>
            </>
          )}
          {resource.name && (
            <>
              <dt>资源名</dt>
              <dd>{resource.name}</dd>
            </>
          )}
          {resource.resource_id && (
            <>
              <dt>资源 ID</dt>
              <dd>
                <code>{resource.resource_id}</code>
              </dd>
            </>
          )}
          {resource.path && (
            <>
              <dt>目录</dt>
              <dd>
                <code>{resource.path}</code>
              </dd>
            </>
          )}
          {resource.occupied_by && (
            <>
              <dt>占用任务</dt>
              <dd>
                {resource.occupied_by === task.task_id ? (
                  "当前任务"
                ) : (
                  <Button
                    type="button"
                    variant="link"
                    onClick={() => openTask(resource.occupied_by!)}
                  >
                    {resource.occupied_by}
                  </Button>
                )}
              </dd>
            </>
          )}
        </dl>
      ) : (
        <p className={styles.caption}>尚未分配资源</p>
      )}
      {desired.include_existing_changes === true && (
        <p className={styles.caption}>任务范围明确承接复用前已有的修改</p>
      )}
    </section>
  );
}
