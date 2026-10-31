import { Button, Input, Checkbox, Section } from "../ui/index.ts";
import { useEffect, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { SetIssueNotificationSettingsPayload } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import type { OpOutcome } from "../app/ops.ts";
import { useStore } from "../state/store.ts";
import styles from "../conversation/Handoff.module.css";

export function IssueNotificationSettings({ connection }: { connection: Connection }) {
  const { client, live } = useServices();
  const read = live.issues.notificationSettings;
  const state = useStore(read.state);
  const operation = useDurableOp(client, "issue-notification-settings");
  const [draft, setDraft] = useState<string | null>(null);
  const [draftRevision, setDraftRevision] = useState<number | null>(null);
  const [outcome, setOutcome] = useState<OpOutcome | null>(null);
  const [progress, setProgress] = useState("");
  const [timed, setTimed] = useState("3");
  useEffect(() => {
    void read.open("/api/v1/issue-notification-settings");
    return () => read.close();
  }, [read]);
  const original = operation.original?.payload as SetIssueNotificationSettingsPayload | undefined;
  const value = draft ?? String(original?.delay_minutes ?? state.value?.delay_minutes ?? 3);
  const minutes = Number(value);
  const immediate = value.trim() !== "" && minutes === 0;
  const valid = value.trim() !== "" && Number.isInteger(minutes) && minutes >= 0 && minutes <= 30;
  const disabled =
    connection.status !== "online" ||
    operation.busy ||
    operation.pending ||
    !!operation.storageError ||
    !state.value ||
    !!state.error;
  const change = (next: string) => {
    setDraftRevision((original) => original ?? state.value?.revision ?? null);
    setDraft(next);
  };
  const save = async (reconcile = false) => {
    if (!reconcile && (!valid || disabled || !state.value)) return;
    setProgress(reconcile ? "正在核对设置结果…" : "正在保存通知设置…");
    try {
      const result = await operation.execute(
        reconcile
          ? undefined
          : {
              operation: "issues.set_notification_settings",
              target: { type: "issue_settings" },
              expected_control_revision: draftRevision ?? state.value!.revision,
              payload: { delay_minutes: minutes },
            },
      );
      setOutcome(result);
      if (result.kind === "done" || result.kind === "stale") {
        setDraft(null);
        setDraftRevision(null);
      }
      await read.refresh();
      live.issues.board.refresh();
    } catch {
      setOutcome({ kind: "unknown", message: "尚未确认设置是否保存，请核对原操作。" });
    } finally {
      setProgress("");
    }
  };
  const message =
    progress ||
    operation.storageError ||
    (operation.pending
      ? "保存结果待确认，请先核对原操作。"
      : state.error
        ? "通知设置读取失败，当前内容可能已过期。"
        : operation.busy
          ? "正在核对本地操作记录…"
          : outcome?.kind === "done"
            ? "通知设置已保存。"
            : outcome?.kind === "stale"
              ? "设置已被更新，请读取最新值后重新调整。"
              : outcome
                ? outcome.message
                : "");
  return (
    <Section title="Issue 通知" className={styles.section} aria-label="Issue通知设置">
      <p className={styles.note}>合并连续的状态调整，减少对负责人的打扰。</p>
      <label className={styles.label} htmlFor="issue-notification-delay">
        状态变更通知延迟
      </label>
      <div className={styles.row}>
        <Input
          id="issue-notification-delay"
          type="number"
          min={1}
          max={30}
          step={1}
          value={immediate ? timed : value}
          disabled={disabled || immediate}
          onChange={(e) => {
            change(e.target.value);
            if (e.target.value) setTimed(e.target.value);
          }}
        />
        <span className={styles.unit}>分钟</span>
      </div>
      <p className={styles.note}>默认 3 分钟；可设为 1–30 分钟，适用于所有 Issue。</p>
      <Checkbox
        className={styles.toggle}
        label="立即通知"
        checked={immediate}
        disabled={disabled}
        onChange={(e) => {
          if (e.target.checked && valid && minutes > 0) setTimed(value);
          change(e.target.checked ? "0" : timed);
        }}
      />
      <p className={styles.note}>
        状态即时保存；停止调整后，只通知最后选定的状态。期间再次调整，将重新计时。
      </p>
      {draft !== null && !valid && (
        <p className={styles.feedback}>请输入 1–30 的整数，或选择立即通知。</p>
      )}
      {message && (
        <p
          className={styles.status}
          role="status"
          data-warning={
            operation.pending || !!state.error || (outcome && outcome.kind !== "done") || undefined
          }
        >
          {message}
        </p>
      )}
      {!message && (
        <p className={styles.note} role="status">
          {!valid
            ? "请输入 1–30 的整数，或选择立即通知。"
            : connection.status !== "online"
              ? "离线时不能修改设置。"
              : !state.value
                ? "正在读取设置…"
                : minutes === state.value.delay_minutes
                  ? "未修改通知设置。"
                  : "有未保存的修改。"}
        </p>
      )}
      {operation.pending ? (
        <Button
          variant="primary"
          disabled={operation.busy || connection.status !== "online"}
          onClick={() => void save(true)}
        >
          核对设置结果
        </Button>
      ) : state.error ? (
        <Button variant="secondary" onClick={() => void read.refresh()}>
          重新读取
        </Button>
      ) : (
        <Button
          variant="primary"
          disabled={disabled || !valid || minutes === state.value?.delay_minutes}
          onClick={() => void save()}
        >
          保存通知设置
        </Button>
      )}
    </Section>
  );
}
