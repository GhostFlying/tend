import { PageHeader } from "../shell/PageHeader.tsx";
import { Button } from "../ui/Button.tsx";
import { EmptyState } from "../ui/Surfaces.tsx";
import { rememberOpener } from "../ui/focus.ts";
import { navigate, useRoute } from "../app/router.ts";
import { NewConsultation } from "./NewConsultation.tsx";
import { Close } from "../shell/icons.tsx";
import { useCallback, useContext, useEffect, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import type {
  IssueAgentConversation as AgentConversation,
  IssueAgentConversationPage as AgentConversationPage,
  SubmitMessagePayload,
  AttentionPage,
} from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { ServicesContext, useServices } from "../app/services.tsx";
import { notify } from "../app/notice.ts";
import { ConversationSessionProvider, ConversationSessionsContext } from "./session.tsx";

import { openOutbox } from "../queue/outbox.ts";

import { Chat } from "../shell/Chat.tsx";
import styles from "./Agents.module.css";

export function AgentWorkspace({
  connection,
  visible,
  mainAttention,
  onAttention,
  onIssue,
}: {
  connection: Connection;
  visible: boolean;
  mainAttention: ReadState<AttentionPage>;
  onAttention(id?: string): void;
  onIssue(id: string): void;
}) {
  const services = useServices();
  const sessions = useContext(ConversationSessionsContext);
  const route = useRoute();
  const selected = route.page === "conversation" ? route.id : "main";
  const selection = useRef({ revision: 0 });
  const [unavailable, setUnavailable] = useState("");
  const listRevision = useRef(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [visited, setVisited] = useState<AgentConversation[]>([]);
  const [open, setOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const drawerOpener = useRef<Element | null>(null);
  const drawer = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (open && drawer.current) {
      rememberOpener(drawer.current, drawerOpener.current);
      drawer.current.showModal();
    }
  }, [open]);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [page, setPage] = useState<AgentConversationPage | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    listRevision.current++;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      void services.client
        .get<AgentConversationPage>(
          `/api/v1/agent-conversations?projection=issues&search=${encodeURIComponent(search)}&status=${status}`,
          controller.signal,
        )
        .then(setPage)
        .catch((e) => {
          if (!controller.signal.aborted) setError(String(e));
        });
    }, 150);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [services.client, search, status, open]);
  const select = useCallback((id: string) => {
    selection.current.revision++;
    setOpen(false);
    navigate(id === "main" ? { page: "chat" } : { page: "conversation", id });
  }, []);
  useEffect(() => {
    const requests = selection.current;
    const request = ++requests.revision;
    if (selected !== "main") {
      void services.client
        .get<AgentConversation>(
          `/api/v1/agent-conversations/${encodeURIComponent(selected)}?projection=issues`,
        )
        .then((c) => {
          if (request !== requests.revision) return;
          if (c.issue_id) {
            navigate({ page: "issue", id: c.issue_id }, { replace: true });
            return;
          }
          setVisited((v) =>
            v.some((x) => x.conversation_id === selected)
              ? v.map((x) => (x.conversation_id === selected ? c : x))
              : [...v, c],
          );
        })
        .catch((error) => {
          if (request === requests.revision) {
            setUnavailable(selected);
            notify(String(error), "warn");
          }
        });
    }
    return () => {
      requests.revision++;
    };
  }, [services.client, selected]);
  useEffect(() => {
    if (connection.status !== "online") return;
    let active = true;
    void openOutbox()
      .then(async (outbox) => {
        const ids = [
          ...new Set(
            (await outbox.all())
              .filter(
                (m) => m.state !== "sent" && m.envelope.operation === "conversation.submit_message",
              )
              .map((m) => (m.envelope.payload as SubmitMessagePayload).conversation_id ?? "main"),
          ),
        ].filter((id) => id !== "main");
        for (const id of ids) {
          if (!active) break;
          try {
            const c = await services.client.get<AgentConversation>(
              `/api/v1/agent-conversations/${encodeURIComponent(id)}?projection=issues`,
            );
            if (active) void sessions?.get(c.conversation_id).start();
          } catch (e) {
            if (active) notify(`后台待发送消息仍保留，尚未恢复：${String(e)}`, "warn");
          }
        }
      })
      .catch((e) => {
        if (active) notify(String(e), "warn");
      });
    return () => {
      active = false;
    };
  }, [services.client, connection.status, sessions]);
  const shared = {
    ...services,
    selectConversation: select,
    openAgentDrawer: () => {
      drawerOpener.current = document.activeElement;
      setOpen(true);
    },
  };
  const more = async () => {
    if (!page?.next || loadingMore) return;
    const revision = listRevision.current;
    setLoadingMore(true);
    try {
      const next = await services.client.get<AgentConversationPage>(
        `/api/v1/agent-conversations?projection=issues&search=${encodeURIComponent(search)}&status=${status}&after=${encodeURIComponent(page.next)}`,
      );
      if (revision === listRevision.current)
        setPage({ ...next, items: [...(page.items ?? []), ...(next.items ?? [])] });
    } catch (e) {
      if (revision === listRevision.current) setError(String(e));
    } finally {
      setLoadingMore(false);
    }
  };
  return (
    <>
      {selected !== "main" && !visited.some((c) => c.conversation_id === selected) && (
        <section className={styles.session}>
          <PageHeader title="会话" />
          <EmptyState>
            {unavailable === selected ? "暂时无法读取此会话，请返回后重试。" : "正在读取会话…"}
            <Button variant="link" onClick={() => select("main")}>
              返回主对话
            </Button>
          </EmptyState>
        </section>
      )}
      <ServicesContext value={{ ...shared, conversation: { id: "main", title: "主 Agent" } }}>
        <section className={styles.session} hidden={selected !== "main"}>
          <Chat
            connection={connection}
            visible={visible && selected === "main"}
            mainAttention={mainAttention}
            onAttention={onAttention}
            onIssue={onIssue}
          />
        </section>
      </ServicesContext>
      {visited.map((c) => (
        <ServicesContext key={c.conversation_id} value={shared}>
          <BackgroundConversation
            conversation={c}
            connection={connection}
            visible={visible && selected === c.conversation_id}
          />
        </ServicesContext>
      ))}
      {open && (
        <dialog
          ref={drawer}
          className={styles.drawer}
          aria-label="会话"
          onCancel={() => setOpen(false)}
        >
          {creating ? (
            <NewConsultation
              onBack={() => setCreating(false)}
              onCreated={(id) => {
                setCreating(false);
                select(id);
              }}
            />
          ) : (
            <>
              <header>
                <div>
                  <h2>会话</h2>
                  <p>主助理、Issue负责人会话与独立咨询</p>
                </div>
                <button onClick={() => setOpen(false)} aria-label="关闭会话抽屉">
                  <Close width={18} height={18} />
                </button>
              </header>
              <button
                onClick={() => {
                  setOpen(false);
                  navigate({ page: "agents" });
                }}
              >
                Agent与类别
              </button>
              <button
                onClick={() => {
                  setOpen(false);
                  navigate({ page: "archive" });
                }}
              >
                历史归档
              </button>
              <button className={styles.primary} onClick={() => setCreating(true)}>
                新建咨询
              </button>
              <button onClick={() => select("main")}>主 Agent</button>
              <input
                autoFocus
                aria-label="搜索会话"
                placeholder="搜索会话…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <select
                aria-label="筛选会话状态"
                value={status}
                onChange={(e) => setStatus(e.target.value)}
              >
                <option value="">全部状态</option>
                {Object.entries(statusLabels).map(([v, label]) => (
                  <option key={v} value={v}>
                    {label}
                  </option>
                ))}
              </select>
              {error && <p role="alert">{error}</p>}
              {page && !page.runtime_available && (
                <p role="alert">后台运行时不可用，消息和处理责任仍保留。</p>
              )}
              <div className={styles.sessionList}>
                {page?.items?.map((c) => (
                  <button
                    key={c.conversation_id}
                    onClick={() => select(c.conversation_id)}
                    className={styles.sessionRow}
                  >
                    <strong>{c.title}</strong>
                    <time>
                      {c.last_message_at
                        ? new Date(c.last_message_at).toLocaleTimeString([], {
                            hour: "2-digit",
                            minute: "2-digit",
                          })
                        : ""}
                    </time>
                    <span>
                      {c.kind === "consultation" ? "咨询 · " : "Issue · "}
                      {c.preview || "尚无消息"}
                    </span>
                    <small data-status={c.status}>{statusLabels[c.status] ?? c.status}</small>
                  </button>
                ))}
                {page && !page.items?.length && (
                  <p>暂无匹配会话。可以开始独立咨询，或在Issue详情与负责人对话。</p>
                )}
                {page?.next && (
                  <button disabled={loadingMore} onClick={() => void more()}>
                    加载更多
                  </button>
                )}
              </div>
            </>
          )}
        </dialog>
      )}
    </>
  );
}
const statusLabels: Record<string, string> = {
  initializing: "等待上下文",
  queued: "排队中",
  running: "处理中",
  idle: "空闲",
  attention: "待核对",
  failed: "失败",
  archived: "已结束",
};
function BackgroundConversation({
  conversation: c,
  connection,
  visible,
}: {
  conversation: AgentConversation;
  connection: Connection;
  visible: boolean;
}) {
  if (c.issue_id) return null;
  return (
    <ConversationSessionProvider conversation={c} connection={connection} visible={visible}>
      <section className={styles.session} hidden={!visible}>
        <Chat connection={connection} visible={visible} />
      </section>
    </ConversationSessionProvider>
  );
}
