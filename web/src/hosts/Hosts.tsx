import { useLayoutEffect, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { HostView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import controls from "../shell/controls.module.css";
import { hostMaintaining, hostMaintenanceLabel, hostOnline } from "./status.ts";
import styles from "./Hosts.module.css";

const phaseLabels: Record<string, string> = {
  maintenance: "等待维护",
  preparing: "准备维护",
  draining: "排空中",
  ready: "已排空",
  downloading: "下载中",
  installing: "安装中",
  restarting: "重启中",
  checking: "健康确认",
  normal: "正常",
};

export function Hosts({
  connection,
  expanded = false,
}: {
  connection: Connection;
  expanded?: boolean;
}) {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const [selected, setSelected] = useState<string | null>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const host = snapshot?.hosts?.find((h) => h.host_id === selected);
  const waiting = connection.status !== "online";
  const close = () => {
    setSelected(null);
    queueMicrotask(() => returnFocus.current?.isConnected && returnFocus.current.focus());
  };
  return (
    <section aria-label="执行主机占用" className={expanded ? styles.expanded : undefined}>
      <div className={styles.title}>
        执行主机{snapshot && ` · ${snapshot.hosts?.length ?? 0} 台`}
      </div>
      {snapshot && waiting && (
        <p className={styles.waiting} role="status">
          等待连接恢复，显示最后观察状态。
        </p>
      )}
      {(snapshot?.hosts ?? []).map((h) => (
        <button
          type="button"
          className={styles.row}
          data-expanded={expanded || undefined}
          key={h.host_id}
          aria-label={`主机详情：${h.host_id}`}
          title={`槽位占用：${h.open_runs} / ${h.slots} · ${h.os} / ${h.arch} · ${h.worker}`}
          onClick={(event) => {
            returnFocus.current = event.currentTarget;
            setSelected(h.host_id);
          }}
        >
          <span className={styles.identity}>
            <span className={styles.name}>{h.host_id}</span>
            <span className={styles.online} data-online={h.online}>
              {hostOnline(h)}
            </span>
          </span>
          <span className={styles.facts}>
            {hostMaintaining(h) && (
              <span className={styles.maintenance}>{hostMaintenanceLabel(h)}</span>
            )}
            <span>
              {h.online === true && !waiting ? (h.open_runs > 0 ? "执行中" : "空闲") : "记录占用"} ·{" "}
              {h.open_runs} / {h.slots} 槽位
            </span>
          </span>
        </button>
      ))}
      {snapshot && !snapshot.hosts?.length && (
        <p className={controls.empty}>暂无已登记的执行主机。</p>
      )}
      {host && <HostDetails host={host} waiting={waiting} onClose={close} />}
    </section>
  );
}

function HostDetails({
  host,
  waiting,
  onClose,
}: {
  host: HostView;
  waiting: boolean;
  onClose(): void;
}) {
  const { openUpgrade } = useServices();
  const panel = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    panel.current?.showModal();
  }, []);
  return (
    <dialog
      ref={panel}
      className={styles.panel}
      aria-label="主机详情"
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }}
      onClick={(event) => event.target === event.currentTarget && onClose()}
    >
      <div className={styles.header}>
        <h2>主机详情</h2>
        <button type="button" onClick={onClose} aria-label="关闭主机详情">
          ×
        </button>
      </div>
      <h3 className={styles.hostName}>{host.host_id}</h3>
      {waiting && <p className={styles.waiting}>等待连接恢复，显示最后观察状态。</p>}
      <dl className={styles.details}>
        <dt>连接</dt>
        <dd>{hostOnline(host)}</dd>
        <dt>运行模式</dt>
        <dd>
          {hostMaintaining(host)
            ? hostMaintenanceLabel(host)
            : host.maintenance?.mode === "normal"
              ? "正常"
              : "未报告"}
        </dd>
        <dt>槽位占用</dt>
        <dd>
          {host.open_runs} / {host.slots}
        </dd>
        <dt>最后心跳</dt>
        <dd>
          {host.last_heartbeat_at ? new Date(host.last_heartbeat_at).toLocaleString() : "未报告"}
        </dd>
        <dt>构建版本</dt>
        <dd>{host.build_version || "未报告"}</dd>
        <dt>通信协议</dt>
        <dd>{host.protocol_version ?? "未报告"}</dd>
        <dt>环境</dt>
        <dd>
          {host.os} / {host.arch} · {host.worker}
        </dd>
        {host.maintenance?.remaining_runs !== undefined && (
          <>
            <dt>剩余 run</dt>
            <dd>{host.maintenance.remaining_runs}</dd>
          </>
        )}
        {host.maintenance?.release_id && (
          <>
            <dt>发布</dt>
            <dd>{host.maintenance.release_id}</dd>
          </>
        )}
      </dl>
      {host.maintenance?.phase && (
        <p className={styles.waiting}>
          维护阶段：{phaseLabels[host.maintenance.phase] ?? host.maintenance.phase}
        </p>
      )}
      {host.maintenance?.upgrade_id && (
        <button
          type="button"
          onClick={() => {
            onClose();
            queueMicrotask(() => openUpgrade(host.maintenance!.upgrade_id!));
          }}
        >
          查看发布详情
        </button>
      )}
      {host.maintenance?.reason && <p className={styles.reason}>{host.maintenance.reason}</p>}
    </dialog>
  );
}
