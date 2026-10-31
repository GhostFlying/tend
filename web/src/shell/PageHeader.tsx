import type { ReactNode } from "react";
import { Button } from "../ui/Button.tsx";
import styles from "./PageHeader.module.css";

export function PageHeader({
  title,
  onBack,
  backLabel = "返回",
  actions,
  leading,
  children,
  className,
}: {
  title: string;
  onBack?: () => void;
  backLabel?: string;
  actions?: ReactNode;
  leading?: ReactNode;
  children?: ReactNode;
  className?: string | undefined;
}) {
  return (
    <header className={className ?? styles.header}>
      {leading}
      {onBack && (
        <Button variant="link" onClick={onBack}>
          ← {backLabel}
        </Button>
      )}
      <h1 className={styles.title} tabIndex={-1}>
        {title}
      </h1>
      {children}
      {actions && <div className={styles.actions}>{actions}</div>}
    </header>
  );
}
