import { useState } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { Hosts } from "../hosts/Hosts.tsx";
import { RepositoryEntry } from "../repos/Repositories.tsx";
import { UsageEntry } from "../usage/Usage.tsx";
import { DetailOverlay, DetailPane } from "./DetailOverlay.tsx";
import { ResourceRow } from "./ResourceRow.tsx";
import styles from "./Resources.module.css";

export function ResourceNavigation({
  onRecords,
  onRepos,
  records = false,
}: {
  onRecords(): void;
  onRepos(): void;
  records?: boolean;
}) {
  const { live } = useServices();
  return (
    <nav className={styles.navigation} aria-label="资源与记录">
      <ResourceRow
        current={records}
        onOpen={() => {
          live.tasks.setProject("");
          onRecords();
        }}
      >
        全部任务执行
      </ResourceRow>
      <RepositoryEntry onOpen={onRepos} />
      <UsageEntry />
    </nav>
  );
}

export function ResourceHosts({ connection }: { connection: Connection }) {
  return (
    <div className={styles.hosts}>
      <Hosts connection={connection} />
    </div>
  );
}

export function ResourcesEntry({
  connection,
  onRecords,
  onRepos,
}: {
  connection: Connection;
  onRecords(): void;
  onRepos(): void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button className={styles.entry} aria-haspopup="dialog" onClick={() => setOpen(true)}>
        资源与记录
      </button>
      {open && (
        <DetailOverlay
          task={false}
          label="资源与记录"
          closeLabel="关闭 资源与记录"
          onClose={() => setOpen(false)}
        >
          <DetailPane active>
            <section className={styles.panel}>
              <h2>资源与记录</h2>
              <ResourceNavigation
                onRepos={onRepos}
                onRecords={() => {
                  setOpen(false);
                  onRecords();
                }}
              />
              <details className={styles.hosts}>
                <summary>执行主机状态</summary>
                <Hosts connection={connection} />
              </details>
            </section>
          </DetailPane>
        </DetailOverlay>
      )}
    </>
  );
}
