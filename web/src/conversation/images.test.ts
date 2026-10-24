import { expect, test } from "vitest";
import { ComposerDraft } from "./draft.ts";

test("the unified draft accepts arbitrary original files without reading or restricting bytes", () => {
  const draft = new ComposerDraft();
  const files = [
    new File(["<svg/>"], "image.svg", { type: "image/svg+xml" }),
    new File([new Uint8Array(21 * 1024 * 1024)], "large.bin"),
    new File([], "empty.txt"),
    ...Array.from({ length: 5 }, () => new File(["bytes"], "image.gif", { type: "image/gif" })),
  ];
  draft.addFiles(files);
  expect(draft.state.get().files).toEqual(files);
  expect(draft.state.get().files[1]).toBe(files[1]);
});
