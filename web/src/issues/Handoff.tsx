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
import { issueHandoffCategoryTarget, issueHandoffCommand } from "./actions.ts";
import styles from "./Issues.module.css";
import form from "../attention/Inbox.module.css";
import layout from "./Handoff.module.css";

export function IssueHandoff({
  issue,
  onBack,
  onChanged,
}: {
  issue: IssueView;
  onBack(): void;
  onChanged?(): void;
}) {
  const { live, client } = useServices();
  const connection = useStore(client.connection);
  const categories = useStore(live.agents.categories.state);
  const activeCategories = (categories.value?.items ?? []).filter(
    (category) => category.state === "active",
  );
  const [selected, setTarget] = useState<string>();
  const preferred = issue.manager?.category_id ?? "issue-manager";
  const initial =
    activeCategories.find((category) => category.category_id === preferred) ?? activeCategories[0];
  const target = selected ?? (initial ? `category:${initial.category_id}` : "owner");
  useEffect(() => {
    void live.agents.openCategories();
  }, [live]);
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
  const categoryTarget = issueHandoffCategoryTarget(original);
  const shownTarget =
    categoryTarget ??
    (originalAssignee
      ? originalAssignee.kind === "owner"
        ? "owner"
        : originalAssignee.ref
      : issue.pending_assignee
        ? issue.pending_assignee.kind === "owner"
          ? "owner"
          : issue.pending_assignee.ref
        : target);
  const run = async (reconcile = false) => {
    if (reconcile) {
      setReason(shownReason ?? "");
      setTarget(shownTarget);
    }
    try {
      const result = await operation.execute(
        reconcile
          ? undefined
          : issueHandoffCommand(
              issue,
              target,
              reason,
              connection.status === "online" ? connection.login : "",
            ),
      );
      setOutcome(result);
      if (result.kind === "done" || result.kind === "stale") {
        void live.issues.detail.refresh();
        onChanged?.();
      }
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
        <AssigneeName assignee={issue.assignee} manager={issue.manager} />
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
              {shownTarget !== "owner" &&
                !activeCategories.some(
                  (category) => `category:${category.category_id}` === shownTarget,
                ) && (
                  <option value={shownTarget}>
                    {shownTarget.startsWith("category:")
                      ? `已选类别：${shownTarget.slice("category:".length)}`
                      : `待核实负责人：${shownTarget}`}
                  </option>
                )}
              {activeCategories.map((category) => (
                <option key={category.category_id} value={`category:${category.category_id}`}>
                  {category.title} · {category.category_id}（执行主机领取）
                </option>
              ))}
            </select>
          </label>
          {categories.value?.more && (
            <button
              type="button"
              onClick={() => void live.agents.openCategories("active", categories.value?.next)}
            >
              下一页类别
            </button>
          )}
          {categories.error && (
            <p role="status">
              类别读取失败，保留已读取结果。
              <button type="button" onClick={() => void live.agents.categories.refresh()}>
                重新读取
              </button>
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
            <li>子 Issue 及各自负责人</li>
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
                !(issue.manager?.state === "queued" && !issue.manager.host_id) &&
                target === (issue.assignee.kind === "owner" ? "owner" : issue.assignee.ref))
            }
            onClick={() => void run()}
          >
            {issue.pending_handoff_id ? "继续交接" : "发起交接"}
          </button>
        )}
        <button className={styles.button} onClick={onBack}>
          返回 Issue
        </button>
      </div>
    </section>
  );
}
