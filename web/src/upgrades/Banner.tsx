import { Button } from "../ui/index.ts";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { batchStatuses } from "./status.ts";
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
      <span>正在更新到新版本</span>
      <span className={styles.hint}>
        {connection.status === "online" ? "查看详情 ›" : "等待连接恢复 · 最后观察"}
      </span>
    </Button>
  );
}
