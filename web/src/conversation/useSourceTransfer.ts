// Source submission uses the existing outbox and upload receipts, separately
// selected from chat. A remount reconciles the original request before transport.
import { useEffect, useMemo, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import type { ManualSourceMetadata } from "../api/generated/api.ts";
import { SendQueue, noOutbox } from "../queue/sendQueue.ts";
import { openOutbox } from "../queue/outbox.ts";
import { useStore } from "../state/store.ts";
import { ComposerDraft } from "./draft.ts";

export function useSourceTransfer(scope: string, conversationID = "main", keepCompleted = false) {
  const { client } = useServices();
  const draft = useMemo(() => new ComposerDraft(`source:${scope}`), [scope]);
  const content = useStore(draft.state);
  const [queue, setQueue] = useState<SendQueue | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const activeRequest = useRef("");
  const records = useStore(queue?.list ?? noOutbox);
  const current = records.find((r) => r.requestId === activeRequest.current);
  useEffect(() => {
    let active = true;
    activeRequest.current = "";
    void draft.open();
    void openOutbox()
      .then((outbox) =>
        SendQueue.open({
          outbox,
          sender: client,
          conversationID,
          operation: "conversation.inject_source",
          scope,
        }),
      )
      .then(async (q) => {
        if (!active) return;
        const existing = q.list
          .get()
          .filter((r) => r.state !== "cancelled" && (r.state !== "sent" || keepCompleted))
          .at(-1);
        activeRequest.current = existing?.requestId ?? "";
        setQueue(q);
        await q.resume();
      })
      .catch(() => {
        if (active) setError("无法打开来源提交记录；尚未发出新请求。");
      });
    return () => {
      active = false;
      void draft.close();
    };
  }, [client, scope, conversationID, draft, keepCompleted]);
  const submit = async (metadata: ManualSourceMetadata = {}) => {
    if (!queue) throw new Error("来源存储尚未就绪");
    setBusy(true);
    setError("");
    try {
      const original = queue.list.get().find((r) => r.requestId === activeRequest.current);
      if (original?.state === "unknown") await queue.reconcile(original.requestId);
      else if (!original) {
        await draft.flush();
        const value = draft.state.get();
        const record = await queue.enqueueSource(
          { body: value.text, files: value.files, attachments: value.attachments },
          metadata,
        );
        activeRequest.current = record.requestId;
        draft.clear();
        await draft.flush();
      }
      await queue.idle();
      return queue.list.get().find((r) => r.requestId === activeRequest.current);
    } finally {
      setBusy(false);
    }
  };
  const editRefused = async () => {
    if (!queue || current?.state !== "failed") return;
    const uploaded = (current.uploads ?? [])
      .filter((file) => file.receipt)
      .map((file) => file.receipt!);
    draft.state.set({
      ...draft.state.get(),
      text: current.body,
      files: (current.uploads ?? [])
        .filter((file) => !file.receipt)
        .map((file) => new File([file.blob], file.name, { type: file.blob.type })),
      attachments: [...(current.references ?? []), ...uploaded],
    });
    await draft.flush();
    await queue.discard(current.requestId);
    activeRequest.current = "";
  };
  return { draft, content, current, queue, busy, error, submit, editRefused };
}
