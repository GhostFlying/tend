import { AssigneeName } from "./AssigneeName.tsx";
import { useEffect, useState } from "react";
import type {
  IssueView,
  IssueAssignmentPayload,
  FinishIssueAssignmentPayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import type { OpOutcome } from "../app/ops.ts";
import { useStore } from "../state/store.ts";
import { issueCommand } from "./actions.ts";
import styles from "./Issues.module.css";
import form from "../attention/Inbox.module.css";
import layout from "./Handoff.module.css";

export function IssueHandoff({ issue, onBack }: { issue: IssueView; onBack(): void }) {
  const { live, client } = useServices();
  const connection = useStore(client.connection);
  const catalog = useStore(live.agents.hosted.state);
  const firstTarget = issue.pending_assignee ?? issue.assignee;
  const [target, setTarget] = useState(firstTarget.kind === "owner" ? "owner" : firstTarget.ref);
  const [reason, setReason] = useState("");
  const [outcome, setOutcome] = useState<OpOutcome | null>(null);
  const operation = useDurableOp(client, `issue-handoff:${issue.issue_id}`);
  const original = operation.original;
  const shownReason = original
    ? (original.payload as IssueAssignmentPayload | FinishIssueAssignmentPayload).reason
    : reason;
  const originalAssignee =
    original?.operation === "issues.assign"
      ? (original.payload as IssueAssignmentPayload).assignee
      : undefined;
  const shownTarget = originalAssignee
    ? originalAssignee.kind === "owner"
      ? "owner"
      : originalAssignee.ref
    : issue.pending_assignee
      ? issue.pending_assignee.kind === "owner"
        ? "owner"
        : issue.pending_assignee.ref
      : target;
  useEffect(() => {
    void live.agents.openHosted();
  }, [live]);
  const candidates =
    catalog.value?.projection.agents?.filter((agent) =>
      agent.purposes?.includes("issue_manager"),
    ) ?? [];
  const run = async (reconcile = false) => {
    if (reconcile) {
      setReason(shownReason ?? "");
      setTarget(shownTarget);
    }
    try {
      const result = await operation.execute(
        reconcile
          ? undefined
          : issue.pending_handoff_id
            ? issueCommand(issue, "finish_assignment", {
                handoff_id: issue.pending_handoff_id,
                reason,
              })
            : issueCommand(issue, "assign", {
                assignee:
                  target === "owner"
                    ? { kind: "owner", ref: connection.status === "online" ? connection.login : "" }
                    : { kind: "agent", ref: target },
                reason,
              }),
      );
      setOutcome(result);
      if (result.kind === "done" || result.kind === "stale") void live.issues.detail.refresh();
    } catch {
      setOutcome({ kind: "unknown", message: "交接结果尚未确认，请核实原请求。" });
    }
  };
  return (
    <section className={layout.root} aria-label="交接负责人">
      <h2>交接负责人</h2>
      <p>{issue.plan.title}</p>
      <p>
        上次确认负责人：
        <AssigneeName assignee={issue.assignee} />
      </p>
      {issue.pending_assignee && (
        <p className={styles.warning}>
          交接待核实，目标：
          {issue.pending_assignee.kind === "owner" ? "你（用户）" : issue.pending_assignee.ref}
        </p>
      )}
      <div className={layout.columns}>
        <div>
          <label>
            交接给
            <select
              value={shownTarget}
              disabled={operation.busy || operation.pending || !!issue.pending_handoff_id}
              onChange={(e) => setTarget(e.target.value)}
            >
              <option value="owner">你（用户）</option>
              {candidates.map((agent) => {
                const ref = `${agent.identity.execution_environment_id}/${agent.identity.local_id}`;
                return (
                  <option key={ref} value={ref} disabled={!agent.enabled}>
                    {agent.display_name}（Agent）{!agent.enabled ? " · 配置未启用，暂不可用" : ""}
                  </option>
                );
              })}
              {issue.assignee.kind === "agent" &&
                !candidates.some(
                  (agent) =>
                    `${agent.identity.execution_environment_id}/${agent.identity.local_id}` ===
                    issue.assignee.ref,
                ) && <option value={issue.assignee.ref}>{issue.assignee.ref}（当前负责人）</option>}
            </select>
          </label>
          {catalog.error && (
            <p className={styles.warning}>
              登记目录暂时无法读取。
              <button onClick={() => void live.agents.openHosted()}>重新读取</button>
            </p>
          )}
          <label>
            <h3>{issue.pending_handoff_id ? "继续交接说明" : "交接说明"}</h3>
            <textarea
              className={form.textarea}
              value={shownReason}
              onChange={(e) => setReason(e.target.value)}
              disabled={operation.busy || operation.pending}
            />
          </label>
        </div>
        <div className={styles.goal}>
          <strong>随交接保留</strong>
          <ul>
            <li>目标、范围、约束与完成条件</li>
            <li>子Issue及各自负责人</li>
            <li>来源、讨论与未解决事项</li>
            <li>已有任务、执行记录、工作区与交付</li>
          </ul>
        </div>
      </div>
      {issue.pending_handoff_id && !reason.trim() && (
        <p className={styles.muted}>待交接目标已保留。请填写本次继续交接说明，再核对旧操作。</p>
      )}
      <p className={styles.muted}>先核实旧操作，再启用新负责人的控制权。交接不会重跑已有任务。</p>
      {operation.storageError && <p role="alert">{operation.storageError}</p>}
      {outcome && (
        <p role="status" className={outcome.kind === "done" ? styles.muted : styles.warning}>
          {outcome.kind === "done" ? "交接请求已记录。" : outcome.message}
        </p>
      )}
      <div className={form.actions}>
        {operation.pending ? (
          <button
            className={styles.primary}
            disabled={operation.busy}
            onClick={() => void run(true)}
          >
            核实原交接请求
          </button>
        ) : (
          <button
            className={styles.primary}
            disabled={
              connection.status !== "online" ||
              operation.busy ||
              !reason.trim() ||
              (!issue.pending_handoff_id &&
                target === (issue.assignee.kind === "owner" ? "owner" : issue.assignee.ref))
            }
            onClick={() => void run()}
          >
            {issue.pending_handoff_id ? "继续交接" : "发起交接"}
          </button>
        )}
        <button className={styles.button} onClick={onBack}>
          返回Issue
        </button>
      </div>
    </section>
  );
}
