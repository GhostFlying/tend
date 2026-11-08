import { Button, Section } from "../ui/index.ts";
import { Link } from "../app/router.ts";
import { SegmentedControl } from "../ui/Choices.tsx";
import { theme, themeLabels, setTheme } from "../theme/theme.ts";
import { PageHeader } from "../shell/PageHeader.tsx";
// Read-only index handoff status; model settings stay separate.
import { useEffect } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Handoff.module.css";
import { handoffStatus } from "./handoffStatus.ts";
import { IssueNotificationSettings } from "../issues/NotificationSettings.tsx";

export function Handoff({
  connection,
  onCatalog,
}: {
  connection: Connection;
  onCatalog?: () => void;
}) {
  const { client, live } = useServices();
  const handoff = live.handoff;
  const state = useStore(handoff.state);
  const choice = useStore(theme);

  useEffect(() => {
    handoff.start();
  }, [handoff]);
  useEffect(() => {
    if (connection.status === "online") {
      void handoff.refresh();
      void handoff.reconcile(client);
    }
  }, [connection.status, handoff, client]);
  const view = state.view;

  const online = connection.status === "online";
  const maintenance = view?.maintenance;
  const maintenanceFailed = maintenance?.status === "failed" || maintenance?.status === "unknown";
  const warning = !!state.readError || !!state.error || !!state.pendingId || maintenanceFailed;
  const status = handoffStatus(state);
  return (
    <section className={styles.panel} aria-label="设置">
      <PageHeader className={styles.pageHead} title="设置" />
      <Section className={styles.section} title="外观" aria-label="外观">
        <SegmentedControl
          label="主题"
          value={choice}
          onChange={setTheme}
          items={[
            { value: "system", label: themeLabels.system },
            { value: "light", label: themeLabels.light },
            { value: "dark", label: themeLabels.dark },
          ]}
        />
      </Section>
      {onCatalog && (
        <Link className={styles.catalogLink} to={{ page: "agents" }}>
          Agent 与类别<span aria-hidden="true">›</span>
        </Link>
      )}
      <Section className={styles.section} title="主 Agent" aria-label="主 Agent">
        <h3 className={styles.heading}>会话交接</h3>
        <p className={styles.status} role="status" data-warning={warning || undefined}>
          {status}
        </p>
        <p className={styles.note}>
          新会话通过索引按需读取当前状态和原始历史；交接不生成摘要。手动压缩独立保留。
        </p>
        {maintenanceFailed && (
          <p className={styles.feedback} role="status">
            {maintenance?.reason ||
              (maintenance?.status === "unknown"
                ? "暂时无法确认处理结果，请先查看当前交接状态。"
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
                    ? "离线时无法读取交接状态。"
                    : state.error}
            </p>
            {(state.readError || state.pendingId || state.error) && (
              <Button
                type="button"
                disabled={!online || state.saving}
                onClick={() => {
                  void (state.pendingId ? handoff.reconcile(client) : handoff.refresh());
                }}
              >
                {state.pendingId ? "核对交接结果" : "重新读取"}
              </Button>
            )}
          </div>
        )}
      </Section>
      <IssueNotificationSettings connection={connection} />
    </section>
  );
}
