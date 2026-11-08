import type { UpdaterSelfObservation } from "../api/generated/api.ts";
import { CopyIdentifier } from "../i18n/identifiers.tsx";
import styles from "./Upgrade.module.css";

export function UpdaterEvidence({
  observation,
}: {
  observation?: UpdaterSelfObservation | undefined;
}) {
  if (!observation) return <p className={styles.note}>更新器：等待本批交接证明。</p>;
  const { receipt, rejection, process, supervised_process: service } = observation;
  const proof = rejection ?? receipt;
  return (
    <section className={styles.evidence} aria-label="更新器交接证明">
      <p>
        更新器：
        {rejection
          ? "准备已拒绝，未执行替换。"
          : receipt
            ? "已取得本批交接证明。"
            : "已观察进程，交接尚未确认。"}
      </p>
      <details>
        <summary>进程与回执</summary>
        <dl>
          {process.pid > 1 && (
            <>
              <dt>最后观察的更新器</dt>
              <dd>
                PID {process.pid} · 启动 {process.start_ticks}
                <br />
                <CopyIdentifier value={process.sha256} />
              </dd>
            </>
          )}
          {service.pid > 1 && (
            <>
              <dt>最后观察的服务</dt>
              <dd>
                PID {service.pid} · 启动 {service.start_ticks}
                <br />
                <CopyIdentifier value={service.sha256} />
              </dd>
            </>
          )}
          {proof && (
            <>
              <dt>原请求</dt>
              <dd>
                <CopyIdentifier value={proof.request_id} />
              </dd>
              <dt>发布集</dt>
              <dd>
                <CopyIdentifier value={proof.release_set_sha256} />
              </dd>
            </>
          )}
          {receipt && !rejection && (
            <>
              <dt>交接时保留的服务</dt>
              <dd>
                PID {receipt.retained_process.pid} · 启动 {receipt.retained_process.start_ticks}
              </dd>
            </>
          )}
        </dl>
      </details>
    </section>
  );
}
