import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import type {
  AgentConversation,
  AgentConversationPage,
  SubmitMessagePayload,
} from "../api/generated/api.ts";
import { ServicesContext, useServices } from "../app/services.tsx";
import { notify } from "../app/notice.ts";
import { LiveData } from "../live/data.ts";
import { SendQueue } from "../queue/sendQueue.ts";
import { openOutbox } from "../queue/outbox.ts";
import { ComposerDraft } from "./draft.ts";
import { Chat } from "../shell/Chat.tsx";
import styles from "./Agents.module.css";

export function AgentWorkspace({
  connection,
  visible,
}: {
  connection: Connection;
  visible: boolean;
}) {
  const services = useServices();
  const [selected, setSelected] = useState("main");
  const selection = useRef({ revision: 0 });
  const listRevision = useRef(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [visited, setVisited] = useState<AgentConversation[]>([]);
  const [open, setOpen] = useState(false);
  const drawer = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (open) drawer.current?.showModal();
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
          `/api/v1/agent-conversations?search=${encodeURIComponent(search)}&status=${status}`,
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
  const select = useCallback(
    (id: string) => {
      const request = ++selection.current.revision;
      if (id === "main") {
        setSelected(id);
        setOpen(false);
        return;
      }
      void services.client
        .get<AgentConversation>(`/api/v1/agent-conversations/${encodeURIComponent(id)}`)
        .then((c) => {
          if (request !== selection.current.revision) return;
          setVisited((v) =>
            v.some((x) => x.conversation_id === id)
              ? v.map((x) => (x.conversation_id === id ? c : x))
              : [...v, c],
          );
          setSelected(id);
          setOpen(false);
        })
        .catch((e) => {
          if (request === selection.current.revision) notify(String(e), "warn");
        });
    },
    [services.client],
  );
  useEffect(() => {
    const requests = selection.current;
    const listener = (event: Event) => select((event as CustomEvent<string>).detail);
    const main = () => select("main");
    window.addEventListener("tend:open-agent", listener);
    window.addEventListener("tend:main-agent", main);
    return () => {
      requests.revision++;
      window.removeEventListener("tend:open-agent", listener);
      window.removeEventListener("tend:main-agent", main);
    };
  }, [select]);
  useEffect(() => {
    if (connection.status !== "online") return;
    let active = true;
    void openOutbox()
      .then(async (outbox) => {
        const ids = [
          ...new Set(
            (await outbox.all())
              .filter((m) => m.state !== "sent")
              .map((m) => (m.envelope.payload as SubmitMessagePayload).conversation_id ?? "main"),
          ),
        ].filter((id) => id !== "main");
        for (const id of ids) {
          if (!active) break;
          try {
            const c = await services.client.get<AgentConversation>(
              `/api/v1/agent-conversations/${encodeURIComponent(id)}`,
            );
            if (active)
              setVisited((old) => (old.some((x) => x.conversation_id === id) ? old : [...old, c]));
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
  }, [services.client, connection.status]);
  const shared = {
    ...services,
    agentsEnabled: page?.enabled ?? false,
    selectConversation: select,
    openAgentDrawer: () => setOpen(true),
  };
  const more = async () => {
    if (!page?.next || loadingMore) return;
    const revision = listRevision.current;
    setLoadingMore(true);
    try {
      const next = await services.client.get<AgentConversationPage>(
        `/api/v1/agent-conversations?search=${encodeURIComponent(search)}&status=${status}&after=${encodeURIComponent(page.next)}`,
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
      <ServicesContext value={{ ...shared, conversation: { id: "main", title: "主 Agent" } }}>
        <section className={styles.session} hidden={selected !== "main"}>
          <Chat connection={connection} visible={visible && selected === "main"} />
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
          aria-label="后台 Agent 会话"
          onCancel={() => setOpen(false)}
        >
          <header>
            <div>
              <h2>后台 Agent</h2>
              <p>按工作项查看进展与处理过程</p>
            </div>
            <button onClick={() => setOpen(false)} aria-label="关闭会话抽屉">
              ✕
            </button>
          </header>
          <input
            autoFocus
            aria-label="搜索工作项会话"
            placeholder="搜索工作项…"
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
                <span>{c.preview || "尚无消息"}</span>
                <small data-status={c.status}>{statusLabels[c.status] ?? c.status}</small>
              </button>
            ))}
            {page && !page.items?.length && <p>暂无匹配会话。工作项启用后台处理后会出现在这里。</p>}
            {page?.next && (
              <button disabled={loadingMore} onClick={() => void more()}>
                加载更多
              </button>
            )}
          </div>
        </dialog>
      )}
    </>
  );
}
const statusLabels: Record<string, string> = {
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
  const parent = useServices();
  const id = c.conversation_id;
  const live = useMemo(
    () =>
      new LiveData(
        {
          get: <T,>(path: string, signal?: AbortSignal) =>
            parent.client.get<T>(
              path.startsWith("/api/v1/conversation")
                ? `${path}${path.includes("?") ? "&" : "?"}conversation_id=${encodeURIComponent(id)}`
                : path,
              signal,
            ),
        },
        id,
      ),
    [parent.client, id],
  );
  const draft = useMemo(() => new ComposerDraft(id), [id]);
  const [queue, setQueue] = useState<SendQueue | null>(null);
  const cleanup = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    clearTimeout(cleanup.current);
    let active = true;
    void openOutbox()
      .then((outbox) => SendQueue.open({ outbox, sender: parent.client, conversationID: id }))
      .then((q) => {
        if (active) {
          setQueue(q);
          void q.resume();
        }
      })
      .catch((e) => notify(String(e), "warn"));
    return () => {
      active = false;
      cleanup.current = setTimeout(() => void live.stop(), 0);
    };
  }, [id, live, parent.client]);
  useEffect(() => {
    if (connection.status !== "online") return;
    void queue?.resume();
    if (!visible) return;
    void live.reload();
    const timer = setInterval(() => void live.reload(), 3000);
    return () => clearInterval(timer);
  }, [live, visible, connection.status, queue]);
  return (
    <ServicesContext
      value={{
        ...parent,
        live,
        draft,
        queue,
        conversation: { id, title: c.title, workItemID: c.work_item_id ?? "" },
      }}
    >
      <section className={styles.session} hidden={!visible}>
        <Chat connection={connection} visible={visible} />
      </section>
    </ServicesContext>
  );
}
