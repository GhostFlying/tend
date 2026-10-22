import { useEffect } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import controls from "../shell/controls.module.css";
import styles from "./Settings.module.css";

const effortLabels: Record<string, string> = {
  none: "无",
  minimal: "最低",
  low: "低",
  medium: "中",
  high: "高",
  xhigh: "更高",
  max: "最高",
};

export function Settings({ connection }: { connection: Connection }) {
  const { client, live } = useServices();
  const settings = live.settings;
  const state = useStore(settings.state);
  useEffect(() => {
    settings.start();
  }, [settings]);
  useEffect(() => {
    if (connection.status === "online") {
      void settings.refresh();
      void settings.reconcile(client);
    }
  }, [settings, client, connection.status]);
  const view = state.view;
  if (!view?.enabled) return null;
  const model = view.models?.find((m) => m.id === view.model);
  const efforts = model?.supported_efforts ?? [];
  const disabled =
    connection.status !== "online" ||
    !view.available ||
    state.saving ||
    !!state.pendingId ||
    !!state.readError;
  const save = (id: string, effort: string) => {
    void settings.save(client, { model: id, effort });
  };
  return (
    <div className={styles.settings} aria-label="主会话设置">
      <div className={styles.fields}>
        <label>
          模型
          <select
            aria-label="主会话模型"
            value={view.model}
            disabled={disabled || !view.models?.length}
            onChange={(e) => {
              const next = view.models?.find((m) => m.id === e.target.value);
              if (next) save(next.id, next.default_effort);
            }}
          >
            {!model && <option value={view.model}>{view.model || "等待模型目录"}</option>}
            {view.models?.map((m) => (
              <option key={m.id} value={m.id}>
                {m.label || m.id}
              </option>
            ))}
          </select>
        </label>
        <label>
          思考强度
          <select
            aria-label="思考强度"
            value={view.effort}
            disabled={disabled || efforts.length === 0}
            onChange={(e) => save(view.model, e.target.value)}
          >
            {!efforts.includes(view.effort) && (
              <option value={view.effort}>
                {effortLabels[view.effort] ?? (view.effort || "默认")}
              </option>
            )}
            {efforts.map((effort) => (
              <option key={effort} value={effort}>
                {effortLabels[effort] ?? effort}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className={styles.hint} role="status">
        {state.saving
          ? "正在保存…"
          : !view.available
            ? "模型目录暂不可用，正在重试…"
            : state.pendingId
              ? "设置结果待核对"
              : "用于下一次开始的回复（含排队消息）"}
        {(state.error || state.readError) && (
          <span className={styles.error}>{state.error || "没能读取设置，正在重试…"}</span>
        )}
        {(state.pendingId || state.readError) && (
          <button
            type="button"
            className={controls.link}
            disabled={state.saving || connection.status !== "online"}
            onClick={() => void (state.pendingId ? settings.reconcile(client) : settings.refresh())}
          >
            {state.pendingId ? "核对设置" : "重新读取"}
          </button>
        )}
      </div>
    </div>
  );
}
