import { describe, expect, it, vi, afterEach } from "vitest";
import type { ClipboardEvent } from "react";
import { clipboardFiles, pasteAttachments } from "./clipboard.ts";

const image = (type = "image/png", contents = "one", name = "image.png") =>
  new File([contents], name, { type });
const item = (file: File | null) => ({ kind: "file", getAsFile: () => file }) as DataTransferItem;
const data = (items: DataTransferItem[], files: File[] = []) =>
  ({ items, files }) as unknown as DataTransfer;

afterEach(() => vi.unstubAllGlobals());

describe("clipboard original files", () => {
  it("uses items once when both views contain the same original", () => {
    const file = image();
    expect(clipboardFiles(data([item(file)], [file]))).toEqual([file]);
  });
  it("falls back when items are absent, null or throw", () => {
    const file = image();
    for (const items of [
      [],
      [item(null)],
      [
        {
          kind: "file",
          getAsFile() {
            throw Error();
          },
        } as unknown as DataTransferItem,
      ],
    ]) {
      expect(clipboardFiles(data(items, [file]))).toEqual([file]);
    }
  });
  it("preserves same-name originals, multiple formats, other files and repeated pastes", async () => {
    const files = [
      image(),
      image("image/png", "two"),
      ...["image/jpeg", "image/gif", "image/webp", "image/svg+xml", "application/pdf"].map((type) =>
        image(type),
      ),
    ];
    const event = data(files.map(item), files);
    expect(clipboardFiles(event)).toEqual(files);
    expect(clipboardFiles(event)).toEqual(files);
    expect(await clipboardFiles(event)[1]!.text()).toBe("two");
  });
  it("names unnamed files without changing their bytes or MIME", async () => {
    const [file] = clipboardFiles(data([item(image("image/jpeg", "original", ""))]));
    expect(file!.name).toBe("clipboard.jpg");
    expect(file!.type).toBe("image/jpeg");
    expect(await file!.text()).toBe("original");
  });
  it("does not impose a count or size budget", () => {
    const file = new File([new Uint8Array(33 * 1024 * 1024)], "large.png", { type: "image/png" });
    const files = Array.from({ length: 25 }, () => file);
    expect(clipboardFiles(data([], files))).toEqual(files);
  });
});

function setup(files: File[], text = "", items = files.map(item)) {
  const input = {
    value: "草稿SELECT尾部",
    selectionStart: 2,
    selectionEnd: 8,
    setRangeText(text: string, start: number, end: number) {
      this.value = this.value.slice(0, start) + text + this.value.slice(end);
      this.selectionStart = this.selectionEnd = start + text.length;
    },
    setSelectionRange: vi.fn(),
  };
  const event = {
    clipboardData: { ...data(items, files), getData: () => text },
    currentTarget: input,
    preventDefault: vi.fn(),
  } as unknown as ClipboardEvent<HTMLTextAreaElement>;
  const options = { blocked: false, addFiles: vi.fn(), write: vi.fn(), warn: vi.fn() };
  vi.stubGlobal("document", { activeElement: input });
  vi.stubGlobal("requestAnimationFrame", (callback: () => void) => callback());
  return { event, options, input };
}

describe("attachment paste", () => {
  it("leaves plain text and HTML-only paste to the browser", () => {
    const { event, options } = setup([], "普通文本");
    pasteAttachments(event, options);
    expect(event.preventDefault).not.toHaveBeenCalled();
    expect(options.write).not.toHaveBeenCalled();
    expect(options.addFiles).not.toHaveBeenCalled();
  });
  it("inserts mixed text at the current selection and restores the caret", () => {
    const { event, options, input } = setup([image()], "图片说明");
    pasteAttachments(event, options);
    expect(options.addFiles).toHaveBeenCalledOnce();
    expect(event.preventDefault).toHaveBeenCalledOnce();
    expect(options.write).toHaveBeenCalledWith("草稿图片说明尾部");
    expect(input.setSelectionRange).toHaveBeenCalledWith(6, 6);
  });
  it("does not touch text or selection for a pure image", () => {
    const { event, options, input } = setup([image()]);
    pasteAttachments(event, options);
    expect(options.write).not.toHaveBeenCalled();
    expect(input.setSelectionRange).not.toHaveBeenCalled();
    expect(options.addFiles).toHaveBeenCalledOnce();
  });
  it("does not consume a paste while disabled or saving", () => {
    const { event, options } = setup([image()], "text");
    pasteAttachments(event, { ...options, blocked: true });
    expect(options.addFiles).not.toHaveBeenCalled();
    expect(event.preventDefault).not.toHaveBeenCalled();
  });
  it("warns on unreadable attachments without consuming text", () => {
    const { event, options } = setup([], "text", [item(null)]);
    pasteAttachments(event, options);
    expect(options.warn).toHaveBeenCalledOnce();
    expect(event.preventDefault).not.toHaveBeenCalled();
  });
  it("reports draft errors without consuming native paste", () => {
    const { event, options } = setup([image()], "text");
    options.addFiles.mockImplementation(() => {
      throw Error("storage unavailable");
    });
    pasteAttachments(event, options);
    expect(options.warn).toHaveBeenCalledOnce();
    expect(event.preventDefault).not.toHaveBeenCalled();
  });
  it("warns when only some file items can be read", () => {
    const file = image();
    const { event, options } = setup([file], "", [item(file), item(null)]);
    pasteAttachments(event, options);
    expect(options.addFiles).toHaveBeenCalledWith([file]);
    expect(options.warn).toHaveBeenCalledOnce();
  });
});
