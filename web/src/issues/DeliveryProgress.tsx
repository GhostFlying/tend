import type { IssueDeliveryProgress, IssueDeliveryView } from "../api/generated/api.ts";
import { AssigneeName } from "./AssigneeName.tsx";
import styles from "./DetailPage.module.css";

const providers = { github: "GitHub", codebase: "Codebase" };
const ciLabels: Record<string, string> = {
  pending: "等待检查",
  running: "检查进行中",
  expected: "等待检查",
  in_progress: "检查进行中",
  success: "检查通过",
  failure: "检查失败",
  error: "检查出错",
  unknown: "检查结果待确认",
};

export function conflictActionLabel(action: string) {
  return action === "working" ? "已开始处理" : "已看到冲突";
}

export function candidateLabel(c: Pick<IssueDeliveryProgress, "repository" | "number">) {
  return `${c.repository} #${c.number}`;
}

function SHA({ value }: { value: string }) {
  return <code title={value}>{value.slice(0, 12)}</code>;
}

export function CandidateIdentity({
  candidate,
}: {
  candidate: Pick<IssueDeliveryProgress, "head_sha" | "base_sha" | "target_sha">;
}) {
  return (
    <span>
      候选 <SHA value={candidate.head_sha} />
      {candidate.base_sha ? (
        <>
          {" "}
          · 基线 <SHA value={candidate.base_sha} />
        </>
      ) : null}
      {candidate.target_sha ? (
        <>
          {" "}
          · 目标分支 <SHA value={candidate.target_sha} />
        </>
      ) : null}
    </span>
  );
}

export function DeliveryProgress({
  item,
  historical = false,
}: {
  item: IssueDeliveryView;
  historical?: boolean;
}) {
  const { delivery, observation: o, conflict_progress: response } = item;
  const c = o.last_conflict;
  const head = o.head_sha;
  if (!head || !o.checked_at) return null;
  const conflict = o.state === "open" && o.mergeability === "conflicting";
  const sameConflict =
    c && c.head_sha === head && c.base_sha === o.base_sha && c.target_sha === o.target_sha;
  const currentConflict = conflict && sameConflict ? c : undefined;
  const conflictUnknown =
    o.state === "open" && sameConflict && (!o.mergeability || o.mergeability === "unknown");
  const replaced = c && c.head_sha !== head;
  const ci = o.ci?.head_sha === head ? o.ci : undefined;
  const time = currentConflict?.checked_at ?? o.checked_at;
  const observer = currentConflict?.observer ?? o.observer;
  const url = o.url ?? delivery.url;
  const heading = candidateLabel(delivery);
  const candidate = { ...o, head_sha: head };
  return (
    <div className={styles.deliveryProgress} data-delivery-progress={delivery.delivery_id}>
      <p>
        {url ? (
          <a href={url} target="_blank" rel="noreferrer">
            {heading}
          </a>
        ) : (
          heading
        )}
        {conflict ? <strong> · 检测到合并冲突</strong> : null}
        {conflictUnknown ? <strong> · 曾检测到冲突，当前状态待确认</strong> : null}
        {replaced ? <strong> · 已推送新候选</strong> : null}
      </p>
      <p>
        {historical ? "当时观察 · " : "当前交付 · "}
        <CandidateIdentity candidate={candidate} />
      </p>
      <p className={styles.muted}>
        {providers[delivery.provider]}
        {observer === "server" ? " · 服务端观察" : observer === "runner" ? " · Runner 观察" : ""}
        {" · "}
        <time dateTime={new Date(time).toISOString()}>
          {new Date(time).toLocaleString("zh-CN")}
        </time>
        {o.error_kind || o.refresh_pending ? " · 等待最新观察" : ""}
      </p>
      {response?.delivery?.conflict_action ? (
        <p>
          <AssigneeName assignee={response.updated_by} />{" "}
          {conflictActionLabel(response.delivery.conflict_action)}
          {" · "}
          <time dateTime={new Date(response.updated_at).toISOString()}>
            {new Date(response.updated_at).toLocaleString("zh-CN")}
          </time>
        </p>
      ) : null}
      {replaced ? (
        <p className={styles.muted}>
          此前冲突 · <CandidateIdentity candidate={c} />
        </p>
      ) : null}
      {ci ? (
        <p>
          {historical ? "当时 CI" : "当前候选的提交 CI"} ·{" "}
          {ci.truncated ? "检查结果待确认" : (ciLabels[ci.state] ?? ci.state)}
        </p>
      ) : null}
    </div>
  );
}
