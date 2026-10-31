import type { Connection } from "../api/client.ts";
import type { AttentionPage } from "../api/generated/api.ts";
import { Link, useRoute } from "../app/router.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { useInboxCount } from "../attention/useInboxCount.ts";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import { StatusDot } from "./StatusDot.tsx";
import { groups, settings, navActive, type NavItem } from "./navigation.tsx";
import styles from "./Sidebar.module.css";

export function Sidebar({
  connection,
  summary,
}: {
  connection: Connection;
  summary: AttentionPage | undefined;
}) {
  const route = useRoute();
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const pending = useInboxCount(summary);
  const hosts = snapshot?.hosts ?? [];
  const hostStatus = !hosts.length
    ? "暂无主机"
    : hosts.every((host) => host.online === true)
      ? "全部主机在线"
      : "部分主机离线";
  const item = ({ path, label, Icon }: NavItem) => (
    <Link
      key={path}
      to={path}
      className={styles.item}
      aria-label={label}
      aria-description={path === "/inbox" ? pending.description : undefined}
      title={label}
      aria-current={navActive(route, path) ? "page" : undefined}
      onClick={path === "/tasks" ? () => live.tasks.setProject("") : undefined}
    >
      <Icon />
      <span className={styles.label}>{label}</span>
      {path === "/inbox" && pending.count > 0 && (
        <span className={styles.badge}>
          {pending.count}
          {pending.more ? "+" : ""}
        </span>
      )}
      {path === "/hosts" && (
        <span
          className={styles.hostStatus}
          title={connection.status === "online" ? hostStatus : `上次观察：${hostStatus}`}
          role="status"
          aria-label={connection.status === "online" ? hostStatus : `上次观察：${hostStatus}`}
        >
          <StatusDot
            tone={
              !hosts.length ? "idle" : hosts.every((host) => host.online === true) ? "ok" : "warn"
            }
          />
        </span>
      )}
    </Link>
  );
  return (
    <div className={styles.root}>
      <div className={styles.brand}>
        <div className={styles.logo} aria-hidden>
          t
        </div>
        <b>tend</b>
        <span className={styles.conn}>
          <span className={styles.fullConnection}>
            <ConnectionBadge connection={connection} />
          </span>
          <span className={styles.railConnection}>
            <ConnectionBadge connection={connection} compact />
          </span>
        </span>
      </div>
      <nav className={styles.scroll} aria-label="主导航">
        {groups.map((group) => (
          <section key={group.label} className={styles.group} aria-label={group.label}>
            <h2>{group.label}</h2>
            {group.items.map(item)}
          </section>
        ))}
      </nav>
      <nav className={styles.foot} aria-label="设置导航">
        {item(settings)}
      </nav>
    </div>
  );
}
