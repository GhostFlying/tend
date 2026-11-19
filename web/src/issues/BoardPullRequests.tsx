import { useState } from "react";
import type { IssueBoardPullRequest } from "../api/generated/api.ts";
import { Button, Dialog } from "../ui/index.ts";
import { pullRequestIcons } from "./primerPullRequestIcons.ts";
import styles from "./BoardPullRequests.module.css";

const statusLabels = {
  open: "已打开",
  draft: "草稿",
  merged: "已合入",
  closed: "已关闭（未合入）",
  unknown: "状态待确认",
} as const;

function pullRequestStatus(pr: IssueBoardPullRequest, online: boolean): keyof typeof statusLabels {
  switch (pr.status) {
    case "open":
    case "draft":
    case "merged":
    case "closed":
      return online ? pr.status : "unknown";
    default:
      return "unknown";
  }
}

function PullRequestLink({
  pr,
  online,
  focusKey,
}: {
  pr: IssueBoardPullRequest;
  online: boolean;
  focusKey?: string;
}) {
  const status = pullRequestStatus(pr, online);
  const label = `${pr.repository} #${pr.number} · ${statusLabels[status]}`;
  return (
    <a
      className={styles.link}
      href={pr.url}
      target="_blank"
      rel="noopener noreferrer"
      draggable={false}
      data-board-focus={focusKey}
      aria-label={label}
    >
      <svg
        className={styles.icon}
        data-status={status}
        aria-hidden="true"
        width="16"
        height="16"
        viewBox="0 0 16 16"
        fill="currentColor"
      >
        <path d={pullRequestIcons[status]} />
      </svg>
      {focusKey ? (
        <>
          <span>#{pr.number}</span>
          <span className={styles.tooltip} aria-hidden="true">
            {label}
          </span>
        </>
      ) : (
        <>
          <span className={styles.repository}>{pr.repository}</span>
          <span>#{pr.number}</span>
          <span className={styles.status}>{statusLabels[status]}</span>
        </>
      )}
    </a>
  );
}

export function BoardPullRequests({
  issueID,
  pullRequests,
  online,
}: {
  issueID: string;
  pullRequests: IssueBoardPullRequest[];
  online: boolean;
}) {
  const [open, setOpen] = useState(false);
  const main = pullRequests[0];
  if (!main) return null;
  const extra = pullRequests.length - 1;
  return (
    <div className={styles.controls}>
      <PullRequestLink pr={main} online={online} focusKey={`${issueID}:pr`} />
      {extra > 0 ? (
        <>
          <Button
            variant="ghost"
            className={styles.more}
            aria-label={`查看关联 PR（另有 ${extra} 个）`}
            aria-haspopup="dialog"
            aria-expanded={open}
            data-board-focus={`${issueID}:prs`}
            onClick={() => setOpen(true)}
          >
            +{extra}
          </Button>
          {open ? (
            <Dialog title="关联 PR" open={open} onClose={() => setOpen(false)}>
              <ul className={styles.list}>
                {pullRequests.map((pr) => (
                  <li key={pr.url}>
                    <PullRequestLink pr={pr} online={online} />
                  </li>
                ))}
              </ul>
            </Dialog>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
