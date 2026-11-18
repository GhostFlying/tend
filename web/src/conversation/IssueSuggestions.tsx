import { useId, useState, type KeyboardEvent, type RefObject } from "react";
import { useIssueSearch } from "../issues/useIssueSearch.ts";
import styles from "./IssueSuggestions.module.css";

export function issueSuggestionTerm(text: string, caret: number) {
  const before = text.slice(0, caret);
  const match = /(?<![\p{L}\p{N}_/])([Tt][1-9][0-9]*|[Tt]:[^\n:]+)$/u.exec(before);
  if (!match) return null;
  const reference = match[0];
  const term = reference[1] === ":" ? reference.slice(2).trim() : reference;
  if (!term) return null;
  const end =
    reference[1] === ":" ? caret : caret + (/^[0-9]*/.exec(text.slice(caret))?.[0].length ?? 0);
  if (reference[1] !== ":" && /^[0-9A-Za-z_]/.test(text.slice(end))) return null;
  return { term, start: match.index, end };
}

export function useIssueSuggestions(
  text: string,
  input: RefObject<HTMLTextAreaElement | null>,
  write: (text: string) => void,
  enabled: boolean,
) {
  const listID = useId();
  const [position, setPosition] = useState<number | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const range = enabled && position !== null ? issueSuggestionTerm(text, position) : null;
  const found = useIssueSearch(range?.term ?? "");
  const items = found?.page?.items?.filter((item) => item.short_code) ?? [];
  const clear = () => {
    setPosition(null);
    setSelected(null);
  };
  const update = (element: HTMLTextAreaElement, changed = false) => {
    const next = element.selectionStart === element.selectionEnd ? element.selectionEnd : null;
    if (changed || position !== next) {
      setPosition(next);
      setSelected(null);
    }
  };
  const choose = (code: string) => {
    if (!range) return;
    const rest = text.slice(range.end);
    const separator = rest === "" || /^[\p{L}\p{N}_]/u.test(rest) ? " " : "";
    write(text.slice(0, range.start) + code + separator + rest);
    clear();
    requestAnimationFrame(() => {
      input.current?.focus();
      const caret = range.start + code.length + separator.length;
      input.current?.setSelectionRange(caret, caret);
    });
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!range || event.nativeEvent.isComposing) return;
    if (event.key === "Escape") {
      event.preventDefault();
      clear();
    } else if (items.length && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      setSelected(
        (index) => ((index ?? (step === 1 ? -1 : 0)) + step + items.length) % items.length,
      );
    } else if (items.length && selected !== null && event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      const item = items[selected];
      if (item?.short_code) choose(item.short_code);
    }
  };
  return {
    update,
    onKeyDown,
    attributes: {
      "aria-controls": items.length ? listID : undefined,
      "aria-autocomplete": "list" as const,
      "aria-activedescendant": selected === null ? undefined : `${listID}-${selected}`,
    },
    list: items.length > 0 && (
      <ul id={listID} className={styles.list} aria-label="Issue 引用建议">
        {items.map((item, index) => (
          <li key={item.issue_id}>
            <button
              type="button"
              id={`${listID}-${index}`}
              data-selected={selected === index}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => choose(item.short_code!)}
            >
              <strong>{item.short_code}</strong> {item.title}
            </button>
          </li>
        ))}
      </ul>
    ),
  };
}
