import type {
  DeliveryCheck,
  IssueDeliveryProgress,
  IssueDeliveryReviewProgress,
  IssueDeliveryView,
} from "../api/generated/api.ts";
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
const checkLabels: Record<string, string> = {
  ...ciLabels,
  action_required: "需要处理",
  cancelled: "已取消",
  neutral: "中性",
  skipped: "按条件跳过",
  timed_out: "超时",
};
const failedCheckStates = new Set([
  "failure",
  "error",
  "action_required",
  "cancelled",
  "stale",
  "startup_failure",
  "timed_out",
]);

const mergeabilityLabels: Record<string, string> = {
  mergeable: "无合并冲突",
  unknown: "合并状态待确认",
};

function DeliveryChecks({ checks }: { checks: DeliveryCheck[] }) {
  return (
    <ul className={styles.deliveryChecks}>
      {checks.map((check, index) => {
        const status = check.conclusion || check.state;
        const label = checkLabels[status] ?? status;
        return (
          <li
            key={`${check.name}:${index}`}
            data-tone={failedCheckStates.has(status) ? "error" : undefined}
          >
            {check.url ? (
              <a href={check.url} target="_blank" rel="noreferrer">
                {check.name}
              </a>
            ) : (
              check.name
            )}
            {" · "}
            {label}
          </li>
        );
      })}
    </ul>
  );
}

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

const verdictLabels: Record<string, string> = {
  passed: "通过",
  changes_requested: "需要修改",
  blocked: "审阅受阻",
  inconclusive: "结论不明确",
};

function ReviewProgress({ review }: { review: IssueDeliveryReviewProgress }) {
  return (
    <p>
      <strong>独立审阅已返回 · {verdictLabels[review.verdict] ?? review.verdict}</strong>
      {" · "}第 {review.round} 轮 · <CandidateIdentity candidate={review} />
      {!review.recorded
        ? " · 待记录到共享审阅"
        : review.verdict === "passed" && !review.valid
          ? " · 当前适用性待核对"
          : ""}
    </p>
  );
}

export function DeliveryProgress({
  item,
  presentation = "current",
  newCandidate = false,
}: {
  item: IssueDeliveryView;
  presentation?: "current" | "candidate" | "observation";
  newCandidate?: boolean;
}) {
  const { delivery, observation: o, conflict_progress: response, review } = item;
  const c = o.last_conflict;
  const head = o.head_sha;
  const reviewProgress = review ? (
    <ReviewProgress review={review} />
  ) : item.review_unavailable ? (
    <p className={styles.muted}>暂时无法读取审阅记录</p>
  ) : null;
  if (!head || !o.checked_at)
    return reviewProgress ? (
      <div className={styles.deliveryProgress} data-delivery-progress={delivery.delivery_id}>
        <p>{candidateLabel(delivery)}</p>
        {reviewProgress}
      </div>
    ) : null;
  const conflict = o.state === "open" && o.mergeability === "conflicting";
  const sameConflict =
    c && c.head_sha === head && c.base_sha === o.base_sha && c.target_sha === o.target_sha;
  const currentConflict = conflict && sameConflict ? c : undefined;
  const conflictUnknown =
    o.state === "open" && sameConflict && (!o.mergeability || o.mergeability === "unknown");
  const earlierConflict = c && c.head_sha !== head ? c : undefined;
  const historical = presentation !== "current";
  const replaced =
    presentation === "current" ? o.previous_head_sha && o.previous_head_sha !== head : newCandidate;
  const deliveryState = o.state === "merged" ? "已合入" : o.state === "closed" ? "已关闭" : "";
  const mergeability =
    o.state === "open" && !conflict && !conflictUnknown
      ? mergeabilityLabels[o.mergeability || "unknown"]
      : "";
  const ci = o.ci?.head_sha === head ? o.ci : undefined;
  const checks = ci?.checks ?? [];
  const visibleChecks =
    presentation === "observation"
      ? checks
      : presentation === "candidate"
        ? checks.filter((check) => failedCheckStates.has(check.conclusion || check.state))
        : [];
  const time = historical ? o.checked_at : (currentConflict?.checked_at ?? o.checked_at);
  const observer = historical ? o.observer : (currentConflict?.observer ?? o.observer);
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
        {historical && (deliveryState || mergeability)
          ? ` · ${deliveryState || mergeability}`
          : null}
      </p>
      <p>
        {presentation === "candidate" ? "最新观察 · " : historical ? "当时观察 · " : "当前交付 · "}
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
      {earlierConflict ? (
        <p className={styles.muted}>
          此前冲突 · <CandidateIdentity candidate={earlierConflict} />
        </p>
      ) : null}
      {reviewProgress}
      {ci ? (
        <p>
          {historical ? "当时 CI" : "当前候选的提交 CI"} ·{" "}
          {ci.truncated ? "检查结果待确认" : (ciLabels[ci.state] ?? ci.state)}
          {historical ? ` · ${checks.length} 项检查` : null}
        </p>
      ) : null}
      {visibleChecks.length ? <DeliveryChecks checks={visibleChecks} /> : null}
    </div>
  );
}
