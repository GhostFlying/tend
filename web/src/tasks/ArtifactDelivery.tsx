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
      {report && (
        <section
          className={compact ? `${styles.report} ${styles.compactReport}` : styles.report}
          aria-label="交付报告"
        >
          <h3>{compact ? "报告摘要" : "报告正文"}</h3>
          {typeof report.body === "string" && (
            <Markdown text={report.body} files={files} demoteHeadings={compact} />
          )}
          {Array.isArray(report.uncertainties) && report.uncertainties.length > 0 && (
            <>
              <h4>仍未知的部分</h4>
              <ReportNotes notes={report.uncertainties} />
            </>
          )}
        </section>
      )}
      {((showFiles && files.length > 0) || !!report?.evidence?.length) && (
        <details className={styles.section}>
          <summary>原件与证据{files.length > 0 ? `（${files.length} 个文件）` : ""}</summary>
          {showFiles && files.length > 0 && (
            <section className={compact ? styles.compactFiles : undefined} aria-label="交付文件">
              <ul className={styles.files}>
                {files.map((file) => (
                  <li key={file.file_id}>
                    <FilePreview file={file} relatedFiles={files} row={compact} />
                    <details>
                      <summary>文件校验</summary>
                      <code>SHA-256 {file.sha256}</code>
                    </details>
                  </li>
                ))}
              </ul>
            </section>
          )}
          {Array.isArray(report?.evidence) && report.evidence.length > 0 && (
            <ReportNotes notes={report.evidence} />
          )}
        </details>
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
