import { useEffect, useMemo } from "react";
import type { Envelope, IssueView, IssueDeliveryView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { DurableOp } from "../app/durableOp.ts";
import { newRequestId } from "../api/ids.ts";
import { refreshBaseline, refreshReconciled, type RefreshBaseline } from "../workitems/model.ts";
import type { OpOutcome } from "../app/ops.ts";
import { useState } from "react";
import { issueCommand } from "./actions.ts";
import { IssueFollowups } from "./Followups.tsx";
import styles from "./Issues.module.css";

const labels: Record<string, string> = {
  active: "等待触发",
  paused: "已暂停",
  finished: "调度已结束",
  cancelled: "已取消",
  queued: "待负责人处理",
  starting: "正在启动",
  processing: "正在核对",
  completed: "本轮结束",
  interrupted: "已中断",
  failed: "失败",
  unknown: "结果待核实",
  delivered: "已投递",
  expired: "等待期限已到",
  not_attempted: "尚未观察",
  open: "打开",
  merged: "已合并",
  closed: "已关闭",
};
export function IssueFollowupsDelivery({ issue }: { issue: IssueView }) {
  const { live } = useServices();
  const deliveries = useStore(live.issues.deliveries.state);
  useEffect(() => {
    void live.issues.openDeliveries(issue.issue_id);
    return () => live.issues.deliveries.close();
  }, [live, issue.issue_id]);
  return (
    <>
      <IssueFollowups issue={issue} deliveries={deliveries.value?.items ?? []} />
      <h3>交付状态</h3>
      <p className={styles.muted}>显示最后观察事实，重新观察不等于重新执行任务。</p>
      {deliveries.error && (
        <p className={styles.warning}>
          交付状态读取失败。
          <button onClick={() => void live.issues.deliveries.refresh()}>重新读取</button>
        </p>
      )}
      {(deliveries.value?.items ?? []).map((item) => (
        <DeliveryRow key={item.delivery.delivery_id} issue={issue} item={item} />
      ))}
      {deliveries.value && !deliveries.value.items?.length && (
        <p className={styles.muted}>暂无已登记交付。</p>
      )}
    </>
  );
}
function DeliveryRow({ issue, item }: { issue: IssueView; item: IssueDeliveryView }) {
  const { client, live } = useServices();
  const prefix = `issue-delivery-refresh:${issue.issue_id}:${item.delivery.delivery_id}`;
  const [pending, setPending] = useState<{ scope: string; envelope: Envelope }[]>([]);
  const [busy, setBusy] = useState(true);
  const [missing, setMissing] = useState<ReadonlySet<string>>(new Set());
  const [result, setResult] = useState<OpOutcome | null>(null);
  const [error, setError] = useState("");
  const loader = useMemo(
    () => async () =>
      (await DurableOp.pendingScopes(prefix)).filter(
        (record) => record.scope === prefix || record.scope.startsWith(prefix + ":facts:"),
      ),
    [prefix],
  );
  useEffect(() => {
    let active = true;
    const refresh = () => {
      void loader().then(
        (value) => {
          if (active) {
            setPending(value);
            setBusy(false);
          }
        },
        () => {
          if (active) {
            setError("无法读取原刷新请求，尚未发送操作。");
            setBusy(false);
          }
        },
      );
    };
    refresh();
    const stop = live.snapshot.subscribe(refresh);
    return () => {
      active = false;
      stop();
    };
  }, [live, loader]);
  const records = pending.map((record) => {
    let baseline: RefreshBaseline | undefined;
    const encoded = record.scope.slice((prefix + ":facts:").length);
    if (record.scope.startsWith(prefix + ":facts:"))
      baseline = JSON.parse(decodeURIComponent(encoded)) as RefreshBaseline;
    return {
      ...record,
      baseline,
      reconciled:
        baseline !== undefined && refreshReconciled(baseline, item.delivery, item.observation),
    };
  });
  const unresolved = records.some((record) => !record.reconciled);
  const refresh = async () => {
    setBusy(true);
    setError("");
    try {
      const scope = `${prefix}:facts:${encodeURIComponent(JSON.stringify(refreshBaseline(item.delivery, item.observation)))}`;
      const command = issueCommand(issue, "refresh_delivery", {
        delivery_id: item.delivery.delivery_id,
      });
      const out = await new DurableOp(scope).submit(client, {
        ...command,
        request_id: newRequestId(Date.now()),
      });
      setResult(out);
      setPending(await loader());
      if (out.kind === "done" || out.kind === "stale") live.issues.refresh();
    } catch {
      setError("刷新请求结果待核实，请保留原请求。");
      try {
        setPending(await loader());
      } catch {
        /* Keep the original read error visible. */
      }
    } finally {
      setBusy(false);
    }
  };
  const reconcile = async (scope: string, envelope: Envelope, retry: boolean) => {
    setBusy(true);
    setError("");
    try {
      const receipt = await client.lookup(envelope.request_id);
      if (!receipt && !retry) {
        setMissing(new Set([...missing, scope]));
        setResult({ kind: "unknown", message: "未查到原刷新请求，可显式重试同一请求。" });
        return;
      }
      const out = await new DurableOp(scope).reconcile(client);
      setResult(out);
      setPending(await loader());
      if (out.kind === "done" || out.kind === "stale") live.issues.refresh();
    } catch {
      setError("仍无法核对原刷新请求，原内容与身份保留。");
    } finally {
      setBusy(false);
    }
  };
  const observation = item.observation;
  return (
    <section className={styles.taskPeek}>
      <h4>
        {item.delivery.repository} · #{item.delivery.number}
      </h4>
      <p>
        {labels[observation.state] || observation.state || "未记录观察状态"}
        {observation.refresh_pending ? " · 观察请求待回报" : ""}
      </p>
      {item.delivery.url && (
        <p>
          <a href={item.delivery.url} target="_blank" rel="noreferrer">
            查看原交付
          </a>
        </p>
      )}
      {observation.checked_at && (
        <p className={styles.muted}>
          观察于{new Date(observation.checked_at).toLocaleString("zh-CN")}
        </p>
      )}
      {observation.head_sha && <p className={styles.muted}>Head：{observation.head_sha}</p>}
      {observation.merge_sha && <p className={styles.muted}>Merge：{observation.merge_sha}</p>}
      {observation.merged_at && (
        <p>
          合入于{observation.merged_at} · {observation.merged_by?.login || "未记录操作者"}
        </p>
      )}
      {observation.closed_at && (
        <p>
          关闭于{observation.closed_at} · {observation.closed_by?.login || "未记录操作者"}
        </p>
      )}
      {observation.error_kind && (
        <p className={styles.warning}>最近观察未完成：{observation.error_kind}，保留上次事实。</p>
      )}
      <button
        className={styles.button}
        disabled={busy || !!error || !!observation.refresh_pending || unresolved}
        onClick={() => void refresh()}
      >
        重新观察交付
      </button>
      {records.map((record) => (
        <div
          className={styles.warning}
          key={record.scope}
          data-request-id={record.envelope.request_id}
        >
          <p>
            {record.reconciled
              ? "交付事实已更新，可查看最新观察；原刷新请求的结果仍未确认。"
              : "刷新请求尚未确认，等待账本事件或连接恢复；不会自动重发。"}
          </p>
          <small>原请求：{record.envelope.request_id}</small>
          <button
            disabled={busy}
            onClick={() => void reconcile(record.scope, record.envelope, false)}
          >
            核对刷新请求
          </button>
          {missing.has(record.scope) && (
            <button
              disabled={busy}
              onClick={() => void reconcile(record.scope, record.envelope, true)}
            >
              重试原刷新请求
            </button>
          )}
        </div>
      ))}
      {error && <p role="status">{error}</p>}
      {result && (
        <p role="status" className={styles.muted}>
          {result.kind === "done" ? "已请求重新观察，等待执行主机回报。" : result.message}
        </p>
      )}
    </section>
  );
}
