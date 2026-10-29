// The main agent's replies as Markdown. markdown-it parses with HTML off,
// and the tokens become React elements from a fixed set of tags, so no
// markup in a reply reaches the page as HTML. Task IDs in text become task
// chips (D72); code blocks are monospace with a copy button.
import { FilePreview } from "../tasks/FilePreview.tsx";
import { resolveFile, type PreviewFile } from "../tasks/files.ts";
import MarkdownIt from "markdown-it";
import type { Token } from "markdown-it";
import { createElement, Fragment, memo, useMemo, useState, type ReactNode } from "react";
import { splitTaskIds } from "./model.ts";
import styles from "./Markdown.module.css";
import { IssueRef } from "../issues/IssueRef.tsx";
import { splitWorkItemIds } from "../workitems/model.ts";
import { TaskRef } from "./TaskRef.tsx";

// linkify turns bare URLs into links; validateLink keeps javascript: and
// the like out of hrefs.
const md = new MarkdownIt({ html: false, linkify: true, typographer: false });

/** The tags a token may become; anything else keeps only its content. */
const tags = new Set([
  "p",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "ul",
  "ol",
  "li",
  "blockquote",
  "strong",
  "em",
  "s",
  "a",
  "table",
  "thead",
  "tbody",
  "tr",
  "th",
  "td",
  "hr",
  "br",
]);

export const Markdown = memo(function Markdown({
  text,
  files,
}: {
  text: string;
  files?: readonly PreviewFile[];
}) {
  const nodes = useMemo(() => render(md.parse(text, {}), { next: 0 }, files), [text, files]);
  return <div className={styles.md}>{nodes}</div>;
});

/** Hands out element keys, unique within one reply. */
interface Keys {
  next: number;
}

function render(tokens: readonly Token[], keys: Keys, files?: readonly PreviewFile[]): ReactNode[] {
  const root: ReactNode[] = [];
  const stack: { token: Token; children: ReactNode[]; previewLink?: boolean }[] = [];
  const add = (...nodes: ReactNode[]) => (stack.at(-1)?.children ?? root).push(...nodes);

  for (const token of tokens) {
    if (token.nesting === 1) {
      stack.push({ token, children: [] });
      continue;
    }
    if (token.nesting === -1) {
      const open = stack.pop();
      if (open) {
        // A linked image becomes an authenticated preview, not nested buttons
        // inside an external anchor that also navigates when the preview opens.
        add(
          open.previewLink ? (
            <Fragment key={keys.next++}>{open.children}</Fragment>
          ) : (
            element(open.token, open.children, keys.next++)
          ),
        );
      }
      continue;
    }
    switch (token.type) {
      case "inline":
        add(...render(token.children ?? [], keys, files));
        break;
      case "text":
        add(...withTaskRefs(token.content, keys));
        break;
      case "code_inline":
        add(<code key={keys.next++}>{withTaskRefs(token.content, keys)}</code>);
        break;
      case "softbreak":
        add("\n");
        break;
      case "hardbreak":
        add(<br key={keys.next++} />);
        break;
      case "fence":
      case "code_block":
        add(<CodeBlock key={keys.next++} code={token.content} />);
        break;
      case "hr":
        add(<hr key={keys.next++} />);
        break;
      case "image": {
        const file = resolveFile(String(token.attrGet("src") ?? ""), files ?? []);
        if (file) {
          for (const parent of stack) if (parent.token.tag === "a") parent.previewLink = true;
        }
        add(
          file ? (
            <FilePreview key={`${keys.next++}:${file.file_id}`} file={file} compact />
          ) : (
            token.content || token.attrGet("alt") || ""
          ),
        );
        break;
      }
      default:
        // With HTML off, markup arrives as text tokens; anything else
        // shows its source.
        add(token.content);
    }
  }
  return root;
}

function element(token: Token, children: ReactNode[], key: number): ReactNode {
  if (token.hidden || !tags.has(token.tag)) {
    // Paragraphs of tight lists are hidden: their text goes in the item.
    return <Fragment key={key}>{children}</Fragment>;
  }
  const props: Record<string, unknown> = { key };
  if (token.tag === "a") {
    const href = token.attrGet("href");
    if (href) {
      props.href = href;
      props.target = "_blank";
      props.rel = "noopener noreferrer";
    }
  }
  if (token.tag === "ol") {
    const start = Number(token.attrGet("start") ?? 1);
    if (Number.isInteger(start) && start !== 1) {
      props.start = start;
    }
  }
  if (token.tag === "th" || token.tag === "td") {
    const align = /text-align:\s*(left|right|center)/.exec(
      String(token.attrGet("style") ?? ""),
    )?.[1];
    if (align) {
      props.style = { textAlign: align };
    }
  }
  return createElement(token.tag, props, ...children);
}

function withTaskRefs(text: string, keys: Keys): ReactNode[] {
  return splitTaskIds(text).map((part) =>
    typeof part === "string" ? (
      splitWorkItemIds(part).map((run) =>
        typeof run === "string" ? run : <IssueRef key={keys.next++} id={run.workItemId} />,
      )
    ) : (
      <TaskRef key={keys.next++} taskId={part.taskId} />
    ),
  );
}

function CodeBlock({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (await copyText(code.replace(/\n$/, ""))) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  };
  return (
    <div className={styles.code}>
      <button type="button" className={styles.copy} onClick={() => void copy()}>
        {copied ? "已复制" : "复制"}
      </button>
      <pre>
        <code>{code}</code>
      </pre>
    </div>
  );
}

/**
 * Copies text. The clipboard API needs a secure context, which a tailnet
 * without HTTPS is not; a selected text area still works there.
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.append(area);
    area.select();
    try {
      return document.execCommand("copy");
    } finally {
      area.remove();
    }
  }
}
