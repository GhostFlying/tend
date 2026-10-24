import { useEffect, useLayoutEffect, useRef } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Repositories.module.css";

export function RepositoryEntry({ onOpen }: { onOpen(): void }) {
  const { live } = useServices();
  const state = useStore(live.repos.state);
  const count = state.view ? (state.view.repos ?? []).length : undefined;
  return (
    <button type="button" className={styles.entry} onClick={onOpen} aria-haspopup="dialog">
      <span>已注册仓库</span>
      <span className={styles.entryCount}>
        {count === undefined ? (state.error ? "读取失败" : "待读取") : `${count} 个`}
        {count !== undefined && state.stale && " · 上次记录"} ›
      </span>
    </button>
  );
}

export function Repositories({ connection, onClose }: { connection: Connection; onClose(): void }) {
  const { live } = useServices();
  const registry = live.repos;
  const state = useStore(registry.state);
  const panel = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    panel.current?.showModal();
  }, []);
  useEffect(() => {
    void registry.refresh();
  }, [registry]);
  const online = connection.status === "online";
  const repos = state.view ? (state.view.repos ?? []) : null;
  return (
    <dialog
      ref={panel}
      className={styles.panel}
      aria-label="已注册仓库"
      onCancel={onClose}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <header className={styles.header}>
        <div>
          <h2>已注册仓库</h2>
          <span>{repos ? `全部 ${repos.length} 个 · 含已退役仓库` : "全部登记"}</span>
        </div>
        <button
          type="button"
          className={styles.close}
          onClick={onClose}
          aria-label="关闭已注册仓库"
          autoFocus
        >
          ×
        </button>
      </header>
      <div className={styles.body}>
        <p className={styles.note}>
          来源：Tend 账本登记。服务主机指已登记且未退役的执行主机配置，与在线状态和仓库是否已
          checkout 分别记录。
        </p>
        <div className={styles.readStatus} role="status" aria-live="polite">
          <span>
            {!online
              ? "连接已断开，当前登记待核对。"
              : state.loading
                ? "正在读取登记…"
                : state.error
                  ? "读取失败，请重试。"
                  : "已读取登记。"}
            {repos && state.stale && " 以下为上次成功读取的记录。"}
          </span>
          <button type="button" disabled={!online} onClick={() => void registry.refresh()}>
            {state.error ? "重试读取仓库" : "刷新仓库"}
          </button>
        </div>
        {repos && repos.length > 0 && (
          <ul className={styles.list} aria-label="仓库登记列表" aria-busy={state.loading}>
            {repos.map((repo) => (
              <li key={repo.repo} className={styles.repo} data-retired={repo.retired || undefined}>
                <div className={styles.repoHead}>
                  <h3>{repo.repo}</h3>
                  <span className={styles.badge}>{repo.retired ? "已退役" : "启用"}</span>
                </div>
                {repo.retired && (
                  <p className={styles.note}>停止新任务挂载，已有任务仍可由配置主机服务。</p>
                )}
                <p className={styles.hostTitle}>服务执行主机 · {(repo.hosts ?? []).length} 台</p>
                {(repo.hosts ?? []).length ? (
                  <ul className={styles.hosts}>
                    {(repo.hosts ?? []).map((host) => (
                      <li key={host}>{host}</li>
                    ))}
                  </ul>
                ) : (
                  <p className={styles.noHosts}>没有已登记且未退役的执行主机配置为服务此仓库。</p>
                )}
              </li>
            ))}
          </ul>
        )}
        {repos?.length === 0 && (
          <p className={styles.empty}>
            {state.stale ? "上次读取时没有已注册仓库。" : "账本中暂无已注册仓库。"}
          </p>
        )}
        {!repos && !state.loading && <p className={styles.empty}>尚未取得仓库登记，数量未知。</p>}
      </div>
    </dialog>
  );
}
