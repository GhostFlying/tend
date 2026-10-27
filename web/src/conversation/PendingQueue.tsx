import { useEffect, useMemo, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { ConversationQueueEntry } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { notify } from "../app/notice.ts";
import { useStore } from "../state/store.ts";
import { noOutbox } from "../queue/sendQueue.ts";
import { MessageAttachments } from "./Attachments.tsx";
import { MessageImages } from "./Images.tsx";
import { OutgoingRow } from "./Messages.tsx";
import { Withdrawals } from "./withdraw.ts";
import type { Category } from "./queueView.ts";
import styles from "./PendingQueue.module.css";

export function PendingQueue({ connection }: { connection: Connection }) {
  const { live, client, queue, draft, conversation } = useServices();
  const state = useStore(live.queueView.state);
  const settings = useStore(live.settings.state).view;
  const handoff = useStore(live.handoff.state).view;
  const local = useStore(queue?.list ?? noOutbox);
  const draftState = useStore(draft.state);
  const withdrawals = useMemo(
    () =>
      new Withdrawals(
        client,
        draft,
        () => {
          void live.queueView.refresh();
        },
        queue,
      ),
    [client, draft, live, queue],
  );
  const requests = useStore(withdrawals.state);
  const [expanded, setExpanded] = useState(false);
  const [pending, setPending] = useState<string | null>(null);
  useEffect(() => {
    live.queueView.start();
    void draft.open();
  }, [live, draft]);
  useEffect(() => {
    if (connection.status !== "online" || !draftState.loaded) return;
    void withdrawals.resume();
    const timer = setInterval(() => void withdrawals.resume(), 10000);
    return () => clearInterval(timer);
  }, [withdrawals, connection.status, draftState.loaded]);
  const act = async (entry: ConversationQueueEntry, edit: boolean) => {
    setPending(entry.item.message_id);
    try {
      await withdrawals.start(entry, edit);
      if (edit) notify("撤回确认后会取回为新草稿；原草稿保存在草稿列表中");
    } catch (error) {
      notify(String(error), "warn");
    } finally {
      setPending(null);
    }
  };
  const more = (category: Category) => {
    void live.queueView.moreQueue(category).catch((error) => notify(String(error), "warn"));
  };
  if (state.supported === false)
    return (
      <div className={styles.note}>
        当前服务端不支持队列管理，使用原聊天视图；撤回和自动跟进不可用。
      </div>
    );
  const owner = state.pages.owner;
  const waiting = owner?.items?.filter((it) => it.status !== "processing") ?? [];
  const background = state.pages.background;
  const withdrawn = state.pages.withdrawn;
  const localPending = local.filter((m) => m.state !== "sent");
  const render = (entry: ConversationQueueEntry, canAct: boolean) => (
    <article key={entry.item.message_id} className={styles.item}>
      <div className={styles.summary}>
        {entry.status === "queued"
          ? "等待处理"
          : entry.status === "starting"
            ? "正在启动"
            : entry.status === "withdrawn"
              ? "已撤回"
              : "正在处理"}{" "}
        · {new Date(entry.item.created_at).toLocaleTimeString()}
      </div>
      <details>
        <summary className={styles.preview}>{entry.item.body || "附件消息"}</summary>
        <p className={styles.body}>{entry.item.body}</p>
        {!!entry.item.attachments?.length && (
          <MessageAttachments attachments={entry.item.attachments} />
        )}{" "}
        {!!entry.item.images?.length && !entry.item.attachments?.length && (
          <MessageImages images={entry.item.images} />
        )}
      </details>
      {canAct && (
        <div className={styles.actions}>
          <button
            disabled={!entry.can_withdraw || pending === entry.item.message_id}
            onClick={() => void act(entry, false)}
          >
            撤回
          </button>
          <button
            disabled={!entry.can_withdraw || pending === entry.item.message_id}
            onClick={() => void act(entry, true)}
          >
            取回编辑
          </button>
          {!entry.can_withdraw && <span>正在启动，开始处理后可停止回复</span>}
        </div>
      )}
    </article>
  );
  return (
    <section className={styles.queue} aria-label="待处理队列">
      {(connection.status !== "online" || state.error) && (
        <p className={styles.note}>
          队列不是实时状态，恢复连接后核对{state.error ? `：${state.error}` : ""}
        </p>
      )}
      {settings?.enabled && !settings.available && (
        <p className={styles.note}>托管主 Agent 暂不可用，消息保留在队列中。</p>
      )}
      {handoff?.pending && (
        <p className={styles.note}>
          {handoff.preparing ? "主会话正在准备摘要" : "主会话正在交接"}；待处理消息会继续保留。
        </p>
      )}
      {owner && !owner.enabled && (
        <p className={styles.note}>托管主 Agent 未启用，消息暂不自动处理。</p>
      )}
      {!!localPending.length && (
        <details open>
          <summary>本机待发送 · {localPending.length}</summary>
          {localPending.map((m) => (
            <div key={m.requestId}>
              <OutgoingRow message={m} queue={queue} />
              {m.submitAttempted === false && (
                <button
                  onClick={() =>
                    void withdrawals.takeBack(m).catch((e) => notify(String(e), "warn"))
                  }
                >
                  取回编辑
                </button>
              )}
            </div>
          ))}
        </details>
      )}
      {!!waiting.length && (
        <>
          <div className={styles.heading}>
            待处理 ·{" "}
            {(owner?.total ?? waiting.length) -
              (owner?.items?.filter((e) => e.status === "processing").length ?? 0)}
          </div>
          {(expanded ? waiting : waiting.slice(0, 3)).map((it) => render(it, true))}
          {waiting.length > 3 && (
            <button onClick={() => setExpanded(!expanded)}>
              {expanded ? "收起" : `展开其余 ${waiting.length - 3} 条`}
            </button>
          )}
          {owner?.next && <button onClick={() => more("owner")}>加载更多待处理消息</button>}
        </>
      )}
      {conversation?.id !== "main" && !!background?.total && (
        <details>
          <summary>
            后台事项 · {background.total}
            {background.items?.[0]
              ? ` · 最早 ${new Date(background.items?.[0].item.created_at).toLocaleString()}`
              : ""}
          </summary>
          {background.items?.map((it) => render(it, false))}
          {background.next && <button onClick={() => more("background")}>加载更多后台事项</button>}
        </details>
      )}
      {!!withdrawn?.total && (
        <details>
          <summary>撤回历史 · {withdrawn.total}</summary>
          {withdrawn.items?.map((it) => render(it, false))}
          {withdrawn.next && <button onClick={() => more("withdrawn")}>加载更多撤回历史</button>}
        </details>
      )}
      {requests
        .filter((w) => w.state === "pending" || (w.state === "failed" && w.draftId === draft.id))
        .map((w) => (
          <p key={w.id} role="status">
            {w.draftId === draft.id
              ? w.error || "正在核对撤回结果…"
              : `有一次尚未恢复的取回：${w.item.body || "附件消息"}`}
            {w.state === "pending" && (
              <button
                onClick={() =>
                  void (
                    w.draftId === draft.id ? withdrawals.resume() : withdrawals.adopt(w.id)
                  ).catch((error) => notify(String(error), "warn"))
                }
              >
                {w.draftId === draft.id ? "继续核对" : "在本页恢复"}
              </button>
            )}
          </p>
        ))}
      {!!draftState.saved.length && (
        <details>
          <summary>暂存草稿 · {draftState.saved.length}</summary>
          {draftState.saved.map((s) => (
            <button
              key={s.id}
              onClick={() => void draft.select(s).catch((e) => notify(String(e), "warn"))}
            >
              {s.text.slice(0, 60) || "附件草稿"} · 恢复
            </button>
          ))}
        </details>
      )}
    </section>
  );
}
