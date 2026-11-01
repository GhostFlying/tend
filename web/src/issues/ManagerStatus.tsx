import type { IssueManagerView } from "../api/generated/api.ts";
import { queueReasonText } from "../tasks/execution.ts";
import { controlLabels, lifecycleLabels } from "../tasks/status.ts";
import styles from "./DetailPage.module.css";

export function ManagerStatus({
  manager,
  onTask,
}: {
  manager: IssueManagerView;
  onTask(id: string): void;
}) {
  const phase =
    !manager.host_id && !["completed", "cancelled"].includes(manager.state)
      ? "等待执行主机领取"
      : manager.state === "waiting"
        ? "等待新进展"
        : manager.state === "running"
          ? "正在处理本 Issue"
          : manager.state === "queued"
            ? "等待执行资源"
            : lifecycleLabels[manager.state];
  return (
    <section aria-label="负责人运行状态">
      <h2>负责人运行</h2>
      <button className={styles.textLink} onClick={() => onTask(manager.task_id)}>
        负责人 Agent
      </button>
      <p>{phase}</p>
      {manager.control_mode && manager.control_mode !== "managed" && (
        <p>{controlLabels[manager.control_mode]}</p>
      )}
      {manager.reconciliation_pending && <p role="status">负责人执行待对账</p>}
      {manager.recovery && (
        <p role="status">
          {manager.recovery.status === "waiting"
            ? "负责人恢复退避中"
            : ["starting", "running"].includes(manager.recovery.status)
              ? "负责人正在续接"
              : "负责人恢复需要处理"}
        </p>
      )}
      {manager.queue_reason && (
        <p>
          {queueReasonText({
            block_reason: "none",
            execution: { state: "queued", queue_reason: manager.queue_reason },
          })}
        </p>
      )}
    </section>
  );
}
