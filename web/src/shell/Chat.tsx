import { PageHeader } from "./PageHeader.tsx";
import { MainPending } from "../attention/MainPending.tsx";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { ManualSource } from "../conversation/ManualSource.tsx";
import { Sheet } from "../ui/Modal.tsx";
import { Rollout } from "../conversation/Rollout.tsx";
import { PendingQueue } from "../conversation/PendingQueue.tsx";
import { UpgradeBanner } from "../upgrades/Upgrade.tsx";
// The one conversation with the main agent (D27): the transcript, and the
// seat docked at the bottom with notes and the composer.
import { useServices } from "../app/services.tsx";
import { useState } from "react";
import type { Connection } from "../api/client.ts";
import { dismissNotice, notice } from "../app/notice.ts";
import { Composer } from "../conversation/Composer.tsx";
import { Transcript } from "../conversation/Transcript.tsx";
import { DecisionDock } from "../decisions/DecisionDock.tsx";
import { theme, themeLabels, toggleTheme } from "../theme/theme.ts";
import { useStore } from "../state/store.ts";
import styles from "./Chat.module.css";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import { Button } from "../ui/Button.tsx";
import { ArrowDown } from "./icons.tsx";

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
  const choice = useStore(theme);
  const { conversation, openAgentDrawer, selectConversation, openIssue } = useServices();
  const [jump, setJump] = useState(0);
  const [sourceOpen, setSourceOpen] = useState(false);
  const toLatest = () => setJump((n) => n + 1);

  return (
    <>
      <PageHeader className={styles.head} title={conversation?.title ?? "主 Agent"}>
        <span className={styles.sub}>
          {conversation?.kind === "consultation"
            ? "独立咨询 · 普通问答不创建任务"
            : conversation?.workItemID
              ? "讨论目标、规划与委派任务"
              : "一个会话，所有设备共享"}
        </span>
        <span className={styles.spacer} />
        {
          <nav aria-label="Agent 切换" className={styles.agentNav}>
            {conversation?.id !== "main" && conversation?.id && (
              <button onClick={() => selectConversation?.("main")}>主 Agent</button>
            )}
            <button aria-pressed={!!conversation?.workItemID} onClick={openAgentDrawer}>
              会话 ▾
            </button>
            {conversation?.workItemID && (
              <button onClick={() => openIssue(conversation.workItemID!)}>Issue ↗</button>
            )}
          </nav>
        }
        <Button variant="secondary" size="s" onClick={() => setSourceOpen(true)}>
          来源
        </Button>
        <Rollout />
        <Button
          type="button"
          variant="secondary"
          size="s"
          onClick={toggleTheme}
          aria-label={`切换主题：${themeLabels[choice]}`}
        >
          {themeLabels[choice]}
        </Button>
        <span className={styles.mobileOnly}>
          <ConnectionBadge connection={connection} compact />
        </span>
        <Button
          type="button"
          variant="ghost"
          icon
          onClick={toLatest}
          title="跳到最新"
          aria-label="跳到最新"
        >
          <ArrowDown />
        </Button>
      </PageHeader>
      {conversation?.kind === "consultation" && (
        <div className={styles.roleNote}>
          独立咨询 · 普通问答不创建任务
          {conversation.agentRef && <span>咨询Agent：{conversation.agentRef}</span>}
        </div>
      )}
      {conversation?.workItemID && (
        <div className={styles.roleNote}>
          Issue负责人 · 澄清目标、规划与委派
          {conversation.agentRef && <span>负责人Agent：{conversation.agentRef}</span>}
        </div>
      )}
      {sourceOpen && (
        <Sheet open title="补充来源" onClose={() => setSourceOpen(false)}>
          <ManualSource onBack={() => setSourceOpen(false)} />
        </Sheet>
      )}
      <UpgradeBanner connection={connection} />
      <div className={styles.body}>
        <Transcript jump={jump} visible={visible} />
        <div className={styles.seat}>
          <div className={styles.seatInner}>
            <Note />
            <div className={styles.seatScroll}>
              {conversation?.id === "main" && mainAttention?.value && onAttention && onIssue && (
                <MainPending summary={mainAttention} onOpen={onAttention} onIssue={onIssue} />
              )}
              {!conversation?.workItemID && !mainAttention?.value && <DecisionDock />}
              <PendingQueue connection={connection} />
            </div>
            <Composer connection={connection} />
          </div>
        </div>
      </div>
    </>
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
