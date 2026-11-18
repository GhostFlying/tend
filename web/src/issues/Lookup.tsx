import { useState } from "react";
import { Button, Input } from "../ui/index.ts";
import { IssueLink } from "./IssueRef.tsx";
import { useIssueSearch } from "./useIssueSearch.ts";
import styles from "./Lookup.module.css";

export function IssueLookup() {
  const [text, setText] = useState("");
  const [cursors, setCursors] = useState([""]);
  const term = text.trim();
  const result = useIssueSearch(term, cursors.at(-1));
  return (
    <div className={styles.lookup}>
      <Input
        type="search"
        aria-label="查找 Issue"
        placeholder="查找 Issue 编号或标题"
        value={text}
        onChange={(event) => {
          setText(event.target.value);
          setCursors([""]);
        }}
      />
      {term && (
        <div className={styles.results}>
          {result?.error ? (
            <p role="alert">暂时无法查找 Issue</p>
          ) : !result ? (
            <p role="status">正在查找 Issue…</p>
          ) : !result.page?.items?.length ? (
            <p role="status">没有匹配的 Issue</p>
          ) : (
            <ul aria-label="Issue 查找结果">
              {result.page.items.map((issue) => (
                <li key={issue.issue_id}>
                  <IssueLink id={issue.issue_id} shortCode={issue.short_code}>
                    <strong>{issue.short_code}</strong> {issue.title}
                  </IssueLink>
                </li>
              ))}
            </ul>
          )}
          {cursors.length > 1 && (
            <Button variant="link" onClick={() => setCursors(cursors.slice(0, -1))}>
              上一页
            </Button>
          )}
          {result?.page?.next && (
            <Button variant="link" onClick={() => setCursors([...cursors, result.page!.next!])}>
              下一页
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
