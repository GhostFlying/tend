import { useEffect, useMemo } from "react";
import type { ArtifactDetail } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { ArtifactDelivery } from "../tasks/ArtifactDelivery.tsx";
import { Button } from "../ui/index.ts";
import { WorkItemRead } from "../workitems/live.ts";

export function ReportOriginal({ id }: { id: string }) {
  const { client } = useServices();
  const read = useMemo(() => new WorkItemRead<ArtifactDetail>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => () => read.close(), [read]);
  const open = () => void read.open(`/api/v1/artifacts/${encodeURIComponent(id)}`);
  return (
    <section aria-label="本次决定关联的报告">
      {state.value ? (
        <ArtifactDelivery value={state.value.artifact.ref} />
      ) : (
        <Button onClick={open} disabled={state.loading}>
          {state.loading ? "正在读取报告…" : state.error ? "重试读取报告" : "查看原报告"}
        </Button>
      )}
      {state.error && <p role="status">无法读取原报告。</p>}
    </section>
  );
}
