import { Button, Checkbox, Select, EmptyState } from "../ui/index.ts";
import { PageHeader } from "../shell/PageHeader.tsx";
import { useEffect, useMemo, useState } from "react";
import type {
  ArchiveOriginal,
  ArchiveSummary,
  IssueMessageAssociations,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { openOutbox, type OutgoingMessage } from "../queue/outbox.ts";
import { OutgoingAttachments } from "../conversation/Attachments.tsx";
import { Archives } from "./data.ts";
import { FileDownload } from "../tasks/FileDownload.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { fileSize } from "../conversation/Attachments.tsx";
import { ImageTile } from "../conversation/Images.tsx";
import { useSourceTransfer } from "../conversation/useSourceTransfer.ts";
import { IssueDefinition } from "../issues/Definition.tsx";
import styles from "./Archive.module.css";
import form from "../agents/Catalog.module.css";

type OriginalKind = "attachment" | "image" | "deliverable";
type HistoricalRecord = { kind: string; id: string; data: Record<string, unknown | undefined> };
const labels: Record<string, string> = {
  work_item: "历史Issue",
  issue_control: "责任记录",
  task: "任务",
  run: "执行",
  artifact: "交付",
  acceptance: "验收",
  decision: "决策",
  message: "消息",
  agent_conversation: "会话",
  run_execution: "执行Agent",
};
const ids: Record<string, string> = {
  work_item: "work_item_id",
  issue_control: "issue_id",
  task: "task_id",
  run: "run_id",
  artifact: "artifact_id",
  acceptance: "acceptance_id",
  decision: "decision_id",
  message: "message_id",
  agent_conversation: "conversation_id",
  run_execution: "run_id",
};

export function ArchiveBrowser({ onIssue }: { onIssue(id: string): void }) {
  const { client } = useServices();
  const archive = useMemo(() => new Archives(client), [client]);
  const catalog = useStore(archive.catalog.state);
  const [selected, setSelected] = useState("");
  useEffect(() => {
    void archive.openCatalog();
    return () => archive.stop();
  }, [archive]);
  const entry =
    catalog.value?.items?.find((a) => a.archive_id === selected) ??
    catalog.value?.items?.find((a) => a.state === "ready") ??
    catalog.value?.items?.[0];
  return (
    <section className={styles.root} aria-label="历史归档">
      <PageHeader className={styles.head} title="历史归档">
        <p>封存的历史记录与原件，只读查看。所选材料可作为新Issue的来源，旧档案保持不变。</p>
      </PageHeader>
      <div className={styles.archiveSelect}>
        <label>
          封存账本
          <Select
            aria-label="选择归档"
            value={entry?.archive_id ?? ""}
            onChange={(e) => setSelected(e.target.value)}
          >
            {(catalog.value?.items ?? [])
              .filter((a) => a.state === "ready")
              .map((a) => (
                <option key={a.archive_id} value={a.archive_id}>
                  {a.archive_id}
                </option>
              ))}
            {(catalog.value?.items ?? []).some((a) => a.state !== "ready") && (
              <optgroup label="其他归档">
                {catalog.value?.items
                  ?.filter((a) => a.state !== "ready")
                  .map((a) => (
                    <option key={a.archive_id} value={a.archive_id}>
                      {a.archive_id} · {a.state === "unsupported" ? "格式暂不支持" : "暂不可读"}
                    </option>
                  ))}
              </optgroup>
            )}
          </Select>
        </label>
        {catalog.error && (
          <p role="status">
            归档目录读取失败。<Button onClick={() => void archive.openCatalog()}>重新读取</Button>
          </p>
        )}
        {!catalog.value && !catalog.error && <p>正在读取归档…</p>}
        {catalog.value?.items?.length === 0 && <EmptyState>尚未配置历史归档。</EmptyState>}
      </div>
      {entry ? (
        <ArchiveContents key={entry.archive_id} archive={archive} entry={entry} onIssue={onIssue} />
      ) : null}
    </section>
  );
}
function ArchiveContents({
  archive,
  entry,
  onIssue,
}: {
  archive: Archives;
  entry: ArchiveSummary;
  onIssue(id: string): void;
}) {
  const records = useStore(archive.records.state),
    originals = useStore(archive.originals.state),
    resources = useStore(archive.resources.state);
  const [kind, setKind] = useState("work_item");
  const [tab, setTab] = useState("records");
  const [selectedRecord, setSelectedRecord] = useState("");
  const [originalKind, setOriginalKind] = useState<OriginalKind>("deliverable");
  const [chosenRecords, setChosenRecords] = useState<HistoricalRecord[]>([]);
  const [chosenOriginals, setChosenOriginals] = useState<ArchiveOriginal[]>([]);
  const [importing, setImporting] = useState(false);
  const [resume, setResume] = useState<OutgoingMessage | null>(null);
  const [transfers, setTransfers] = useState<OutgoingMessage[]>([]);
  useEffect(() => {
    let active = true;
    void openOutbox()
      .then((box) => box.all())
      .then((all) => {
        if (active)
          setTransfers(
            all.filter(
              (record) =>
                record.envelope.operation === "conversation.inject_source" &&
                record.queueScope?.startsWith(`archive:${entry.archive_id}:`) &&
                record.state !== "failed" &&
                record.state !== "cancelled",
            ),
          );
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [entry.archive_id, importing, resume]);
  const kinds = entry.metadata?.record_kinds ?? [];
  const actualKind = kinds.includes(kind) ? kind : kinds[0];
  useEffect(() => {
    if (entry.state !== "ready") return;
    if (actualKind) void archive.openRecords(entry.archive_id, actualKind);
    if (tab === "originals") void archive.openOriginals(entry.archive_id, originalKind);
    if (tab === "resources") void archive.openResources(entry.archive_id);
    return () => {
      archive.records.close();
      archive.originals.close();
      archive.resources.close();
    };
  }, [archive, entry.archive_id, entry.state, actualKind, tab, originalKind]);
  if (entry.state !== "ready")
    return (
      <>
        <h2>{entry.archive_id}</h2>
        <p className={form.warning}>
          {entry.state === "unsupported"
            ? "当前格式暂不支持浏览，原档案仍保留。"
            : "归档暂时不可读取，不能据此推断历史记录为空。"}
        </p>
        {entry.reason && <p>{entry.reason}</p>}
      </>
    );
  if (importing || resume)
    return (
      <ArchiveImport
        archive={archive}
        archiveID={entry.archive_id}
        records={chosenRecords}
        originals={chosenOriginals}
        {...(resume ? { resume } : {})}
        onBack={() => {
          setImporting(false);
          setResume(null);
        }}
        onIssue={onIssue}
      />
    );
  const count = chosenRecords.length + chosenOriginals.length;
  return (
    <>
      <h2>
        {entry.archive_id} <small className={form.muted}>只读</small>
      </h2>
      <p className={form.muted}>
        {entry.metadata?.created_at
          ? `封存于${new Date(entry.metadata.created_at).toLocaleString("zh-CN")}`
          : "封存时间未记录"}
        {entry.metadata?.source_build ? ` · 来源版本${entry.metadata.source_build}` : ""}
      </p>
      {transfers.length > 0 && (
        <section className={styles.record} aria-label="已保存的材料提交">
          <h3>材料提交记录</h3>
          {transfers.map((record) => (
            <p key={record.requestId}>
              <Button className={form.button} onClick={() => setResume(record)}>
                {record.state === "sent" ? "继续使用已保存材料新建Issue" : "核实未完成的材料提交"}
              </Button>{" "}
              <small className={form.muted}>
                {new Date(record.createdAt).toLocaleString("zh-CN")}
              </small>
            </p>
          ))}
        </section>
      )}
      <div className={styles.layout}>
        <aside className={styles.list} aria-label="历史记录导航">
          <label>
            记录类型
            <Select
              aria-label="记录类型"
              value={actualKind ?? ""}
              onChange={(e) => {
                setKind(e.target.value);
                setSelectedRecord("");
                setTab("records");
              }}
            >
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {labels[k] ?? k}
                </option>
              ))}
            </Select>
          </label>
          {records.error ? (
            <p>
              历史目录读取失败。
              <Button onClick={() => void archive.records.refresh()}>重新读取</Button>
            </p>
          ) : !records.value ? (
            <p>正在读取…</p>
          ) : (
            <>
              {(records.value.items ?? []).map((row, i) => {
                const id = String(row[ids[actualKind ?? ""] ?? ""] ?? "");
                return (
                  <Button
                    key={id || i}
                    aria-pressed={
                      (selectedRecord ||
                        String(records.value?.items?.[0]?.[ids[actualKind ?? ""] ?? ""] ?? "")) ===
                      id
                    }
                    onClick={() => {
                      setSelectedRecord(id);
                      setTab("records");
                    }}
                  >
                    {recordTitle(row) || id || "历史记录"}
                    <small>{labels[actualKind ?? ""] ?? actualKind}</small>
                  </Button>
                );
              })}
              {!records.value.items?.length && <p>此类历史记录为空。</p>}
              {records.value.next_after && (
                <Button
                  onClick={() =>
                    void archive.openRecords(
                      entry.archive_id,
                      actualKind!,
                      records.value?.next_after,
                    )
                  }
                >
                  下一页记录
                </Button>
              )}
            </>
          )}
        </aside>
        <main className={styles.content}>
          <nav className={styles.tabs} aria-label="归档内容">
            {[
              ["records", "历史记录"],
              ["sources", "对话与来源"],
              ["originals", "原文件与报告"],
              ["resources", "原生历史与缺口"],
            ].map(([value, label]) => (
              <Button
                key={value}
                aria-pressed={tab === value}
                onClick={() => {
                  setTab(value!);
                  if (value === "sources" && kinds.includes("message")) {
                    setKind("message");
                    setSelectedRecord("");
                  }
                }}
              >
                {label}
              </Button>
            ))}
          </nav>
          {(tab === "records" || tab === "sources") && (
            <>
              {records.error ? (
                <p className={form.warning}>
                  历史记录读取失败。
                  <Button onClick={() => void archive.records.refresh()}>重新读取</Button>
                </p>
              ) : !records.value ? (
                <p>正在读取…</p>
              ) : (
                <>
                  {(records.value.items ?? [])
                    .filter((row, index) =>
                      selectedRecord
                        ? String(row[ids[actualKind ?? ""] ?? ""] ?? "") === selectedRecord
                        : index === 0,
                    )
                    .map((row, i) => {
                      const id =
                        typeof row[ids[actualKind ?? ""] ?? ""] === "string"
                          ? String(row[ids[actualKind ?? ""]!])
                          : "";
                      const selected = chosenRecords.some(
                        (r) => r.kind === actualKind && r.id === id,
                      );
                      return (
                        <article className={styles.record} key={id || i}>
                          <Checkbox
                            label={<h3>{recordTitle(row) || id || "历史记录"}</h3>}
                            aria-label={`选择记录 ${recordTitle(row) || id}`}
                            disabled={!id}
                            checked={selected}
                            onChange={(e) =>
                              setChosenRecords((old) =>
                                e.target.checked
                                  ? [...old, { kind: actualKind!, id, data: row }]
                                  : old.filter((r) => r.kind !== actualKind || r.id !== id),
                              )
                            }
                          />
                          {typeof row.body === "string" && <Markdown text={row.body} />}
                          <details>
                            <summary>查看原始记录</summary>
                            <pre>{JSON.stringify(row, null, 2)}</pre>
                          </details>
                        </article>
                      );
                    })}
                  {!records.value.items?.length && <p>此类历史记录为空。</p>}
                  {records.value.next_after && (
                    <Button
                      className={form.button}
                      onClick={() =>
                        void archive.openRecords(
                          entry.archive_id,
                          actualKind!,
                          records.value?.next_after,
                        )
                      }
                    >
                      下一页记录
                    </Button>
                  )}
                </>
              )}
            </>
          )}
          {tab === "originals" && (
            <>
              <p className={form.muted}>全部原件 · 当前封存账本收录的文件与报告。</p>
              <label>
                原件类型
                <Select
                  value={originalKind}
                  onChange={(e) => setOriginalKind(e.target.value as OriginalKind)}
                >
                  <option value="deliverable">交付文件与报告</option>
                  <option value="attachment">会话附件</option>
                  <option value="image">历史图片</option>
                </Select>
              </label>
              {originals.error ? (
                <p className={form.warning}>
                  原件目录读取失败。
                  <Button onClick={() => void archive.originals.refresh()}>重新读取</Button>
                </p>
              ) : !originals.value ? (
                <p>正在读取…</p>
              ) : (
                <>
                  {(originals.value.items ?? []).map((file) => (
                    <article className={styles.record} key={file.id}>
                      <Checkbox
                        label={<h3>{file.name}</h3>}
                        aria-label={`选择原件 ${file.name}`}
                        checked={chosenOriginals.some(
                          (f) => f.id === file.id && f.kind === file.kind,
                        )}
                        onChange={(e) =>
                          setChosenOriginals((old) =>
                            e.target.checked
                              ? [...old, file]
                              : old.filter((f) => f.id !== file.id || f.kind !== file.kind),
                          )
                        }
                      />
                      <p className={form.muted}>{fileSize(file.size)} · 原件</p>
                      <ArchiveOriginalView
                        archive={archive}
                        archiveID={entry.archive_id}
                        file={file}
                      />
                    </article>
                  ))}
                  {!originals.value.items?.length && <p>该类原件未收录。</p>}
                  {originals.value.next_after && (
                    <Button
                      className={form.button}
                      onClick={() =>
                        void archive.openOriginals(
                          entry.archive_id,
                          originalKind,
                          originals.value?.next_after,
                        )
                      }
                    >
                      下一页原件
                    </Button>
                  )}
                </>
              )}
            </>
          )}
          {tab === "resources" && (
            <>
              {resources.error ? (
                <p className={form.warning}>
                  资源索引读取失败。
                  <Button onClick={() => void archive.resources.refresh()}>重新读取</Button>
                </p>
              ) : !resources.value ? (
                <p>正在读取…</p>
              ) : (
                <>
                  {(resources.value.items ?? []).map((resource) => (
                    <article className={styles.record} key={resource.id}>
                      <h3>
                        {resource.kind} · {resource.id}
                      </h3>
                      <p>
                        {resource.status === "included"
                          ? "已收录"
                          : resource.status === "missing"
                            ? "未收录"
                            : resource.status}
                        {resource.reason ? ` · ${resource.reason}` : ""}
                      </p>
                      {resource.run_id && <p className={form.muted}>原运行：{resource.run_id}</p>}
                      {resource.files?.map((file) => (
                        <ArchiveDownload
                          key={file.index}
                          name={file.name}
                          getURL={(signal) =>
                            archive.resourceDownload(
                              entry.archive_id,
                              resource.id,
                              file.index,
                              signal,
                            )
                          }
                        />
                      ))}
                    </article>
                  ))}
                  {!resources.value.items?.length && (
                    <p>未收录原生历史或runner journal资源索引，不代表当时没有执行。</p>
                  )}
                  {resources.value.next_after && (
                    <Button
                      className={form.button}
                      onClick={() =>
                        void archive.openResources(entry.archive_id, resources.value?.next_after)
                      }
                    >
                      下一页资源
                    </Button>
                  )}
                </>
              )}
            </>
          )}
          <div className={form.actions}>
            <Button className={form.primary} disabled={!count} onClick={() => setImporting(true)}>
              从所选材料新建Issue{count ? `（${count}）` : ""}
            </Button>
            {count > 0 && (
              <Button
                className={form.button}
                onClick={() => {
                  setChosenRecords([]);
                  setChosenOriginals([]);
                }}
              >
                取消选择
              </Button>
            )}
          </div>
          <p className={form.muted}>原归档保持不变；不会恢复旧任务、旧执行身份或凭据。</p>
        </main>
      </div>
    </>
  );
}
function recordTitle(row: Record<string, unknown | undefined>): string {
  if (typeof row.title === "string") return row.title;
  for (const key of ["plan_json", "plan", "spec_json"]) {
    try {
      const raw = typeof row[key] === "string" ? JSON.parse(row[key]) : row[key];
      if (raw && typeof raw === "object" && typeof raw.title === "string") return raw.title;
    } catch {
      /* The raw historical record remains visible unchanged. */
    }
  }
  return "";
}
function ArchiveOriginalView({
  archive,
  archiveID,
  file,
}: {
  archive: Archives;
  archiveID: string;
  file: ArchiveOriginal;
}) {
  const [url, setURL] = useState("");
  const [error, setError] = useState(false);
  const [showText, setShowText] = useState(false);
  const [text, setText] = useState<string | undefined>();
  const [textError, setTextError] = useState(false);
  useEffect(() => {
    if (!showText) return;
    const abort = new AbortController();
    void archive
      .originalDownload(archiveID, file.kind as OriginalKind, file.id, abort.signal)
      .then(async ({ url }) => {
        const response = await fetch(url, { signal: abort.signal });
        if (!response.ok) throw new Error("read failed");
        const original = await response.text();
        if (!abort.signal.aborted) setText(original);
      })
      .catch(() => {
        if (!abort.signal.aborted) setTextError(true);
      });
    return () => abort.abort();
  }, [archive, archiveID, file.kind, file.id, showText]);
  useEffect(() => {
    if (!file.media_type?.startsWith("image/")) return;
    const control = new AbortController();
    void archive
      .originalDownload(archiveID, file.kind as OriginalKind, file.id, control.signal)
      .then((v) => {
        if (!control.signal.aborted) setURL(v.url);
      })
      .catch(() => {
        if (!control.signal.aborted) setError(true);
      });
    return () => control.abort();
  }, [archive, archiveID, file]);
  return (
    <>
      {url && !error && (
        <ImageTile
          source={url}
          name={file.name}
          previewPath={`/api/v1/archives/${encodeURIComponent(archiveID)}/originals/${file.kind}/${encodeURIComponent(file.id)}/download-url`}
          onError={() => setError(true)}
        />
      )}
      {error && <p>图片预览暂不可用，可重新获取原件。</p>}
      {(file.media_type?.startsWith("text/") || /\.(md|txt|log|json|csv)$/i.test(file.name)) && (
        <>
          <Button
            className={form.button}
            onClick={() => {
              setTextError(false);
              setShowText(!showText);
            }}
            aria-expanded={showText}
          >
            {showText ? "收起原文" : "查看原文"}
          </Button>
          {showText && (
            <section aria-label={`${file.name} 原文`}>
              {textError ? (
                <p>
                  原文读取失败。
                  <Button
                    onClick={() => {
                      setShowText(false);
                      setText(undefined);
                    }}
                  >
                    关闭后重试
                  </Button>
                </p>
              ) : text !== undefined ? (
                <pre>{text}</pre>
              ) : (
                <p>正在读取原文…</p>
              )}
            </section>
          )}
        </>
      )}
      <ArchiveDownload
        name={file.name}
        getURL={(signal) =>
          archive.originalDownload(archiveID, file.kind as OriginalKind, file.id, signal)
        }
      />
    </>
  );
}
function ArchiveDownload({
  name,
  getURL,
}: {
  name: string;
  getURL(signal: AbortSignal): Promise<{ url: string }>;
}) {
  return <FileDownload fileId="" name={name} downloadURL={getURL} />;
}
function ArchiveImport({
  archive,
  archiveID,
  records,
  originals,
  resume,
  onBack,
  onIssue,
}: {
  archive: Archives;
  archiveID: string;
  records: HistoricalRecord[];
  originals: ArchiveOriginal[];
  resume?: OutgoingMessage;
  onBack(): void;
  onIssue(id: string): void;
}) {
  const scope =
    resume?.queueScope ??
    `archive:${archiveID}:${[...records.map((r) => `${r.kind}:${r.id}`), ...originals.map((f) => `${f.kind}:${f.id}`)].sort().join("|")}`;
  const source = useSourceTransfer(scope, "main", true);
  const { client } = useServices();
  const [sourceReference, setSourceReference] = useState<{ id: string; revision: number }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const prepare = async () => {
    setBusy(true);
    setError("");
    try {
      if (!source.current && !source.draft.state.get().text) {
        const files = await Promise.all(
          originals.map(async (original) => {
            const grant = await archive.originalDownload(
              archiveID,
              original.kind as OriginalKind,
              original.id,
            );
            const response = await fetch(grant.url);
            if (!response.ok) throw new Error("download failed");
            return new File([await response.blob()], original.name, {
              type: original.media_type ?? "application/octet-stream",
            });
          }),
        );
        source.draft.write(
          `来自只读归档：${archiveID}\n\n${records.map((r) => `${r.kind} ${r.id}\n${JSON.stringify(r.data, null, 2)}`).join("\n\n")}\n\n${originals.map((f) => `原件 ${f.name} · ${f.kind}/${f.id} · sha256 ${f.sha256}`).join("\n")}`,
        );
        source.draft.addFiles(files);
      }
      const result = await source.submit({
        url: `${location.origin}/api/v1/archives/${encodeURIComponent(archiveID)}`,
      });
      if (result?.state === "sent" && result.messageId) {
        const reference = await client.get<IssueMessageAssociations>(
          `/api/v1/messages/${encodeURIComponent(result.messageId)}/associations`,
        );
        setSourceReference({ id: result.messageId, revision: reference.content_revision });
      } else
        setError(
          result?.state === "failed"
            ? (result.error ?? "来源保存被拒绝。")
            : "来源保存结果待核实，未创建新Issue。",
        );
    } catch {
      setError("材料准备或来源保存未完成，旧档案保持不变。请重试核对。");
    } finally {
      setBusy(false);
    }
  };
  if (sourceReference)
    return (
      <IssueDefinition
        sourceMessageID={sourceReference.id}
        sourceRevision={sourceReference.revision}
        sourceDescription={`来源：历史归档 ${archiveID} · 所选材料：${resume ? resume.uploads?.map((f) => f.name).join("、") || "原提交所选记录（见来源原文）" : [...records.map((r) => recordTitle(r.data) || r.id), ...originals.map((f) => f.name)].join("、")} · 已保存为当前资料 ${sourceReference.id}。旧档案保持只读。`}
        onBack={onBack}
        onSaved={onIssue}
      />
    );
  return (
    <section className={form.form} aria-label="归档材料新建Issue">
      <h2>从所选材料新建Issue</h2>
      <p>来源归档：{archiveID} · 只读</p>
      {resume ? (
        <>
          <p>核实原材料提交，继续使用同一请求和原文件。</p>
          <pre>{resume.body}</pre>
          {!!resume.uploads?.length && <OutgoingAttachments uploads={resume.uploads} />}
        </>
      ) : (
        <p>
          已选择{records.length}份记录、{originals.length}份原件。
        </p>
      )}
      <ul>
        {records.map((r) => (
          <li key={`${r.kind}/${r.id}`}>{recordTitle(r.data) || r.id}</li>
        ))}
        {originals.map((f) => (
          <li key={`${f.kind}/${f.id}`}>{f.name}</li>
        ))}
      </ul>
      <p className={form.muted}>
        所选材料会保存为当前部署的新来源，再填写新Issue的目标与负责人。归档不会恢复可写。
      </p>
      {(error || source.error) && (
        <p className={form.warning} role="status">
          {error || source.error}
        </p>
      )}
      {source.current?.state === "failed" && (
        <Button
          className={form.button}
          disabled={busy || source.busy}
          onClick={() => void source.editRefused()}
        >
          取回材料后重新提交
        </Button>
      )}
      <div className={form.actions}>
        <Button
          className={form.primary}
          disabled={
            busy || source.current?.state === "failed" || !source.queue || !source.content.loaded
          }
          onClick={() => void prepare()}
        >
          {busy ? "正在保存所选材料…" : source.current ? "核实原来源并继续" : "保存所选材料并继续"}
        </Button>
        <Button className={form.button} onClick={onBack}>
          返回归档
        </Button>
      </div>
    </section>
  );
}
