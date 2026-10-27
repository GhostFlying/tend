// The composer's compact model seat: a two-level menu shared across devices.
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { Back, Chevron } from "../shell/icons.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Settings.module.css";
import { useAnchoredPanel } from "./useAnchoredPanel.ts";

type Pane = "root" | "model" | "effort";
const effortLabel = (effort: string) => effort || "默认";

export function Settings({ connection }: { connection: Connection }) {
  const { client, live } = useServices();
  const settings = live.settings;
  const state = useStore(settings.state);
  const [open, setOpen] = useState(false);
  const [pane, setPane] = useState<Pane>("root");
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const rootFocus = useRef("模型");
  const id = useId();
  const position = useAnchoredPanel(open, trigger, 240);

  useEffect(() => {
    settings.start();
  }, [settings]);
  useEffect(() => {
    if (connection.status === "online") {
      void settings.refresh();
      void settings.reconcile(client);
    }
  }, [settings, client, connection.status]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !root.current?.contains(event.target) &&
        !menu.current?.contains(event.target)
      )
        setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  useLayoutEffect(() => {
    if (!open || position.visibility === "hidden") return;
    const current = menu.current;
    const preferred =
      pane === "root"
        ? current?.querySelector<HTMLButtonElement>(
            `button[aria-label="${rootFocus.current}"]:not(:disabled)`,
          )
        : (current?.querySelector<HTMLButtonElement>('[aria-checked="true"]:not(:disabled)') ??
          current?.querySelector<HTMLButtonElement>('[role="menuitemradio"]:not(:disabled)'));
    (
      preferred ??
      current?.querySelector<HTMLButtonElement>("button:not(:disabled)") ??
      current
    )?.focus();
  }, [open, pane, position.visibility]);

  useLayoutEffect(() => {
    if (!open) return;
    const active = document.activeElement;
    // Disabling a focused recovery action during its request can move focus
    // to the body. Keep Escape and Tab owned by the still-open menu.
    if (
      active === document.body ||
      (active instanceof HTMLButtonElement && active.disabled && menu.current?.contains(active))
    ) {
      menu.current?.focus();
    }
  });

  const view = state.view;
  if (view?.enabled === false || (!view && !state.readError)) return null;
  const model = view?.models?.find((m) => m.id === view.model);
  const modelLabel = model?.label || view?.model || "选择模型";
  const efforts = model?.supported_efforts ?? [];
  const online = connection.status === "online";
  const disabled =
    !online || !view?.available || state.saving || !!state.pendingId || !!state.readError;
  const status = state.saving
    ? "保存中"
    : state.pendingId
      ? "待核对"
      : state.readError
        ? "读取失败"
        : !view?.available
          ? "暂不可用"
          : state.error
            ? "设置失败"
            : !online
              ? "离线"
              : "";
  const warning = !!state.pendingId || !!state.readError || !!state.error || !view?.available;
  const message = state.saving
    ? "正在保存设置…"
    : state.pendingId
      ? "设置结果待核对，不会重复提交。"
      : state.readError
        ? "没能读取设置，正在重试…"
        : !view?.available
          ? "模型目录暂不可用，正在重试…"
          : !online
            ? "离线时不能修改设置。"
            : "";

  const close = (restore = false) => {
    setOpen(false);
    if (restore) trigger.current?.focus();
  };
  const show = (last = false) => {
    rootFocus.current = last ? "思考强度" : "模型";
    setPane("root");
    setOpen(true);
    void settings.refresh();
  };
  const back = () => {
    rootFocus.current = pane === "effort" ? "思考强度" : "模型";
    setPane("root");
  };
  const choose = (modelId: string, effort: string) => {
    close(true);
    if (modelId !== view?.model || effort !== view?.effort)
      void settings.save(client, { model: modelId, effort });
  };
  const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!open) return;
    if (event.key === "Tab") {
      // Leave the whole menu as one control in the composer's tab order.
      close(true);
      return;
    }
    if (event.key === "Escape" || (event.key === "ArrowLeft" && pane !== "root")) {
      event.preventDefault();
      event.stopPropagation();
      if (pane !== "root") back();
      else close(true);
      return;
    }
    if (event.key === "ArrowRight" && pane === "root") {
      const focused = document.activeElement;
      if (focused instanceof HTMLButtonElement && focused.dataset.pane) {
        event.preventDefault();
        focused.click();
      }
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const items = [
      ...(menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []),
    ];
    const index = items.findIndex((item) => item === document.activeElement);
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? items.length - 1
          : index < 0
            ? event.key === "ArrowUp"
              ? items.length - 1
              : 0
            : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
    items[next]?.focus();
  };

  return (
    <div
      ref={root}
      className={styles.root}
      onKeyDown={keyDown}
      onBlur={(event) => {
        if (
          !(event.relatedTarget instanceof Node) ||
          (!root.current?.contains(event.relatedTarget) &&
            !menu.current?.contains(event.relatedTarget))
        )
          close();
      }}
    >
      <button
        ref={trigger}
        type="button"
        className={styles.trigger}
        aria-label="主会话设置"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? `${id}-menu` : undefined}
        title={`${modelLabel} · ${effortLabel(view?.effort ?? "")} ${status}`}
        onClick={() => (open ? close() : show())}
        onKeyDown={(event) => {
          if (!open && ["ArrowDown", "ArrowUp"].includes(event.key)) {
            event.preventDefault();
            event.stopPropagation();
            show(event.key === "ArrowUp");
          }
        }}
      >
        <span className={styles.triggerLabel}>{modelLabel}</span>
        <span
          className={styles.triggerEffort}
          data-warning={warning || undefined}
          role={status ? "status" : undefined}
        >
          {status || effortLabel(view?.effort ?? "")}
        </span>
        <Chevron className={styles.chevron} data-open={open || undefined} />
      </button>
      {open &&
        createPortal(
          <div
            ref={menu}
            id={`${id}-menu`}
            className={styles.menu}
            style={position}
            role="menu"
            tabIndex={-1}
            aria-label={
              pane === "root" ? "主会话设置菜单" : pane === "model" ? "选择模型" : "选择思考强度"
            }
            aria-busy={state.saving}
          >
            {pane !== "root" && (
              <button
                type="button"
                role="menuitem"
                tabIndex={-1}
                aria-label="返回主菜单"
                className={styles.back}
                onClick={back}
              >
                <Back />
                {pane === "model" ? "模型" : "思考强度"}
              </button>
            )}
            <div className={styles.options}>
              {pane === "root" ? (
                <>
                  <button
                    type="button"
                    role="menuitem"
                    tabIndex={-1}
                    aria-label="模型"
                    aria-describedby={`${id}-model`}
                    data-pane="model"
                    className={styles.cell}
                    disabled={disabled || !view?.models?.length}
                    onClick={() => setPane("model")}
                  >
                    <span>模型</span>
                    <span id={`${id}-model`} className={styles.cellValue}>
                      {modelLabel}
                    </span>
                    <Chevron />
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    tabIndex={-1}
                    aria-label="思考强度"
                    aria-describedby={`${id}-effort`}
                    data-pane="effort"
                    className={styles.cell}
                    disabled={disabled || efforts.length === 0}
                    onClick={() => setPane("effort")}
                  >
                    <span>思考强度</span>
                    <span id={`${id}-effort`} className={styles.cellValue}>
                      {effortLabel(view?.effort ?? "")}
                    </span>
                    <Chevron />
                  </button>
                </>
              ) : pane === "model" ? (
                view?.models?.map((choice) => (
                  <button
                    key={choice.id}
                    type="button"
                    role="menuitemradio"
                    tabIndex={-1}
                    aria-label={choice.label || choice.id}
                    aria-checked={choice.id === view.model}
                    title={choice.label || choice.id}
                    className={styles.option}
                    disabled={disabled}
                    onClick={() =>
                      choose(
                        choice.id,
                        choice.id === view.model ? view.effort : choice.default_effort,
                      )
                    }
                  >
                    <span className={styles.optionLabel}>{choice.label || choice.id}</span>
                    <span className={styles.check} aria-hidden>
                      {choice.id === view.model ? "✓" : ""}
                    </span>
                  </button>
                ))
              ) : (
                efforts.map((effort) => (
                  <button
                    key={effort}
                    type="button"
                    role="menuitemradio"
                    tabIndex={-1}
                    aria-label={effortLabel(effort)}
                    aria-checked={effort === view?.effort}
                    className={styles.option}
                    disabled={disabled}
                    onClick={() => {
                      if (view) choose(view.model, effort);
                    }}
                  >
                    <span className={styles.optionLabel}>{effortLabel(effort)}</span>
                    <span className={styles.check} aria-hidden>
                      {effort === view?.effort ? "✓" : ""}
                    </span>
                  </button>
                ))
              )}
            </div>
            {(message || state.error) && (
              <div className={styles.status} data-warning={warning || undefined} role="status">
                {message}
                {state.error && <span>{state.error}</span>}
              </div>
            )}
            {(state.pendingId || state.readError || (view && !view.available)) && (
              <button
                type="button"
                role="menuitem"
                tabIndex={-1}
                className={styles.recover}
                disabled={state.saving || !online}
                onClick={() =>
                  void (state.pendingId ? settings.reconcile(client) : settings.refresh())
                }
              >
                {state.pendingId ? "核对设置" : "重新读取"}
              </button>
            )}
            <div className={styles.note}>
              用于下一次开始的回复，含排队消息。
              {model && efforts.length === 0 && "该模型使用默认思考强度。"}
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}
