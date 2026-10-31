/// <reference types="node" />
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import ts from "typescript";
import { expect, test } from "vitest";

// Source-only syntax (regular expressions, identifiers and comments) is not copy.
// No UI string exceptions are currently needed; register any here with a reason.
const exceptions: Record<string, string> = {};
const adjacent = /[\p{Script=Han}][A-Za-z0-9]|[A-Za-z0-9][\p{Script=Han}]/u;

test("UI literal copy separates Chinese and Latin letters or digits", () => {
  const failures: string[] = [];
  function scan(dir: string) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name !== "generated" && entry.name !== "__tests__") scan(path);
      } else if (/\.tsx?$/.test(path) && !/\.(?:test|spec)\./.test(path)) {
        const source = ts.createSourceFile(
          path,
          readFileSync(path, "utf8"),
          ts.ScriptTarget.Latest,
          true,
        );
        function visit(node: ts.Node) {
          if (
            ts.isStringLiteral(node) ||
            ts.isNoSubstitutionTemplateLiteral(node) ||
            ts.isTemplateHead(node) ||
            ts.isTemplateMiddle(node) ||
            ts.isTemplateTail(node) ||
            ts.isJsxText(node)
          ) {
            const copy = node.text;
            if (adjacent.test(copy) && !exceptions[copy]) {
              failures.push(
                `${path}:${source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1}: ${copy}`,
              );
            }
          }
          ts.forEachChild(node, visit);
        }
        visit(source);
      }
    }
  }
  scan(new URL("..", import.meta.url).pathname);
  expect(failures).toEqual([]);
});
