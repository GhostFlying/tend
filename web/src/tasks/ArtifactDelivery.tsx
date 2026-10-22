// Readable report and immutable file manifest alongside legacy Git artifacts.
import type { ArtifactFile, ResultReport } from "../api/generated/api.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { FileDownload } from "./FileDownload.tsx";
import styles from "./Deliverables.module.css";

export function ArtifactDelivery({ value }: { value: unknown }) {
  if (!value || typeof value !== "object") return null;
  const payload = value as Record<string, unknown>;
  const report =
    payload.report && typeof payload.report === "object"
      ? (payload.report as Partial<ResultReport>)
      : null;
  const files = Array.isArray(payload.files)
    ? payload.files.filter(
        (file): file is ArtifactFile =>
          !!file &&
          typeof file === "object" &&
          typeof file.file_id === "string" &&
          typeof file.name === "string" &&
          typeof file.size === "number" &&
          typeof file.sha256 === "string",
      )
    : [];
  return (
    <>
      {report && (
        <section className={styles.report} aria-label="交付报告">
          <h3>报告</h3>
          {typeof report.body === "string" && <Markdown text={report.body} />}
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
      {files.length > 0 && (
        <section className={styles.section} aria-label="交付文件">
          <h3>文件</h3>
          <ul className={styles.files}>
            {files.map((file) => (
              <li key={file.file_id}>
                <strong>{file.name}</strong>
                <span className={styles.caption}>
                  {new Intl.NumberFormat("zh-CN").format(file.size)} 字节
                </span>
                <FileDownload fileId={file.file_id} name={file.name} />
                <details>
                  <summary>文件校验</summary>
                  <code>SHA-256 {file.sha256}</code>
                </details>
              </li>
            ))}
          </ul>
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
