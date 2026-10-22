// One task's details: a panel on a desktop, a sheet on a phone (F7).
import controls from "./controls.module.css";
import styles from "./Details.module.css";
import { Back, Close } from "./icons.tsx";

export function Details({ onClose }: { onClose: () => void }) {
  return (
    <>
      <div className={styles.head}>
        <button
          type="button"
          className={`${controls.iconBtn} ${styles.back}`}
          onClick={onClose}
          aria-label="返回"
        >
          <Back />
        </button>
        <span className={styles.title}>任务详情</span>
        <button type="button" className={controls.iconBtn} onClick={onClose} aria-label="关闭">
          <Close />
        </button>
      </div>
      <div className={styles.body}>
        <p className={controls.empty}>点任务行或回复里的任务标签，在这里查看任务。</p>
      </div>
    </>
  );
}
