import { describe, expect, test } from "vitest";
import { captureAnchor, restoreAnchor } from "./scrollAnchor.ts";

// Geometry changes independently of the anchor implementation, as when the
// browser appends live output and later prepends an older page.
function viewport() {
  const rows = [
    { id: "owner-54", top: 200, height: 90 },
    { id: "owner-55", top: 300, height: 90 },
    { id: "owner-56", top: 400, height: 90 },
  ];
  const root = {
    scrollTop: 250,
    scrollHeight: 4498,
    clientHeight: 709,
    getBoundingClientRect: () => ({ top: 52 }),
    querySelectorAll: () =>
      rows.map((row) => ({
        dataset: { scrollId: row.id },
        getBoundingClientRect: () => ({
          top: 52 + row.top - root.scrollTop,
          bottom: 52 + row.top + row.height - root.scrollTop,
        }),
      })),
  };
  return {
    rows,
    root: root as unknown as HTMLElement,
    top: (id: string) => rows.find((r) => r.id === id)!.top - root.scrollTop,
  };
}

describe("reading anchor", () => {
  test("a delayed older page excludes live growth below the reader", () => {
    const { root, rows, top } = viewport();
    const before = top("owner-55");
    const anchor = captureAnchor(root);
    Object.assign(root, { scrollHeight: root.scrollHeight + 1396 });
    restoreAnchor(root, anchor);
    expect(top("owner-55")).toBe(before);
    rows.forEach((row) => {
      row.top += 4400;
    });
    Object.assign(root, { scrollHeight: root.scrollHeight + 4400 });
    restoreAnchor(root, anchor);
    expect(top("owner-55")).toBe(before);
  });

  test("the reader can replace the anchor by scrolling during the wait", () => {
    const { root, rows, top } = viewport();
    captureAnchor(root);
    root.scrollTop += 100;
    const before = top("owner-56");
    const moved = captureAnchor(root);
    rows.forEach((row) => {
      row.top += 4400;
    });
    restoreAnchor(root, moved);
    expect(top("owner-56")).toBe(before);
  });

  test("a disappearing first row uses another visible message", () => {
    const { root, rows, top } = viewport();
    const before = top("owner-55");
    const anchor = captureAnchor(root);
    rows.shift();
    rows.forEach((row) => {
      row.top += 4400;
    });
    restoreAnchor(root, anchor);
    expect(top("owner-55")).toBe(before);
  });
});

test("a tall earlier row does not eclipse the later visible reading anchor", () => {
  const { root, rows, top } = viewport();
  rows[0]!.height = 1500;
  const before = top("owner-55");
  const anchor = captureAnchor(root);
  rows[0]!.height += 1020;
  rows[1]!.top += 1020;
  rows[2]!.top += 1020;
  restoreAnchor(root, anchor);
  expect(top("owner-55")).toBe(before);
});
