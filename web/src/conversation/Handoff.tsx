// Independent owner preferences for idle preparation; model settings stay separate.
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Handoff.module.css";

export function Handoff({ connection, onClose }: { connection: Connection; onClose(): void }) {
  const { client, live } = useServices();
  const handoff = live.handoff;
  const state = useStore(handoff.state);

  const [draft, setDraft] = useState<string | null>(null);

  const panel = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    handoff.start();
  }, [handoff]);
  useEffect(() => {
    if (connection.status === "online") {
      void handoff.refresh();
      void handoff.reconcile(client);
    }
  }, [connection.status, handoff, client]);
  useLayoutEffect(() => {
    panel.current?.showModal();
  }, []);
  const view = state.view;

  const online = connection.status === "online";
  const blocked =
    !online || !view?.enabled || state.saving || !!state.pendingId || !!state.readError;
  const value = draft ?? String(view?.idle_minutes ?? 50);
  const minutes = Number(value);
  const valid =
    value.trim() !== "" && Number.isInteger(minutes) && minutes >= 0 && minutes <= 10080;
  const automatic = value.trim() === "" || minutes !== 0;
  const maintenance = view?.maintenance;
  const maintenanceFailed = maintenance?.status === "failed" || maintenance?.status === "unknown";
  const preparing =
    !!state.preparationRequestId ||
    !!view?.pending ||
    !!view?.preparing ||
    ["pending", "sending"].includes(maintenance?.status ?? "");
  const warning = !!state.readError || !!state.error || !!state.pendingId || maintenanceFailed;
  const status =
    view?.enabled === false
      ? "未启用托管主会话"
      : !view && !state.readError
        ? "正在读取…"
        : state.saving
          ? "保存中"
          : state.pendingId
            ? "待核对"
            : state.readError
              ? "读取失败"
              : state.error
                ? "设置失败"
                : preparing
                  ? view?.preparing
                    ? "正在生成预备摘要"
                    : "请求已排队"
                  : maintenanceFailed
                    ? maintenance?.status === "unknown"
                      ? "上次请求结果待确认"
                      : "上次请求未完成"
                    : view?.summary?.current
                      ? "预备摘要已就绪"
                      : view?.summary
                        ? "预备摘要已过期"
                        : "尚无预备摘要";
  return (
    <dialog
      ref={panel}
      className={styles.panel}
      aria-label="设置"
      onCancel={onClose}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className={styles.header}>
        <h2>设置</h2>
        <button type="button" onClick={onClose} aria-label="关闭设置">
          ×
        </button>
      </div>
      <section aria-label="主 agent">
        <h2 className={styles.sectionHeading}>主 agent</h2>
        <h3 className={styles.heading}>预备摘要</h3>
        <p className={styles.status} role="status" data-warning={warning || undefined}>
          {status}
        </p>
        {view?.summary && (
          <p className={styles.note}>
            生成于{" "}
            {new Date(view.summary.created_at_millis).toLocaleString([], {
              month: "numeric",
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
            })}
          </p>
        )}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (!blocked && valid && minutes !== view?.idle_minutes) {
              setDraft(null);
              void handoff.save(client, { idle_minutes: minutes });
            }
          }}
        >
          <label className={styles.toggle}>
            <input
              type="checkbox"
              checked={automatic}
              disabled={blocked}
              onChange={(event) => setDraft(event.target.checked ? "50" : "0")}
            />
            自动准备摘要
          </label>
          <label className={styles.label} htmlFor={`${id}-minutes`}>
            空闲分钟
          </label>
          <div className={styles.row}>
            <input
              id={`${id}-minutes`}
              type="number"
              inputMode="numeric"
              min={0}
              max={10080}
              step={1}
              value={value}
              onChange={(event) => setDraft(event.target.value)}
              disabled={blocked || !automatic}
              aria-describedby={`${id}-help`}
            />
            <button
              type="submit"
              className={styles.save}
              disabled={blocked || !valid || minutes === view?.idle_minutes}
            >
              保存
            </button>
          </div>
        </form>
        <p id={`${id}-help`} className={styles.note}>
          {view?.idle_minutes === 0
            ? "自动准备已关闭。"
            : `空闲 ${view?.idle_minutes ?? 50} 分钟后自动准备。`}{" "}
          0 为关闭，最多 10080 分钟。
        </p>
        <p className={styles.note}>摘要供会话交接时参考，原会话历史仍保留。</p>
        <button
          type="button"
          className={styles.prepare}
          disabled={blocked || preparing}
          onClick={() => void handoff.prepare(client)}
        >
          立即生成预备摘要
        </button>
        {maintenanceFailed && (
          <p className={styles.feedback} role="status">
            {maintenance?.reason ||
              (maintenance?.status === "unknown"
                ? "暂时无法确认处理结果，请先查看当前摘要状态。"
                : "这次请求未完成，可以重新读取状态。")}
          </p>
        )}
        {(!online || state.readError || state.error || state.pendingId) && (
          <div className={styles.feedback}>
            <p>
              {state.pendingId
                ? "操作结果待核对，不会重复提交。"
                : state.readError
                  ? "读取失败，正在重试。"
                  : !online
                    ? "离线时不能修改设置。"
                    : state.error}
            </p>
            {(state.readError || state.pendingId || state.error) && (
              <button
                type="button"
                disabled={!online || state.saving}
                onClick={() => {
                  void (state.pendingId ? handoff.reconcile(client) : handoff.refresh());
                }}
              >
                {state.pendingId ? "核对设置结果" : "重新读取"}
              </button>
            )}
          </div>
        )}
      </section>
    </dialog>
  );
}
