import { useState } from "react";
import type { IssueView, SetIssueAcceptancePolicyPayload } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import { issueCommand } from "./actions.ts";
import styles from "./DetailPage.module.css";
import layout from "./Definition.module.css";

export function IssueAcceptancePolicyEditor({
  issue,
  onSaved,
}: {
  issue: IssueView;
  onSaved(): void;
}) {
  const { client } = useServices();
  const [baseline] = useState(issue);
  const [required, setRequired] = useState(issue.acceptance_policy?.required ?? true);
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [restored, setRestored] = useState("");
  const op = useDurableOp(client, `issue-acceptance-policy:${issue.issue_id}`);
  const original = op.original?.payload as SetIssueAcceptancePolicyPayload | undefined;
  if (op.original && op.original.request_id !== restored) {
    // Preserve recovered fields before a definitive receipt clears the journal.
    setRestored(op.original.request_id);
    setRequired(original!.required);
    setReason(original!.reason);
  }
  const submit = async () => {
    try {
      const result = await op.execute(
        op.pending
          ? undefined
          : issueCommand(baseline, "set_acceptance_policy", { required, reason }),
      );
      if (result.kind === "done") onSaved();
      else
        setError(
          result.kind === "stale"
            ? "Issue 已变化，请重新打开策略编辑；原输入仍保留。"
            : result.message,
        );
    } catch {
      setError("策略结果待核实，请核对保留的原请求。");
    }
  };
  if (!issue.acceptance_policy)
    return <p className={styles.muted}>历史事项沿用独立验收要求，未推断其创建者或更改已有任务。</p>;
  return (
    <form
      className={`${styles.form} ${layout.policyEditor}`}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <h2>后续任务的独立验收</h2>
      <p>
        当前策略：{issue.acceptance_policy.required ? "要求独立验收" : "不强制独立验收"}。
        {issue.acceptance_policy.reason}
      </p>
      <label className={layout.policyChoice}>
        <input
          type="checkbox"
          checked={original?.required ?? required}
          disabled={op.busy || op.pending}
          onChange={(e) => setRequired(e.target.checked)}
        />
        要求另一个 Agent 独立验收后续任务
      </label>
      <label>
        调整理由
        <textarea
          required
          value={original?.reason ?? reason}
          readOnly={op.busy || op.pending}
          onChange={(e) => setReason(e.target.value)}
        />
      </label>
      <p className={styles.muted}>
        调查、只读报告可由创建者决定不另派验收。此处是用户的显式策略；Agent
        只能以原创建者身份调整。已有任务、验收和待处理决策保持原约定；合法结果及合入、部署证明仍须完成。
      </p>
      {(error || op.storageError) && <p role="status">{error || op.storageError}</p>}
      <button className={styles.primary} disabled={op.busy || !!op.storageError}>
        {op.pending ? "核对原策略请求" : "保存后续任务策略"}
      </button>
    </form>
  );
}
