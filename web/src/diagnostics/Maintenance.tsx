import { useEffect, useState } from "react";
import type { MaintenanceOverview } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Button } from "../ui/index.ts";
import styles from "./Diagnostics.module.css";

export function Maintenance() {
  const { client } = useServices();
  const [value, setValue] = useState<MaintenanceOverview | null>(null);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    void client
      .get<MaintenanceOverview>("/api/v1/maintenance", abort.signal)
      .then((v) => {
        if (!abort.signal.aborted) setValue(v);
      })
      .catch(() => {
        if (!abort.signal.aborted) setValue(null);
      });
    return () => abort.abort();
  }, [client, refresh]);
  if (!value?.configured) return null;
  const time = (seconds?: number) =>
    seconds ? new Date(seconds * 1000).toLocaleString() : "尚未记录";
  return (
    <section aria-label="自维护状态" className={styles.maintenance}>
      <h2>自维护</h2>
      <p>{value.available ? (value.paused ? "已暂停新维护会话" : "监测中") : "维护服务不可达"}</p>
      <p>
        最近检测：{time(value.last_check)} · 最近审计：{time(value.last_audit)}
      </p>
      <p>
        待诊断问题 {value.open_cases} · 待处理线索 {value.pending}
      </p>
      {value.session_id ? (
        <p>
          {value.agent} · {value.phase} · 会话 <code>{value.session_id}</code>
        </p>
      ) : (
        <p>当前没有维护会话</p>
      )}
      {value.upgrade_phase ? <p>维护服务更新：{value.upgrade_phase}</p> : null}
      {value.error ? <p role="status">{value.error}</p> : null}
      <Button onClick={() => setRefresh((n) => n + 1)}>刷新维护状态</Button>
    </section>
  );
}
