import { useId, useLayoutEffect, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import { ServicesContext, useServices } from "../app/services.tsx";
import { Hosts } from "../hosts/Hosts.tsx";
import { hostMaintaining } from "../hosts/status.ts";
import { RepositoryRegistry } from "../repos/Repositories.tsx";
import { RecentTasks } from "../tasks/Board.tsx";
import { useStore } from "../state/store.ts";
import { Close } from "../shell/icons.tsx";
import styles from "./WorkItems.module.css";

export function BoardResourceEntry({
  connection,
  onRecords,
}: {
  connection: Connection;
  onRecords(): void;
}) {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const close = () => {
    setOpen(false);
    queueMicrotask(() => trigger.current?.focus({ preventScroll: true }));
  };
  return (
    <>
      <button
        ref={trigger}
        type="button"
        className={styles.resourceEntry}
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
      >
        <svg
          width="20"
          height="20"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          aria-hidden="true"
        >
          <rect x="3" y="4" width="18" height="6" rx="2" />
          <rect x="3" y="14" width="18" height="6" rx="2" />
          <path d="M7 7h.01M7 17h.01M11 7h6m-6 10h6" />
        </svg>
        <span>
          资源与记录
          <small>
            {snapshot ? `${snapshot.hosts?.length ?? 0} 台主机` : "主机待读取"}
            {!!snapshot?.hosts?.filter(hostMaintaining).length &&
              ` · ${snapshot.hosts.filter(hostMaintaining).length} 台维护`}
          </small>
        </span>
      </button>
      {open && (
        <BoardResourcePanel
          connection={connection}
          onClose={close}
          onRecords={() => {
            close();
            onRecords();
          }}
          onNavigate={() => {
            setOpen(false);
            // Task details capture their return focus before the panel unmounts.
            trigger.current?.focus({ preventScroll: true });
          }}
        />
      )}
    </>
  );
}

const categories = [
  { id: "hosts", title: "执行主机" },
  { id: "repos", title: "已注册仓库" },
  { id: "recent", title: "最近完成" },
] as const;

function BoardResourcePanel({
  connection,
  onClose,
  onRecords,
  onNavigate,
}: {
  connection: Connection;
  onClose(): void;
  onRecords(): void;
  onNavigate(): void;
}) {
  const services = useServices();
  const [category, setCategory] = useState("hosts");
  const panel = useRef<HTMLDialogElement>(null);
  const id = useId();
  useLayoutEffect(() => {
    panel.current?.showModal();
  }, []);
  return (
    <dialog
      ref={panel}
      className={`${styles.dialog} ${styles.resourceDialog}`}
      aria-label="资源与记录"
      onCancel={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (
          event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom
        )
          onClose();
      }}
    >
      <header className={styles.detailHead}>
        <div>
          <h2>资源与记录</h2>
          <p className={styles.meta}>全局信息 · 不随项目筛选改变</p>
        </div>
        <button
          className={styles.roundButton}
          onClick={onClose}
          aria-label="关闭资源与记录"
          autoFocus
        >
          <Close width={20} height={20} />
        </button>
      </header>
      <div className={styles.resourceBody}>
        <div className={styles.resourceTabs} role="tablist" aria-label="资源分类">
          {categories.map((entry) => (
            <button
              key={entry.id}
              type="button"
              role="tab"
              id={`${id}-${entry.id}`}
              aria-controls={`${id}-content`}
              aria-selected={category === entry.id}
              tabIndex={category === entry.id ? 0 : -1}
              onClick={() => setCategory(entry.id)}
              onKeyDown={(event) => {
                const index = categories.findIndex((value) => value.id === category);
                const next =
                  event.key === "Home"
                    ? 0
                    : event.key === "End"
                      ? 2
                      : event.key === "ArrowRight"
                        ? (index + 1) % 3
                        : event.key === "ArrowLeft"
                          ? (index + 2) % 3
                          : undefined;
                if (next === undefined) return;
                event.preventDefault();
                const value = categories[next]!;
                setCategory(value.id);
                document.getElementById(`${id}-${value.id}`)?.focus();
              }}
            >
              {entry.title}
            </button>
          ))}
        </div>
        <ServicesContext
          value={{
            ...services,
            openTask(id) {
              panel.current?.close();
              onNavigate();
              services.openTask(id);
            },
          }}
        >
          <section id={`${id}-content`} role="tabpanel" aria-labelledby={`${id}-${category}`}>
            {category === "hosts" && <Hosts connection={connection} expanded />}
            {category === "repos" && <RepositoryRegistry connection={connection} summary />}
            {category === "recent" && (
              <>
                <p className={styles.meta}>任务完成后，所属工作项可以继续跟进。</p>
                <RecentTasks expanded />
              </>
            )}
          </section>
        </ServicesContext>
        <button className={styles.recordsButton} onClick={onRecords}>
          查看执行记录
        </button>
      </div>
    </dialog>
  );
}
