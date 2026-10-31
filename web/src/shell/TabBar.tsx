import type { AttentionPage } from "../api/generated/api.ts";
import { Link, useRoute } from "../app/router.ts";
import { useInboxCount } from "../attention/useInboxCount.ts";
import { phoneTabs, navActive } from "./navigation.tsx";
import styles from "./TabBar.module.css";

export function TabBar({ summary }: { summary: AttentionPage | undefined }) {
  const route = useRoute();
  const pending = useInboxCount(summary);
  return (
    <nav className={styles.tabbar} aria-label="视图">
      {phoneTabs.map(({ path, label, Icon }) => (
        <Link
          key={path}
          to={path}
          className={styles.tab}
          aria-label={label}
          aria-current={navActive(route, path) ? "page" : undefined}
        >
          <Icon />
          {label}
          {path === "/inbox" && pending.count > 0 && (
            <span className={styles.badge}>
              {pending.count}
              {pending.more ? "+" : ""}
            </span>
          )}
        </Link>
      ))}
    </nav>
  );
}
