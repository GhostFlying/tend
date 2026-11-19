import { issueAssociationCommand } from "./issueMetadata.ts";
import { useEffect, useRef, useState } from "react";
import type {
  IssueConversationItem as ConversationItem,
  IssueMessageAssociations as MessageAssociations,
  IssuePage as WorkItemPage,
  SetIssueMessageAssociationsPayload,
  MessageProcessingState,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Link } from "../app/router.ts";
import { useDurableOp } from "../app/useDurableOp.ts";
import { Tag, ExternalLink, More } from "../shell/icons.tsx";
import { Menu } from "../ui/Menu.tsx";
import styles from "./Agents.module.css";

export function MessageTags({
  item,
  currentIssueId,
  processing: groupProcessing,
}: {
  item: ConversationItem;
  currentIssueId?: string | undefined;
  processing?: MessageProcessingState | null | undefined;
}) {
  const { client, live, openIssue, selectConversation } = useServices();
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
  const [unassigned, setUnassigned] = useState(false);
  const [contentRevision, setContentRevision] = useState(0);
  const [editRevision, setEditRevision] = useState<number | null>(null);
  const [error, setError] = useState("");
  const operation = useDurableOp(client, `associations:${item.message_id}`);
  const original =
    operation.original?.operation === "conversation.set_issue_associations"
      ? (operation.original.payload as SetIssueMessageAssociationsPayload)
      : undefined;
  const visibleSelection = original?.issue_ids ?? selection;
  const visibleUnassigned = original ? original.disposition === "unassigned" : unassigned;
  const recovery = useDurableOp(client, `processing:${item.message_id}`);
  const dialog = useRef<HTMLDialogElement>(null);
  const paging = useRef<AbortController | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  useEffect(() => {
    if (!open) return;
    dialog.current?.showModal();
    const abort = new AbortController();
    void Promise.all([
      client.get<MessageAssociations>(
        `/api/v1/messages/${encodeURIComponent(item.message_id)}/associations?projection=issues`,
        abort.signal,
      ),
      client.get<WorkItemPage>("/api/v1/issues?include_closed=true", abort.signal),
    ])
      .then(([links, list]) => {
        setValue(links);
        setEditRevision(links.revision);
        setContentRevision(links.content_revision);
        setUnassigned(links.disposition === "unassigned");
        setSelection((links.items ?? []).map((x) => x.issue_id));
        setItems(list);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => {
      abort.abort();
      paging.current?.abort();
    };
  }, [open, client, item.message_id]);
  const save = async (reconcile = false) => {
    if (!reconcile && editRevision === null) return;
    try {
      const result = await operation.execute(
        reconcile
          ? undefined
          : issueAssociationCommand(
              {
                message_id: item.message_id,
                revision: editRevision!,
                content_revision: contentRevision,
              },
              unassigned ? "unassigned" : "linked",
              selection,
            ),
      );
      if (result.kind !== "done") {
        setError(result.message);
        if (result.kind === "stale") {
          const current = await client.get<MessageAssociations>(
            `/api/v1/messages/${encodeURIComponent(item.message_id)}/associations?projection=issues`,
          );
          setValue(current);
          setEditRevision(current.revision);
          setContentRevision(current.content_revision);
          setUnassigned(current.disposition === "unassigned");
          setSelection((current.items ?? []).map((x) => x.issue_id));
        }
        return;
      }
      setValue(
        await client.get<MessageAssociations>(
          `/api/v1/messages/${encodeURIComponent(item.message_id)}/associations?projection=issues`,
        ),
      );
      setOpen(false);
      void live.reload();
    } catch (e) {
      setError(String(e));
    }
  };
  const links = (value?.items ?? []).filter((link) => link.issue_id !== currentIssueId);
  const processing = groupProcessing === undefined ? value?.processing : groupProcessing;
  const editAssociations = () => {
    setError("");
    setEditRevision(null);
    setOpen(true);
  };
  return (
    <>
      <div className={styles.tags} data-issue-context={!!currentIssueId}>
        {processing && processing.status !== "completed" && processing.status !== "processing" && (
          <span className={styles.tag} title={processing.reason}>
            {(
              {
                recovering: "正在恢复处理进度",
                stopped: "处理已停止",
                needs_review: "需要继续处理",
                unknown: "正在核对执行结果",
                awaiting_clarification: "等待澄清",
              } as Record<string, string>
            )[processing.status] ?? "关联处理中"}
            {["stopped", "needs_review", "awaiting_clarification"].includes(processing.status) && (
              <button
                disabled={recovery.busy || !!recovery.storageError}
                onClick={() => {
                  void recovery
                    .execute(
                      recovery.pending
                        ? undefined
                        : {
                            operation: "conversation.resume_processing",
                            target: {
                              type: "logical_turn",
                              id: processing.logical_turn_id,
                            },
                            expected_control_revision: processing.revision,
                            payload: {},
                          },
                    )
                    .then((r) => {
                      if (r.kind !== "done") setError(r.message);
                      void live.reload();
                    });
                }}
              >
                {recovery.pending ? "核对继续处理" : "继续处理"}
              </button>
            )}
          </span>
        )}
        {!currentIssueId && (
          <>
            {value?.disposition === "unassigned" && <span className={styles.tag}>无 Issue</span>}
            {(!value || value.disposition === "pending") &&
              (item.author === "owner" || !!processing) && (
                <span className={styles.tag}>关联处理中</span>
              )}
            {value?.disposition === "linked" && !value.items?.length && (
              <span className={styles.tag}>历史分类</span>
            )}
          </>
        )}
        {(expanded ? links : links.slice(0, 2)).map((link) => {
          const title = `${link.title} · ${link.source === "owner" ? "用户关联" : "Agent / 明确来源关联"}`;
          const content = (
            <>
              <Tag />
              <span>{link.title}</span>
            </>
          );
          return currentIssueId ? (
            <Link
              key={link.issue_id}
              className={styles.tag}
              title={title}
              to={{ page: "issue", id: link.issue_id }}
            >
              {content}
            </Link>
          ) : (
            <button
              key={link.issue_id}
              className={styles.tag}
              title={title}
              onClick={() => openIssue(link.issue_id)}
            >
              {content}
            </button>
          );
        })}
        {links.length > 2 && (
          <button className={styles.tag} onClick={() => setExpanded(!expanded)}>
            {expanded ? "收起" : `+${links.length - 2}`}
          </button>
        )}
        {currentIssueId ? (
          <Menu
            label="消息操作"
            align="end"
            className={styles.issueMenu}
            variant="ghost"
            size="s"
            icon
            items={[{ id: "associate", label: "修改 Issue 关联", onSelect: editAssociations }]}
          >
            <More />
          </Menu>
        ) : (
          <button
            className={styles.menu}
            title="修改 Issue 关联"
            aria-label="修改 Issue 关联"
            onClick={editAssociations}
          >
            <Tag /> 关联 Issue
          </button>
        )}
        {item.return_link && (
          <button
            className={styles.returnLink}
            onClick={() => selectConversation?.(item.return_link!.conversation_id)}
          >
            查看后台处理过程 <ExternalLink />
          </button>
        )}
      </div>
      {recovery.storageError && <p role="alert">{recovery.storageError}</p>}
      {error && !open && <p role="alert">{error}</p>}
      {open && (
        <dialog
          ref={dialog}
          className={styles.editor}
          onCancel={() => setOpen(false)}
          onClose={() => setOpen(false)}
          aria-label="修改 Issue 关联"
        >
          <h3>关联 Issue</h3>
          <p>
            选择这条消息属于哪些 Issue，便于在对应 Issue 中查阅上下文和原始资料。修改会通知相关
            Issue 核对变化。
          </p>
          <p>
            你保存的归属会锁定，Agent
            不能覆盖；可在此再次修改。关联不会转交负责人、授予执行权限或标记完成，也不会把排队或已撤回的消息变成新指令。
          </p>
          <input
            autoFocus
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索 Issue"
            aria-label="搜索 Issue"
          />
          <label>
            <input
              type="checkbox"
              disabled={operation.busy || operation.pending}
              checked={visibleUnassigned}
              onChange={(e) => {
                setUnassigned(e.target.checked);
                if (e.target.checked) setSelection([]);
              }}
            />
            明确不属于任何 Issue
          </label>
          {items?.items
            ?.filter((x) => x.title.toLocaleLowerCase().includes(search.toLocaleLowerCase()))
            .map((x) => (
              <label key={x.issue_id}>
                <input
                  type="checkbox"
                  disabled={operation.busy || operation.pending}
                  checked={visibleSelection.includes(x.issue_id)}
                  onChange={(e) => {
                    setUnassigned(false);
                    setSelection((old) =>
                      e.target.checked
                        ? [...old, x.issue_id]
                        : old.filter((id) => id !== x.issue_id),
                    );
                  }}
                />
                <span>
                  {x.title}
                  {x.state === "closed" ? " · 已结束" : ""}
                </span>
              </label>
            ))}
          {items?.more && (
            <button
              disabled={operation.busy || operation.pending || loadingMore}
              onClick={() => {
                const abort = new AbortController();
                paging.current?.abort();
                paging.current = abort;
                setLoadingMore(true);
                void client
                  .get<WorkItemPage>(
                    `/api/v1/issues?include_closed=true&after=${encodeURIComponent(items.next ?? "")}`,
                    abort.signal,
                  )
                  .then((next) =>
                    setItems({ ...next, items: [...(items.items ?? []), ...(next.items ?? [])] }),
                  )
                  .catch((e) => {
                    if (!abort.signal.aborted) setError(String(e));
                  })
                  .finally(() => {
                    if (!abort.signal.aborted) setLoadingMore(false);
                  });
              }}
            >
              继续读取 Issue
            </button>
          )}
          {error && <p role="alert">{error}</p>}
          {operation.storageError && <p role="alert">{operation.storageError}</p>}
          {operation.pending && (
            <p>有尚未核对的修改。核对回执后再编辑；未找到回执时仅重试原请求。</p>
          )}
          <footer>
            <button onClick={() => setOpen(false)}>取消</button>
            <button
              disabled={
                operation.busy ||
                !items ||
                editRevision === null ||
                !!operation.storageError ||
                (!operation.pending && !unassigned && selection.length === 0)
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
