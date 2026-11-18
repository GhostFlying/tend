import { IssueRef } from "./IssueRef.tsx";
import { splitWorkItemIds } from "../workitems/model.ts";

export function IssueReferences({ text }: { text: string }) {
  return splitWorkItemIds(text, "short").map((part, index) =>
    typeof part === "string" ? part : <IssueRef key={index} id={part.workItemId} />,
  );
}
