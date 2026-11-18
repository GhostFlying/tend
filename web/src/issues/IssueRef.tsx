import type { ComponentProps, ReactNode } from "react";
import { href, Link } from "../app/router.ts";
import { shortIssueCode, useIssueIdentity } from "./identity.ts";
import styles from "../conversation/TaskRef.module.css";

export function IssueLink({
  id,
  shortCode,
  suffix = "",
  children,
  ...props
}: Omit<ComponentProps<typeof Link>, "to"> & {
  id: string;
  shortCode?: string | undefined;
  suffix?: string;
  children: ReactNode;
}) {
  const code = shortCode || shortIssueCode(id);
  const identity = useIssueIdentity(code ? "" : id);
  const canonical = code || (identity && identity !== "missing" ? identity.short_code : id);
  return (
    <Link to={href({ page: "issue", id: canonical }) + suffix} {...props}>
      {children}
    </Link>
  );
}

/** Candidate aliases remain readable as text until Tend confirms the Issue. */
export function IssueRef({ id }: { id: string }) {
  const issue = useIssueIdentity(id);
  if (!issue || issue === "missing") return <>{id}</>;
  return (
    <Link className={styles.chip} to={{ page: "issue", id: issue.short_code }} title={issue.title}>
      <span className={styles.title}>{issue.short_code}</span>
    </Link>
  );
}
