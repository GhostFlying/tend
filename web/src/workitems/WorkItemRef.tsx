import { useEffect } from "react";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { columnLabel } from "./model.ts";
import styles from "../conversation/TaskRef.module.css";

export function WorkItemRef({ id }: { id: string }) {
  const { live, openWorkItem } = useServices();
  const cache = useStore(live.workItems.cache);
  const item = cache.get(id);
  useEffect(() => live.workItems.lookUp(id), [live, id]);
  return (
    <button
      type="button"
      className={styles.chip}
      onClick={() => openWorkItem(id)}
      title={`${item ? columnLabel(item.column) + " · " : "工作项 · "}${id}`}
    >
      <span className={styles.title}>{item?.title ?? id}</span>
    </button>
  );
}
