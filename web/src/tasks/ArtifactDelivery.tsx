// Readable report and immutable file manifest alongside legacy Git artifacts.
import type { ResultReport } from "../api/generated/api.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { FilePreview } from "./FilePreview.tsx";
import { artifactFiles } from "./files.ts";
import styles from "./Deliverables.module.css";

export function ArtifactDelivery({
  value,
  showFiles = true,
  compact = false,
}: {
  value: unknown;
  showFiles?: boolean;
  compact?: boolean;
}) {
  if (!value || typeof value !== "object") return null;
  const payload = value as Record<string, unknown>;
  const report =
    payload.report && typeof payload.report === "object"
      ? (payload.report as Partial<ResultReport>)
      : null;
  const files = artifactFiles(payload.files);
  return (
    <>
      {showFiles && files.length > 0 && (
        <section
          className={compact ? `${styles.section} ${styles.compactFiles}` : styles.section}
          aria-label="交付文件"
        >
          {!compact && <h3>文件</h3>}
          <ul className={styles.files}>
            {files.map((file) => (
              <li key={file.file_id}>
                <FilePreview file={file} row={compact} />
                {!compact && (
                  <details>
                    <summary>文件校验</summary>
                    <code>SHA-256 {file.sha256}</code>
                  </details>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {report && (
        <section
          className={compact ? `${styles.report} ${styles.compactReport}` : styles.report}
          aria-label="交付报告"
        >
          {!compact && <h3>报告</h3>}
          {typeof report.body === "string" && (
            <Markdown text={report.body} files={files} demoteHeadings={compact} />
          )}
          {Array.isArray(report.evidence) && report.evidence.length > 0 && (
            <>
              <h4>证据</h4>
              <ReportNotes notes={report.evidence} />
            </>
          )}
          {Array.isArray(report.uncertainties) && report.uncertainties.length > 0 && (
            <>
              <h4>不确定项</h4>
              <ReportNotes notes={report.uncertainties} />
            </>
          )}
        </section>
      )}
    </>
  );
}
function ReportNotes({ notes }: { notes: readonly string[] }) {
  return (
    <ul className={styles.notes}>
      {notes
        .filter((note) => typeof note === "string")
        .map((note, index) => (
          <li key={index}>
            <Markdown text={note} />
          </li>
        ))}
    </ul>
  );
}
