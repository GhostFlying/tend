import type { SendQueue } from "../queue/sendQueue.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import type { SubmitMessagePayload } from "../api/generated/api.ts";
import type { ApiClient } from "../api/client.ts";
import type {
  ConversationQueueEntry,
  ConversationQueuePage,
  DownloadURLResponse,
} from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";
import { Store } from "../state/store.ts";
import { type SavedDraft, type Withdrawal } from "./draftStore.ts";
import type { ComposerDraft } from "./draft.ts";

export class Withdrawals {
  readonly state = new Store<Withdrawal[]>([]);
  private running: Promise<void> | null = null;
  constructor(
    private client: ApiClient,
    private draft: ComposerDraft,
    private changed: () => void,
    private queue: SendQueue | null,
  ) {}
  async start(entry: ConversationQueueEntry, edit: boolean): Promise<void> {
    await this.draft.flush();
    const storage = await this.draft.durableStorage();
    const id = newRequestId(Date.now());
    const w: Withdrawal = {
      id,
      draftId: this.draft.id,
      item: entry.item,
      edit,
      state: "pending",
      envelope: {
        request_id: id,
        operation: "conversation.withdraw_message",
        target: { type: "message", id: entry.item.message_id },
        expected_control_revision: entry.revision,
        payload: {},
      },
    };
    await storage.save("withdrawals", w);
    await this.resume();
  }
  async takeBack(m: OutgoingMessage): Promise<void> {
    if (m.submitAttempted !== false) throw new Error("消息可能已提交，请先核对服务端状态");
    await this.draft.flush();
    const id = newRequestId(Date.now()),
      payload = m.envelope.payload as SubmitMessagePayload;
    const localDraft: SavedDraft = {
      id: this.draft.id,
      text: m.body,
      files: (m.uploads ?? [])
        .filter((u) => !u.receipt)
        .map((u) => new File([u.blob], u.name, { type: u.blob.type })),
      attachments: [
        ...(m.references ?? []),
        ...(m.uploads ?? []).flatMap((u) => (u.receipt ? [u.receipt] : [])),
      ],
      replyTo: payload.reply_to,
      updated: Date.now(),
    };
    for (const image of payload.images ?? []) {
      const bytes = Uint8Array.from(atob(image.data_base64), (ch) => ch.charCodeAt(0));
      localDraft.files.push(new File([bytes], image.name, { type: image.media_type }));
    }
    const item: ConversationQueueEntry["item"] = {
      message_id: m.requestId,
      seq: 0,
      author: "owner",
      kind: "message",
      body: m.body,
      created_at: new Date(m.createdAt).toISOString(),
      ...(payload.decision ? { decision: payload.decision } : {}),
    };
    localDraft.source = item;
    const w: Withdrawal = {
      id,
      draftId: this.draft.id,
      localRequestId: m.requestId,
      localDraft,
      item,
      edit: true,
      state: "pending",
      envelope: m.envelope,
    };
    await (await this.draft.durableStorage()).save("withdrawals", w);
    await this.resume();
  }
  async adopt(id: string): Promise<void> {
    await this.draft.adoptWithdrawal(id);
    await this.resume();
  }
  resume(): Promise<void> {
    if (this.running) return this.running;
    this.running = this.run().finally(() => {
      this.running = null;
    });
    return this.running;
  }
  private async run(): Promise<void> {
    const storage = await this.draft.durableStorage();
    for (let w of await storage.withdrawals()) {
      if (w.state !== "pending" || w.draftId !== this.draft.id) continue;
      try {
        if (w.localRequestId) {
          if (!this.queue) throw new Error("本机发送队列尚未就绪");
          if (!(await this.queue.cancelled(w.localRequestId))) {
            try {
              await this.queue.discard(w.localRequestId);
            } catch {
              const submitted = await this.client.lookup(w.localRequestId);
              const result = submitted?.result as { message_id?: string } | undefined;
              if (
                !result?.message_id ||
                (submitted?.status !== "accepted" && submitted?.status !== "succeeded")
              )
                throw new Error("发送结果尚未确认，保留原请求继续核对");
              let after = 0,
                entry: ConversationQueueEntry | undefined;
              do {
                const page = await this.client.get<ConversationQueuePage>(
                  `/api/v1/conversation/queue?category=owner&after=${after}`,
                );
                entry = page.items?.find((it) => it.item.message_id === result.message_id);
                after = page.next ?? 0;
              } while (!entry && after);
              if (!entry?.can_withdraw) {
                await storage.save("withdrawals", {
                  ...w,
                  state: "failed",
                  error: "消息已被服务端领取，无法取回；请查看当前回复并按需停止",
                });
                this.changed();
                continue;
              }
              const promoted: Withdrawal = {
                ...w,
                item: entry.item,
                error: undefined,
                envelope: {
                  request_id: w.id,
                  operation: "conversation.withdraw_message",
                  target: { type: "message", id: entry.item.message_id },
                  expected_control_revision: entry.revision,
                  payload: {},
                },
              };
              delete promoted.localRequestId;
              delete promoted.localDraft;
              await storage.save("withdrawals", promoted);
              w = promoted;
            }
          }
          if (w.localRequestId) {
            if (!(await this.queue.cancelled(w.localRequestId)))
              throw new Error("未找到本机取消回执，请先核对发送结果");
            await this.draft.recover(w, w.localDraft!);
            this.changed();
            continue;
          }
        }
        const found = await this.client.lookup(w.id);
        const op = found ?? (await this.client.submit(w.envelope)).operation;
        if (!op || op.status === "unknown") throw new Error("撤回结果待核对，原消息可能仍在队列中");
        if (op.status === "failed") {
          await storage.save("withdrawals", {
            ...w,
            state: "failed",
            error: op.error?.message || "消息已开始或版本已变化，请重新读取",
          });
          this.changed();
          continue;
        }
        const recovered: SavedDraft = {
          id: w.draftId,
          text: w.item.body ?? "",
          files: [],
          attachments: w.item.attachments ?? [],
          replyTo: w.item.reply_to,
          source: w.item,
          updated: Date.now(),
        };
        // Legacy images remain referenced by the durable intention until every byte is recovered.
        if (w.edit)
          for (const image of w.item.images ?? []) {
            if (recovered.attachments.some((a) => a.file_id === image.image_id)) continue;
            const response = await this.client.get<DownloadURLResponse>(
              `/api/v1/conversation/images/${encodeURIComponent(image.image_id)}/download-url`,
            );
            const download = await fetch(response.url);
            if (!download.ok) throw new Error("消息已撤回，原图片尚未取回；恢复连接后重试");
            recovered.files.push(
              new File([await download.blob()], image.name, { type: image.media_type }),
            );
          }
        await this.draft.recover(w, recovered);
        this.changed();
      } catch (error) {
        await storage.save("withdrawals", {
          ...w,
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }
    this.state.set(await storage.withdrawals());
  }
}
