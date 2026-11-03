import type { ClipboardEvent } from "react";

/** Consume one view of an event: items and files commonly represent the same files. */
export function clipboardFiles(
  data: Pick<DataTransfer, "items" | "files">,
  warn: (message: string) => void = () => {},
): File[] {
  const files: File[] = [];
  let unreadable = false;
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind !== "file") continue;
    try {
      const file = item.getAsFile();
      if (file) files.push(file);
      else unreadable = true;
    } catch {
      unreadable = true;
      // A browser may expose an unreadable item but still provide the files view.
    }
  }
  const selected = files.length ? files : Array.from(data.files ?? []);
  if (unreadable && (files.length || !selected.length)) {
    warn("部分剪贴板附件无法读取，请检查待发送附件，重新复制或使用添加附件补充");
  }
  return selected.map((file) => {
    if (file.name.trim()) return file;
    const extension = {
      "image/png": "png",
      "image/jpeg": "jpg",
      "image/gif": "gif",
      "image/webp": "webp",
    }[file.type];
    return new File([file], extension ? `clipboard.${extension}` : "clipboard", {
      type: file.type,
      lastModified: file.lastModified,
    });
  });
}

/** Keep native text paste unless files were actually accepted into the draft. */
export function pasteAttachments(
  event: ClipboardEvent<HTMLTextAreaElement>,
  options: {
    blocked: boolean;
    addFiles(files: File[]): void;
    write(text: string): void;
    warn(message: string): void;
  },
): void {
  if (options.blocked) return;
  try {
    const files = clipboardFiles(event.clipboardData, options.warn);
    if (!files.length) return;
    const text = event.clipboardData.getData("text/plain");
    options.addFiles(files);
    event.preventDefault();
    if (text) {
      const input = event.currentTarget;
      // Chromium clears the textarea undo stack on setRangeText. insertText
      // participates in the native editing history, including selected text.
      const inserted =
        document.activeElement === input && document.execCommand?.("insertText", false, text);
      if (!inserted) input.setRangeText(text, input.selectionStart, input.selectionEnd, "end");
      const caret = input.selectionEnd;
      options.write(input.value);
      requestAnimationFrame(() => {
        if (document.activeElement === input) input.setSelectionRange(caret, caret);
      });
    }
  } catch {
    options.warn("无法添加剪贴板附件，请重试或使用添加附件");
  }
}
