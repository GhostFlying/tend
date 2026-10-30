import { useId, type HTMLAttributes, type ReactNode } from "react";
import { StatusDot } from "../shell/StatusDot.tsx";
import styles from "./Surfaces.module.css";

export type Tone = "neutral" | "ok" | "warn" | "err" | "run";
type BadgeProps = HTMLAttributes<HTMLSpanElement> & { tone?: Tone };
export function Badge({ tone = "neutral", className, ...props }: BadgeProps) {
  return (
    <span
      {...props}
      className={[styles.badge, styles[tone], className].filter(Boolean).join(" ")}
    />
  );
}
export function StatusChip({ tone = "neutral", children, ...props }: BadgeProps) {
  return (
    <Badge {...props} tone={tone}>
      <StatusDot tone={tone === "neutral" ? "idle" : tone} />
      {children}
    </Badge>
  );
}
export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div {...props} className={[styles.card, className].filter(Boolean).join(" ")} />;
}
export function Section({
  title,
  actions,
  children,
  className,
  ...props
}: Omit<HTMLAttributes<HTMLElement>, "title"> & { title: ReactNode; actions?: ReactNode }) {
  const id = useId();
  return (
    <section
      aria-labelledby={id}
      {...props}
      className={[styles.section, className].filter(Boolean).join(" ")}
    >
      <header>
        <h2 id={id}>{title}</h2>
        {actions}
      </header>
      {children}
    </section>
  );
}
export function EmptyState({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p {...props} className={[styles.empty, className].filter(Boolean).join(" ")} />;
}
