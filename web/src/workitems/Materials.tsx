import { CopyIdentifier } from "../i18n/identifiers.tsx";
import { FilePreview } from "../tasks/FilePreview.tsx";
import { artifactFiles } from "../tasks/files.ts";
import styles from "./Materials.module.css";

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
              <details>
                <summary>来源消息详细信息</summary>
                <CopyIdentifier value={file.source_message_id} />
              </details>
            )}
            {file.source_task_id && (
              <details>
                <summary>来源任务详细信息</summary>
                <CopyIdentifier value={file.source_task_id} />
              </details>
            )}
          </div>
        ))}
      </div>
    </section>
  ) : null;
}
