import type { ReactNode } from "react";
import styles from "./Resources.module.css";

export function ResourceRow({
  children,
  meta,
  onOpen,
  dialog = false,
  current = false,
}: {
  children: ReactNode;
  meta?: ReactNode;
  onOpen(): void;
  dialog?: boolean;
  current?: boolean;
}) {
  return (
    <button
      type="button"
      className={styles.row}
      onClick={onOpen}
      aria-haspopup={dialog ? "dialog" : undefined}
      aria-current={current ? "page" : undefined}
    >
      <span>{children}</span>
      <span className={styles.meta}>
        {meta}
        <span aria-hidden> ›</span>
      </span>
    </button>
  );
}
