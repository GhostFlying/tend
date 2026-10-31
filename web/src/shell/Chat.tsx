import { Link } from "../app/router.ts";
import { Menu } from "../ui/Menu.tsx";
import { MainPending } from "../attention/MainPending.tsx";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { ManualSource } from "../conversation/ManualSource.tsx";
import { Sheet } from "../ui/Modal.tsx";
import { Rollout } from "../conversation/Rollout.tsx";
import { PendingQueue } from "../conversation/PendingQueue.tsx";
import { UpgradeBanner } from "../upgrades/Banner.tsx";
// The one conversation with the main agent (D27): the transcript, and the
// seat docked at the bottom with notes and the composer.
import { useServices } from "../app/services.tsx";
import type { Connection } from "../api/client.ts";
import { dismissNotice, notice } from "../app/notice.ts";
import { Composer } from "../conversation/Composer.tsx";
import { Transcript } from "../conversation/Transcript.tsx";
import { DecisionDock } from "../decisions/DecisionDock.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Chat.module.css";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import { Button } from "../ui/Button.tsx";
import { ChevronDown, More } from "./icons.tsx";
import { OverlayVisibility, useOverlayState } from "../ui/focus.ts";

export function Chat({
  connection,
  visible,
  mainAttention,
  onAttention,
  onIssue,
}: {
  connection: Connection;
  visible: boolean;
  mainAttention?: ReadState<AttentionPage>;
  onAttention?: (id?: string) => void;
  onIssue?: (id: string) => void;
}) {
  const { conversation, openAgentDrawer } = useServices();
  const title = conversation?.title ?? "主 Agent";
  const [sourceOpen, setSourceOpen] = useOverlayState(false, visible);
  const [rolloutOpen, setRolloutOpen] = useOverlayState(false, visible);

  return (
    <OverlayVisibility value={visible}>
      <header className={styles.head}>
        <h1 tabIndex={-1} aria-label={title}>
          <Button
            variant="ghost"
            size="s"
            className={styles.switcher}
            title={title}
            onClick={openAgentDrawer}
            aria-label={`切换会话：${title}`}
          >
            <span>{title}</span>
            <ChevronDown />
          </Button>
        </h1>
        <span className={styles.mobileOnly}>
          <ConnectionBadge connection={connection} compact />
        </span>
        <span className={styles.sub}>
          {conversation?.kind === "consultation"
            ? "独立咨询 · 普通问答不创建任务"
            : conversation?.workItemID
              ? "讨论目标、规划与委派任务"
              : "一个会话，所有设备共享"}
        </span>
        <span className={styles.spacer} />
        {conversation?.workItemID && (
          <Link className={styles.issueLink} to={{ page: "issue", id: conversation.workItemID }}>
            Issue
          </Link>
        )}
        <Menu
          label="更多"
          variant="ghost"
          size="s"
          icon
          items={[
            { id: "source", label: "补充来源", onSelect: () => setSourceOpen(true) },
            { id: "rollout", label: "执行记录", onSelect: () => setRolloutOpen(true) },
          ]}
        >
          <More />
        </Menu>
      </header>
      {conversation?.kind === "consultation" && (
        <div className={styles.roleNote}>
          独立咨询 · 普通问答不创建任务
          {conversation.agentRef && <span>咨询 Agent：{conversation.agentRef}</span>}
        </div>
      )}
      {conversation?.workItemID && (
        <div className={styles.roleNote}>
          Issue 负责人 · 澄清目标、规划与委派
          {conversation.agentRef && <span>负责人 Agent：{conversation.agentRef}</span>}
        </div>
      )}
      {sourceOpen && (
        <Sheet open title="补充来源" onClose={() => setSourceOpen(false)}>
          <ManualSource onBack={() => setSourceOpen(false)} />
        </Sheet>
      )}
      <Rollout open={rolloutOpen} onClose={() => setRolloutOpen(false)} />
      <UpgradeBanner connection={connection} />
      <div className={styles.body}>
        <Transcript visible={visible} />
        <div className={styles.seat}>
          <div className={styles.seatInner}>
            <Note />
            <div className={styles.seatScroll}>
              {conversation?.id === "main" && mainAttention?.value && onAttention && onIssue && (
                <MainPending summary={mainAttention} />
              )}
              {!conversation?.workItemID && !mainAttention?.value && <DecisionDock />}
              <PendingQueue connection={connection} />
            </div>
            <Composer connection={connection} />
          </div>
        </div>
      </div>
    </OverlayVisibility>
  );
}

export function Note() {
  const n = useStore(notice);
  if (!n) {
    return null;
  }
  return (
    <div className={styles.note} data-tone={n.tone} role="status">
      <span>{n.text}</span>
      <Button type="button" variant="link" onClick={dismissNotice}>
        知道了
      </Button>
    </div>
  );
}
