import { useEffect, useRef, useState } from "react";
import type { ConversationItem, MessageAssociations, WorkItemPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import { Tag, ExternalLink } from "../shell/icons.tsx";
import styles from "./Agents.module.css";

export function MessageTags({ item }: { item: ConversationItem }) {
  const { client, live, openWorkItem, selectConversation } = useServices();
  const [savedValue, setValue] = useState(item.associations);
  const value =
    (item.associations?.revision ?? -1) >= (savedValue?.revision ?? -1)
      ? item.associations
      : savedValue;
  const [open, setOpen] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [search, setSearch] = useState("");
  const [items, setItems] = useState<WorkItemPage | null>(null);
  const [selection, setSelection] = useState<string[]>([]);
  const [editRevision, setEditRevision] = useState<number | null>(null);
  const [error, setError] = useState("");
  const operation = useDurableOp(client, `associations:${item.message_id}`);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (!open) return;
    dialog.current?.showModal();
    const abort = new AbortController();
    void Promise.all([
      client.get<MessageAssociations>(
        `/api/v1/messages/${encodeURIComponent(item.message_id)}/associations`,
        abort.signal,
      ),
      client.get<WorkItemPage>("/api/v1/work-items?include_closed=true", abort.signal),
    ])
      .then(([links, list]) => {
        setValue(links);
        setEditRevision(links.revision);
        setSelection((links.items ?? []).map((x) => x.work_item_id));
        setItems(list);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [open, client, item.message_id]);
  const save = async (reconcile = false) => {
    if (editRevision === null) return;
    try {
      const result = await operation.execute(
        reconcile
          ? undefined
          : {
              operation: "conversation.set_associations",
              target: { type: "message", id: item.message_id },
              expected_control_revision: editRevision,
              payload: { work_item_ids: selection },
            },
      );
      if (result.kind !== "done") {
        setError(result.message);
        if (result.kind === "stale") {
          const current = await client.get<MessageAssociations>(
            `/api/v1/messages/${encodeURIComponent(item.message_id)}/associations`,
          );
          setValue(current);
          setEditRevision(current.revision);
          setSelection((current.items ?? []).map((x) => x.work_item_id));
        }
        return;
      }
      setValue(
        await client.get<MessageAssociations>(
          `/api/v1/messages/${encodeURIComponent(item.message_id)}/associations`,
        ),
      );
      setOpen(false);
      void live.reload();
    } catch (e) {
      setError(String(e));
    }
  };
  const links = value?.items ?? [];
  return (
    <>
      <div className={styles.tags}>
        {(expanded ? links : links.slice(0, 2)).map((link) => (
          <button
            key={link.work_item_id}
            className={styles.tag}
            title={`${link.title} · ${link.source === "owner" ? "用户关联" : "Agent / 明确来源关联"}`}
            onClick={() => openWorkItem(link.work_item_id)}
          >
            <Tag />
            <span>{link.title}</span>
          </button>
        ))}
        {links.length > 2 && (
          <button className={styles.tag} onClick={() => setExpanded(!expanded)}>
            {expanded ? "收起" : `+${links.length - 2}`}
          </button>
        )}
        <button
          className={styles.menu}
          title="修改工作项关联"
          aria-label="修改工作项关联"
          onClick={() => {
            setError("");
            setEditRevision(null);
            setOpen(true);
          }}
        >
          ···
        </button>
        {item.return_link && (
          <button
            className={styles.returnLink}
            onClick={() => selectConversation?.(item.return_link!.conversation_id)}
          >
            查看后台处理过程 <ExternalLink />
          </button>
        )}
      </div>
      {open && (
        <dialog
          ref={dialog}
          className={styles.editor}
          onCancel={() => setOpen(false)}
          onClose={() => setOpen(false)}
          aria-label="修改工作项关联"
        >
          <h3>关联工作项</h3>
          <p>关联用于汇集上下文，不代表授权或完成。</p>
          <input
            autoFocus
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索工作项"
            aria-label="搜索工作项"
          />
          {items?.items
            ?.filter((x) => x.title.toLocaleLowerCase().includes(search.toLocaleLowerCase()))
            .map((x) => (
              <label key={x.work_item_id}>
                <input
                  type="checkbox"
                  checked={selection.includes(x.work_item_id)}
                  onChange={(e) =>
                    setSelection((old) =>
                      e.target.checked
                        ? [...old, x.work_item_id]
                        : old.filter((id) => id !== x.work_item_id),
                    )
                  }
                />
                <span>
                  {x.title}
                  {x.status === "closed" ? " · 已结束" : ""}
                </span>
              </label>
            ))}
          {error && <p role="alert">{error}</p>}
          {operation.storageError && <p role="alert">{operation.storageError}</p>}
          {operation.pending && (
            <p>有尚未核对的修改。核对回执后再编辑；未找到回执时仅重试原请求。</p>
          )}
          <footer>
            <button onClick={() => setOpen(false)}>取消</button>
            <button
              disabled={
                operation.busy || !items || editRevision === null || !!operation.storageError
              }
              onClick={() => void save(operation.pending)}
            >
              {operation.busy ? "处理中…" : operation.pending ? "核对原请求" : "保存关联"}
            </button>
          </footer>
        </dialog>
      )}
    </>
  );
}
