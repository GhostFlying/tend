import { createContext, useContext, useEffect, useMemo, type ReactNode } from "react";
import type { ApiClient, Connection } from "../api/client.ts";
import type { IssueAgentConversation } from "../api/generated/api.ts";
import { ServicesContext, useServices } from "../app/services.tsx";
import { LiveData } from "../live/data.ts";
import { openOutbox } from "../queue/outbox.ts";
import { SendQueue } from "../queue/sendQueue.ts";
import { Store } from "../state/store.ts";
import { useStore } from "../state/store.ts";
import { ComposerDraft } from "./draft.ts";

class ConversationSession {
  readonly live: LiveData;
  readonly draft: ComposerDraft;
  readonly queue = new Store<SendQueue | null>(null);
  private opening: Promise<void> | undefined;
  private views = 0;
  private version = 0;
  private closing = Promise.resolve();
  private pauseTimer: ReturnType<typeof setTimeout> | undefined;
  get active(): boolean {
    return this.views > 0;
  }
  acquire(): () => void {
    clearTimeout(this.pauseTimer);
    if (this.views++ === 0) {
      const version = ++this.version;
      void this.start();
      void this.closing.then(() => {
        if (version === this.version && this.active) this.live.resume();
      });
    }
    return () => {
      if (--this.views === 0) this.pauseIfIdle();
    };
  }
  pauseIfIdle(): void {
    if (this.active) return;
    clearTimeout(this.pauseTimer);
    const version = ++this.version;
    this.pauseTimer = setTimeout(() => {
      if (version === this.version && !this.active) this.closing = this.live.stop();
    }, 0);
  }
  constructor(
    private client: ApiClient,
    readonly id: string,
  ) {
    this.live = new LiveData(client, id);
    this.draft = new ComposerDraft(id);
  }
  start(): Promise<void> {
    return (this.opening ??= (async () => {
      await this.draft.open();
      try {
        const queue = await SendQueue.open({
          outbox: await openOutbox(),
          sender: this.client,
          conversationID: this.id,
        });
        this.queue.set(queue);
        await queue.resume();
      } catch {
        /* Composer explains unavailable durable storage. */
      }
    })());
  }
  async close(): Promise<void> {
    clearTimeout(this.pauseTimer);
    ++this.version;
    await this.opening;
    await this.closing;
    await Promise.all([this.live.stop(), this.draft.close()]);
  }
}

/** One registry per app instance, never a global current-conversation identity. */
export class ConversationSessions {
  private sessions = new Map<string, ConversationSession>();
  constructor(private client: ApiClient) {}
  get(id: string): ConversationSession {
    let session = this.sessions.get(id);
    if (!session) {
      session = new ConversationSession(this.client, id);
      this.sessions.set(id, session);
    }
    return session;
  }
  connect(root: LiveData): () => void {
    const stop = root.subscribeEvents((event) => {
      for (const session of this.sessions.values()) if (session.active) session.live.apply(event);
    });
    const refresh = root.subscribeReload(() => {
      for (const session of this.sessions.values()) {
        if (session.active) void session.live.reload();
        void session.queue.get()?.resume();
      }
    });
    return () => {
      stop();
      refresh();
    };
  }
  async close(): Promise<void> {
    await Promise.all([...this.sessions.values()].map((s) => s.close()));
  }
}

export const ConversationSessionsContext = createContext<ConversationSessions | null>(null);

export function ConversationSessionProvider({
  conversation,
  children,
  visible = true,
}: {
  conversation: IssueAgentConversation;
  connection: Connection;
  children: ReactNode;
  visible?: boolean;
}) {
  const parent = useServices();
  const registry = useContext(ConversationSessionsContext);
  if (!registry) throw new Error("ConversationSessionProvider requires the app registry");
  const session = useMemo(
    () => registry.get(conversation.conversation_id),
    [registry, conversation.conversation_id],
  );
  const queue = useStore(session.queue);
  useEffect(() => {
    if (visible) return session.acquire();
    session.pauseIfIdle();
  }, [session, visible]);
  return (
    <ServicesContext
      value={{
        ...parent,
        live: session.live,
        draft: session.draft,
        queue,
        conversation: {
          id: conversation.conversation_id,
          title: conversation.title,
          kind: conversation.kind,
          ...(conversation.issue_id ? { workItemID: conversation.issue_id } : {}),
          ...(conversation.agent_ref ? { agentRef: conversation.agent_ref } : {}),
        },
      }}
    >
      {children}
    </ServicesContext>
  );
}
