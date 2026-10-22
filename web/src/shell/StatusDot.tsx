import styles from "./StatusDot.module.css";

export type Tone = "idle" | "ok" | "warn" | "err" | "run";

/** A state dot, as dsh's StateDot. */
export function StatusDot({ tone = "idle" }: { tone?: Tone }) {
  return <span className={`${styles.dot} ${styles[tone]}`} aria-hidden />;
}
