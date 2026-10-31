import { Button } from "../ui/index.ts";
import { PageHeader } from "../shell/PageHeader.tsx";
import type { UpgradeView } from "../api/generated/api.ts";
import { useEffect, useState } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { batchPhases, batchStatuses, hostPhases } from "./status.ts";
import styles from "./Upgrade.module.css";

export function UpgradeBanner({ connection }: { connection: Connection }) {
  const { live, openUpgrade } = useServices();
  const upgrade = useStore(live.snapshot)?.upgrade;
  if (!upgrade || upgrade.status === "completed" || upgrade.status === "cancelled") return null;
  return (
    <Button
      type="button"
      className={styles.banner}
      data-attention={upgrade.status === "attention" || undefined}
      aria-label="查看发布进度"
      onClick={() => openUpgrade(upgrade.upgrade_id)}
    >
      <strong>{batchStatuses[upgrade.status] ?? "发布状态"}</strong>
      <span>{upgrade.release_id}</span>
      <span className={styles.hint}>
        {connection.status === "online" ? "查看详情 ›" : "等待连接恢复 · 最后观察"}
      </span>
    </Button>
  );
}

export function UpgradeDetails({
  id,
  connection,
  onClose,
}: {
  id: string;
  connection: Connection;
  onClose(): void;
}) {
  const { live, client } = useServices();
  const current = useStore(live.snapshot)?.upgrade;
  const matching = current?.upgrade_id === id ? current : undefined;
  const [history, setHistory] = useState<UpgradeView | null>(null);
  const [readError, setReadError] = useState(false);
  const [retry, setRetry] = useState(0);
  const upgrade = matching ?? (history?.upgrade_id === id ? history : null);
  if (matching && history !== matching) setHistory(matching);
  useEffect(() => {
    if (matching) return;
    if (connection.status !== "online") return;
    const controller = new AbortController();
    void client
      .get<{ upgrade: UpgradeView }>(
        `/api/v1/upgrades/${encodeURIComponent(id)}`,
        controller.signal,
      )
      .then((response) => {
        if (!controller.signal.aborted) {
          setHistory(response.upgrade);
          setReadError(false);
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setReadError(true);
      });
    return () => controller.abort();
  }, [id, matching, client, connection.status, retry]);
  return (
    <section className={styles.panel} aria-label="发布详情">
      <PageHeader title="发布详情" onBack={onClose} />
      {connection.status !== "online" && (
        <p className={styles.note} role="status">
          等待连接恢复，显示最后观察状态。
        </p>
      )}
      {readError && !matching && (
        <p className={styles.reason}>
          读取发布详情失败。{" "}
          <Button type="button" onClick={() => setRetry((n) => n + 1)}>
            重试读取
          </Button>
        </p>
      )}
      {!upgrade ? (
        <p className={styles.note}>
          {connection.status === "online" ? "正在读取发布详情…" : "等待连接恢复。"}
        </p>
      ) : (
        <>
          <h3>{upgrade.release_id}</h3>
          <p className={styles.status} data-attention={upgrade.status === "attention" || undefined}>
            {batchStatuses[upgrade.status] ?? upgrade.status}
          </p>
          <p>{batchPhases[upgrade.phase] ?? upgrade.phase}</p>
          <p className={styles.note}>最后更新：{new Date(upgrade.updated_at).toLocaleString()}</p>
          {upgrade.reason && <p className={styles.reason}>{upgrade.reason}</p>}
          <ol className={styles.hosts}>
            {upgrade.hosts?.map((host) => (
              <li key={host.host_id}>
                <h4>{host.host_id}</h4>
                <p>
                  {hostPhases[host.phase] ?? host.phase}
                  {host.status === "attention" ? " · 需要处理" : ""}
                </p>
                <dl>
                  <dt>版本</dt>
                  <dd>
                    {host.current_version || "未报告"} → {host.target_version || "未报告"}
                  </dd>
                  <dt>通信协议</dt>
                  <dd>{host.protocol_version}</dd>
                  <dt>剩余 run</dt>
                  <dd>{host.remaining_runs ?? 0}</dd>
                  {!!host.total_bytes && (
                    <>
                      <dt>下载</dt>
                      <dd>
                        {(host.downloaded_bytes ?? 0).toLocaleString()} /{" "}
                        {host.total_bytes.toLocaleString()} 字节
                      </dd>
                    </>
                  )}
                </dl>
                {host.reason && <p className={styles.reason}>{host.reason}</p>}
              </li>
            ))}
          </ol>
          <p className={styles.note}>重试或取消发布请使用 CLI。此面板仅查看进度。</p>
        </>
      )}
    </section>
  );
}
