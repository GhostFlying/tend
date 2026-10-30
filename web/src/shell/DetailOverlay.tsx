import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "../ui/Button.tsx";
import styles from "./DetailOverlay.module.css";

/** One modal owns both ledger detail views; hidden panes retain reading state. */
export function DetailOverlay({
  children,
  task,
  onClose,
  onBack,
  label,
  closeLabel = "关闭详情",
  backLabel = "返回工作项",
}: {
  children: ReactNode;
  task: boolean;
  label?: string | undefined;
  closeLabel?: string;
  backLabel?: string;
  onClose(): void;
  onBack?: (() => void) | undefined;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [full, setFull] = useState(false);
  useLayoutEffect(() => {
    const panel = dialog.current;
    const opener = document.activeElement;
    panel?.showModal();
    return () => {
      panel?.close();
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  }, []);
  return (
    <dialog
      ref={dialog}
      className={styles.dialog}
      data-full={full}
      aria-label={label ?? (task ? "任务详情" : "工作项详情")}
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className={styles.toolbar}>
        <Button variant="link" onClick={onBack ?? onClose}>
          {onBack ? `← ${backLabel}` : "← 返回"}
        </Button>
        <div className={styles.actions}>
          <Button variant="secondary" size="s" aria-pressed={full} onClick={() => setFull(!full)}>
            {full ? "退出全屏" : "全屏阅读"}
          </Button>
          <Button variant="ghost" icon aria-label={closeLabel} onClick={onClose}>
            ×
          </Button>
        </div>
      </div>
      {children}
    </dialog>
  );
}

export function DetailPane({ active, children }: { active: boolean; children: ReactNode }) {
  const pane = useRef<HTMLDivElement>(null);
  const position = useRef(0);
  const focus = useRef<HTMLElement | null>(null);
  useLayoutEffect(() => {
    if (active && pane.current) {
      pane.current.scrollTop = position.current;
      if (focus.current?.isConnected) focus.current.focus({ preventScroll: true });
      else pane.current.focus({ preventScroll: true });
    }
  }, [active]);
  return (
    <div
      ref={pane}
      className={styles.pane}
      tabIndex={-1}
      hidden={!active}
      onScroll={(event) => {
        if (active) position.current = event.currentTarget.scrollTop;
      }}
      onFocus={(event) => {
        focus.current = event.target;
      }}
    >
      {children}
    </div>
  );
}

export function DetailTabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: readonly { key: T; label: string }[];
  value: T;
  onChange(value: T): void;
}) {
  return (
    <nav className={styles.tabs} aria-label="详情内容">
      {tabs.map((tab) => (
        <button
          key={tab.key}
          type="button"
          aria-pressed={tab.key === value}
          onClick={() => onChange(tab.key)}
        >
          {tab.label}
        </button>
      ))}
    </nav>
  );
}
