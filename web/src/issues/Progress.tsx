import { useState } from "react";
import type {
  IssueView,
  UpdateIssueProgressPayload,
  IssueControlPayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import { issueCommand } from "./actions.ts";
import styles from "./DetailPage.module.css";

export function ProgressEditor({ issue, onSaved }: { issue: IssueView; onSaved(): void }) {
  const { client } = useServices();
  const operation = useDurableOp(client, `issue-progress:${issue.issue_id}`);
  const [baseline, setBaseline] = useState(issue);
  const original = operation.original?.payload as UpdateIssueProgressPayload | undefined;
  const [summary, setSummary] = useState(issue.progress?.summary ?? "");
  const [next, setNext] = useState(issue.progress?.next_step ?? "");
  const [error, setError] = useState("");
  const submit = async () => {
    if (operation.original) {
      const envelope = operation.original;
      const payload = envelope.payload as UpdateIssueProgressPayload;
      setSummary(payload.summary);
      setNext(payload.next_step);
      setBaseline((previous) => ({
        ...previous,
        revision: envelope.expected_control_revision ?? previous.revision,
        assignment_epoch: payload.expected_assignment_epoch,
      }));
    }
    try {
      const result = await operation.execute(
        operation.pending
          ? undefined
          : issueCommand(baseline, "update_progress", { summary, next_step: next }),
      );
      if (result.kind === "done") onSaved();
      else
        setError(
          result.kind === "stale"
            ? "Issue已变化，请关闭后重新打开进展编辑；当前输入仍保留。"
            : result.message,
        );
    } catch {
      setError("更新结果待核实，原请求已保留。");
    }
  };
  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <h2>编辑当前进展</h2>
      <label>
        当前进展
        <textarea
          aria-label="当前进展"
          value={original?.summary ?? summary}
          onChange={(e) => setSummary(e.target.value)}
          maxLength={500}
          rows={5}
          readOnly={operation.pending || operation.busy}
        />
      </label>
      <label>
        下一步
        <textarea
          aria-label="下一步"
          value={original?.next_step ?? next}
          onChange={(e) => setNext(e.target.value)}
          maxLength={240}
          rows={3}
          readOnly={operation.pending || operation.busy}
        />
      </label>
      <p className={styles.muted}>负责人总结与系统执行、验收事实分别保留。两项留空可清除总结。</p>
      {(error || operation.storageError) && <p role="status">{error || operation.storageError}</p>}
      <button className={styles.primary} disabled={operation.busy || !!operation.storageError}>
        {operation.pending ? "核对原更新请求" : "保存进展"}
      </button>
    </form>
  );
}
export function IssueControlEditor({
  issue,
  action,
  onSaved,
}: {
  issue: IssueView;
  action: "pause" | "resume" | "reopen";
  onSaved(): void;
}) {
  const { client } = useServices();
  const operation = useDurableOp(client, `issue-control:${issue.issue_id}:${action}`);
  const [baseline, setBaseline] = useState(issue);
  const original = operation.original?.payload as IssueControlPayload | undefined;
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const labels = { pause: "暂停Issue", resume: "恢复Issue", reopen: "重新打开Issue" };
  const submit = async () => {
    if (operation.original) {
      const envelope = operation.original;
      const payload = envelope.payload as IssueControlPayload;
      setReason(payload.reason ?? "");
      setBaseline((previous) => ({
        ...previous,
        revision: envelope.expected_control_revision ?? previous.revision,
        assignment_epoch: payload.expected_assignment_epoch,
      }));
    }
    try {
      const result = await operation.execute(
        operation.pending ? undefined : issueCommand(baseline, action, { reason }),
      );
      if (result.kind === "done") onSaved();
      else setError(result.message);
    } catch {
      setError("操作结果待核实，原请求已保留。");
    }
  };
  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <h2>{labels[action]}</h2>
      <label>
        原因
        <textarea
          aria-label="操作原因"
          value={original?.reason ?? reason}
          onChange={(e) => setReason(e.target.value)}
          rows={3}
          readOnly={operation.pending || operation.busy}
          required
        />
      </label>
      {error && <p role="status">{error}</p>}
      <button
        className={styles.primary}
        disabled={
          operation.busy || !!operation.storageError || (!operation.pending && !reason.trim())
        }
      >
        {operation.pending ? "核对原请求" : labels[action]}
      </button>
    </form>
  );
}
