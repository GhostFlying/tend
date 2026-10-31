import { useState } from "react";
import type { Connection } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { Hosts } from "../hosts/Hosts.tsx";
import { RepositoryEntry } from "../repos/Repositories.tsx";
import { UsageEntry } from "../usage/Usage.tsx";
import { Sheet } from "../ui/Modal.tsx";
import { navigate } from "../app/router.ts";
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
        <Sheet open title="资源与记录" onClose={() => setOpen(false)}>
          <section className={styles.panel}>
            <ResourceNavigation
              onRepos={() => {
                setOpen(false);
                onRepos();
              }}
              onRecords={() => {
                setOpen(false);
                onRecords();
              }}
            />
            <button
              onClick={() => {
                setOpen(false);
                navigate({ page: "hosts" });
              }}
            >
              执行主机状态
            </button>
            <button
              onClick={() => {
                setOpen(false);
                navigate({ page: "settings" });
              }}
            >
              设置
            </button>
          </section>
        </Sheet>
      )}
    </>
  );
}
