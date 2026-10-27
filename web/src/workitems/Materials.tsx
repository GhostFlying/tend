import { useEffect, useMemo, useState } from "react";
import type { TaskDetail, WorkItemView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "./live.ts";
import { uniqueTasks } from "./progress.ts";
import { currentWork } from "../tasks/facts.ts";
import { ArtifactDelivery } from "../tasks/ArtifactDelivery.tsx";
import { FilePreview, FileReference } from "../tasks/FilePreview.tsx";
import { artifactFiles, isImage, sourceFileIds } from "../tasks/files.ts";
import controls from "../shell/controls.module.css";
import styles from "./Materials.module.css";

export function OriginalInputs({ item }: { item: WorkItemView }) {
  const ids = sourceFileIds((item.background ?? []).flatMap((fact) => [fact.source, fact.text]));
  return ids.length ? (
    <section aria-label="原始输入">
      <h3>原始输入</h3>
      <div className={styles.gallery}>
        {ids.map((id) => (
          <FileReference key={id} id={id} />
        ))}
      </div>
    </section>
  ) : null;
}

export function TaskInputs({ spec }: { spec: unknown }) {
  const inputs = artifactFiles(
    spec && typeof spec === "object" && "inputs" in spec ? spec.inputs : undefined,
  );
  return inputs.length ? (
    <section aria-label="任务输入">
      <h3>任务输入原件</h3>
      <div className={styles.gallery}>
        {inputs.map((file) => (
          <div key={file.file_id}>
            <FilePreview
              file={file}
              scope={file.source_task_id ? "files" : "conversation/attachments"}
            />
            {file.source_message_id && (
              <p className={styles.caption}>来源消息：{file.source_message_id}</p>
            )}
            {file.source_task_id && (
              <p className={styles.caption}>来源任务：{file.source_task_id}</p>
            )}
          </div>
        ))}
      </div>
    </section>
  ) : null;
}

export function Materials({ item }: { item: WorkItemView }) {
  const tasks = uniqueTasks(item);
  const [chosen, setChosen] = useState("");
  const task =
    tasks.find((t) => t.task_id === chosen) ??
    tasks
      .filter((t) => t.lifecycle === "completed")
      .toSorted((a, b) => (a.completed_at ?? a.ended_at ?? 0) - (b.completed_at ?? b.ended_at ?? 0))
      .at(-1) ??
    tasks.at(-1);
  return (
    <div className={styles.materials}>
      <section aria-label="调查与交付材料">
        <h3>调查与交付材料</h3>
        {task ? (
          <>
            {tasks.length > 1 && (
              <label className={styles.select}>
                关联任务
                <select value={task.task_id} onChange={(event) => setChosen(event.target.value)}>
                  {tasks.map((t) => (
                    <option key={t.task_id} value={t.task_id}>
                      {t.title}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <TaskMaterials key={task.task_id} id={task.task_id} item={item} />
          </>
        ) : (
          <>
            <OriginalInputs item={item} />
            <p className={styles.caption}>尚未关联调查或执行任务。</p>
          </>
        )}
      </section>
    </div>
  );
}

function TaskMaterials({ id, item }: { id: string; item: WorkItemView }) {
  const { client, live, openTask } = useServices();
  const read = useMemo(() => new WorkItemRead<TaskDetail>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => live.watchMaterials(id, read), [live, read, id]);
  const [expanded, setExpanded] = useState(false);
  const task = state.value;
  const current = task ? currentWork(task) : null;
  const originals = sourceFileIds(
    (item.background ?? []).flatMap((fact) => [fact.source, fact.text]),
  );
  const spec = task?.spec;
  const inputs = artifactFiles(
    spec && typeof spec === "object" && "inputs" in spec ? spec.inputs : undefined,
  ).filter((file) => !originals.includes(file.file_id));
  const previews = (current?.artifacts ?? []).flatMap((artifact) => {
    const files = artifactFiles(
      artifact.ref && typeof artifact.ref === "object" && "files" in artifact.ref
        ? artifact.ref.files
        : undefined,
    );
    const passed =
      current?.verdicts.some(
        (verdict) => verdict.artifact_id === artifact.artifact_id && verdict.verdict === "passed",
      ) ?? false;
    return files
      .filter((file) => isImage(file) || /\.html?$/i.test(file.name))
      .map((file) => ({ file, artifactId: artifact.artifact_id, passed }));
  });
  return (
    <>
      {state.error && (
        <p role="alert">
          材料读取失败：{state.error}{" "}
          <button className={controls.link} onClick={() => void read.refresh()}>
            重试材料
          </button>
        </p>
      )}
      {!task && <OriginalInputs item={item} />}
      {!task && !state.error && <p role="status">正在读取交付材料…</p>}
      {task && current && (
        <>
          <p className={styles.taskHead}>
            <strong>{task.title}</strong>
            <button className={controls.link} onClick={() => openTask(id)}>
              打开任务详情 →
            </button>
          </p>
          <h3>原始输入与调查证据</h3>
          <div className={styles.gallery}>
            {originals.map((fileId) => (
              <div key={fileId}>
                <p className={styles.caption}>原始输入</p>
                <FileReference id={fileId} />
              </div>
            ))}
            {inputs.map((file) => (
              <div key={file.file_id}>
                <p className={styles.caption}>任务输入原件</p>
                <FilePreview
                  file={file}
                  scope={file.source_task_id ? "files" : "conversation/attachments"}
                />
              </div>
            ))}
            {previews.slice(0, expanded ? undefined : 2).map(({ file, artifactId, passed }) => (
              <div key={`${artifactId}:${file.file_id}`}>
                <p className={styles.caption}>
                  任务交付 · spec {task.spec_version} · {passed ? "验收通过" : "未通过验收"}
                </p>
                <FilePreview file={file} />
              </div>
            ))}
          </div>
          {previews.length > 2 && (
            <button className={controls.link} onClick={() => setExpanded(!expanded)}>
              {expanded ? "收起更多材料" : `查看全部交付图片与设计稿（${previews.length}）`}
            </button>
          )}
          {!current.artifacts.length && (
            <p className={styles.caption}>本版暂无已提交产物。阶段完成不代表已实施或部署。</p>
          )}
          {current.artifacts.map((artifact) => {
            const verdict = current.verdicts.find(
              (value) => value.artifact_id === artifact.artifact_id,
            );
            return (
              <section key={artifact.artifact_id} className={styles.delivery}>
                <p className={styles.caption}>
                  spec {task.spec_version} ·{" "}
                  {verdict?.verdict === "passed"
                    ? "独立验收通过"
                    : verdict
                      ? "验收未通过"
                      : "尚未验收"}{" "}
                  · <code>{artifact.artifact_id}</code>
                </p>
                <details>
                  <summary>阅读交付报告全文</summary>
                  <ArtifactDelivery value={artifact.ref} showFiles={false} />
                </details>
                <details>
                  <summary>文件清单与校验</summary>
                  <ArtifactDelivery
                    value={{
                      files:
                        artifact.ref && typeof artifact.ref === "object" && "files" in artifact.ref
                          ? artifact.ref.files
                          : [],
                    }}
                  />
                </details>
              </section>
            );
          })}
        </>
      )}
    </>
  );
}
