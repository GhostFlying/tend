import { useState } from "react";
import type {
  IssueView,
  IssueDeliveryProgress,
  IssueDeliveryView,
  UpdateIssueProgressPayload,
  IssueControlPayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import { candidateLabel } from "./DeliveryProgress.tsx";
import { issueCommand } from "./actions.ts";
import styles from "./DetailPage.module.css";

function retainCandidate(delivery: IssueDeliveryProgress): IssueDeliveryProgress {
  const candidate = { ...delivery };
  delete candidate.conflict_action;
  delete candidate.conflict_revision;
  return candidate;
}

function candidateKey({ delivery, observation }: IssueDeliveryView) {
  return JSON.stringify([
    delivery.delivery_id,
    delivery.provider,
    delivery.repository,
    delivery.number,
    observation.head_sha,
    observation.base_sha,
    observation.target_sha,
    observation.mergeability === "conflicting" ? observation.last_conflict?.revision : undefined,
  ]);
}

export function ProgressEditor({
  issue,
  deliveries,
  onSaved,
}: {
  issue: IssueView;
  deliveries: IssueDeliveryView[] | null | undefined;
  onSaved(): void;
}) {
  const { client } = useServices();
  const operation = useDurableOp(client, `issue-progress:${issue.issue_id}`);
  const [baseline, setBaseline] = useState(issue);
  const candidates = (deliveries ?? []).filter(
    ({ observation: o }) =>
      o.state === "open" && o.head_sha && o.checked_at && (o.base_sha || o.target_sha),
  );
  const retained = baseline.progress?.delivery;
  const [selection, setSelection] = useState<IssueDeliveryView | null>();
  const [action, setAction] = useState("");
  const candidateChoice = selection
    ? candidateKey(selection)
    : selection === null || !retained
      ? ""
      : "original";
  const options =
    selection && !candidates.some((item) => candidateKey(item) === candidateChoice)
      ? [...candidates, selection]
      : candidates;
  const conflict =
    selection?.observation.mergeability === "conflicting"
      ? selection.observation.last_conflict
      : undefined;
  const canRecordAction = issue.assignee.kind === "owner" && conflict;
  const candidate: IssueDeliveryProgress | undefined = selection
    ? {
        delivery_id: selection.delivery.delivery_id,
        provider: selection.delivery.provider,
        repository: selection.delivery.repository,
        number: selection.delivery.number,
        head_sha: selection.observation.head_sha!,
        ...(selection.observation.base_sha ? { base_sha: selection.observation.base_sha } : {}),
        ...(selection.observation.target_sha
          ? { target_sha: selection.observation.target_sha }
          : {}),
        ...(canRecordAction && action
          ? { conflict_revision: conflict.revision, conflict_action: action }
          : {}),
      }
    : candidateChoice === "original" && retained
      ? retainCandidate(retained)
      : undefined;
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
          : issueCommand(baseline, "update_progress", {
              summary,
              next_step: next,
              ...(candidate ? { delivery: candidate } : {}),
            }),
      );
      if (result.kind === "done") onSaved();
      else
        setError(
          result.kind === "stale"
            ? "Issue 已变化，请关闭后重新打开进展编辑；当前输入仍保留。"
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
      {(retained || options.length > 0) && !original ? (
        <label>
          关联候选
          <select
            value={candidateChoice}
            disabled={operation.busy}
            onChange={(e) => {
              const value = e.target.value;
              setSelection(
                value === "original"
                  ? undefined
                  : (options.find((item) => candidateKey(item) === value) ?? null),
              );
              setAction("");
            }}
          >
            <option value="">Issue 整体进展</option>
            {retained ? (
              <option value="original">
                保留原候选 · {candidateLabel(retained)} · {retained.head_sha.slice(0, 12)}
              </option>
            ) : null}
            {options.map((item) => (
              <option value={candidateKey(item)} key={candidateKey(item)}>
                {candidateLabel(item.delivery)} · {item.observation.head_sha?.slice(0, 12)}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      {canRecordAction && !original ? (
        <label>
          冲突处理
          <select
            value={action}
            disabled={operation.busy}
            onChange={(e) => setAction(e.target.value)}
          >
            <option value="">仅更新进展</option>
            <option value="seen">已看到冲突</option>
            <option value="working">已开始处理</option>
          </select>
        </label>
      ) : null}
      {original?.delivery ? (
        <p>
          原请求候选 {candidateLabel(original.delivery)} · {original.delivery.head_sha.slice(0, 12)}
        </p>
      ) : null}
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
  const labels = { pause: "暂停 Issue", resume: "恢复 Issue", reopen: "重新打开 Issue" };
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
