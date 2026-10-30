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

test.each([
  "hsl(0 100% 50%)",
  "hsla(0, 100%, 50%, 0.5)",
  "hwb(0 0% 0%)",
  "lab(50% 40 59)",
  "lch(50% 40 60)",
  "oklab(50% 0.1 0.2)",
  "oklch(50% 0.1 0.2)",
  "color(display-p3 1 0 0)",
  "HSL(0 100% 50%)",
])("rejects literal color function %s", (color) => {
  expect(styleViolations(`.a {background: ${color};}`)).toEqual([
    { line: 1, message: "define colors in styles/tokens.css" },
  ]);
});

test.each([
  "white",
  "black",
  "red",
  "gray",
  "grey",
  "blue",
  "green",
  "orange",
  "ReBeccAPurple",
  "lightgoldenrodyellow",
])("rejects named color %s", (color) => {
  expect(styleViolations(`.a {border: 1px solid ${color};}`)).toHaveLength(1);
});

test("checks gradient colors and variable fallbacks without matching variable names", () => {
  expect(
    styleViolations(
      `.a {color: var(--red, white); background: linear-gradient(var(--gray), black); outline-color: var(--blue, hsl(0 0% 20%));}`,
    ),
  ).toHaveLength(3);
  expect(
    styleViolations(
      `.a {color: var(--red); background: var(--rgb-black); border-color: var(--hsl-gray);}`,
    ),
  ).toEqual([]);
});

test("allows CSS global keywords, transparency and inherited colors", () => {
  for (const color of ["transparent", "currentColor", "inherit", "initial", "unset", "none"])
    expect(styleViolations(`.a {color: ${color};}`)).toEqual([]);
});

test("ignores quoted strings, URLs and font families while preserving line numbers", () => {
  expect(
    styleViolations(`.a {
  background: url("data:image/svg+xml;white,#fff,rgb(1,2,3)");
  color: red;
  content: "white; hsl(0 0% 50%) { color: blue; }";
  font-family: Red, Black, "White";
  font: var(--text-body) / 1.5 Red;
}`).map((entry) => entry.line),
  ).toEqual([3]);
  expect(
    styleViolations(
      `.a {background: url(data:image/svg+xml;path(red,#fff)); color: var(--label);}`,
    ),
  ).toEqual([]);
});
