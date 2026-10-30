import { expect, test } from "vitest";
import { styleViolations } from "./style-check.ts";

test("reports raw declarations with source line numbers, including fallbacks", () => {
  expect(
    styleViolations(`/* font-size: 14px; */
.a {
  font-size: 14px;
  z-index: -2;
  border-radius: 0 8px;
  color: var(--danger, #abc);
  background: rgb(0 0 0 / 30%);
}`).map((entry) => entry.line),
  ).toEqual([3, 4, 5, 6, 7]);
});

test("accepts tokens, zero and circular radii, and non-color values", () => {
  expect(
    styleViolations(
      `.a {font-size: var(--text-body); z-index: var(--z-dialog); border-radius: 0 50%; color: currentColor; background: transparent; padding: 10px;}`,
    ),
  ).toEqual([]);
});

test("checks font shorthand and individual corner radii", () => {
  expect(
    styleViolations(`.a {font: 13px/20px sans-serif; border-top-left-radius: 8px;}`),
  ).toHaveLength(2);
});

test("rejects numeric layer expressions and fallbacks", () => {
  expect(styleViolations(".a {z-index: calc(var(--z-sheet) + 1);}")).toHaveLength(1);
  expect(styleViolations(".a {z-index: var(--z-sheet, 3);}")).toHaveLength(1);
});
