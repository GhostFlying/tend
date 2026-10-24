// The phone's bottom tabs: the conversation, the tasks, what needs the owner.
import type { ComponentType, SVGProps } from "react";
import { Chat, Pending, Tasks } from "./icons.tsx";
import styles from "./TabBar.module.css";

export type View = "chat" | "tasks" | "decisions";

const tabs: { view: View; label: string; Icon: ComponentType<SVGProps<SVGSVGElement>> }[] = [
  { view: "chat", label: "对话", Icon: Chat },
  { view: "tasks", label: "工作项", Icon: Tasks },
  { view: "decisions", label: "待处理", Icon: Pending },
];

export function TabBar({
  view,
  onChange,
  pending,
}: {
  view: View;
  onChange: (view: View) => void;
  /** How many things wait for the owner. */
  pending: number;
}) {
  return (
    <nav className={styles.tabbar} aria-label="视图">
      {tabs.map(({ view: v, label, Icon }) => (
        <button
          key={v}
          type="button"
          className={styles.tab}
          aria-current={v === view ? "page" : undefined}
          onClick={() => onChange(v)}
        >
          <Icon />
          {label}
          {v === "decisions" && pending > 0 && <span className={styles.badge}>{pending}</span>}
        </button>
      ))}
    </nav>
  );
}
