import { readdir, readFile } from "node:fs/promises";
import { fileURLToPath, pathToFileURL } from "node:url";
import { join, relative } from "node:path";

export function styleViolations(source: string): { line: number; message: string }[] {
  const content = source.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, " "));
  const violations: { line: number; message: string }[] = [];
  const report = (offset: number, message: string) =>
    violations.push({ line: content.slice(0, offset).split("\n").length, message });
  for (const match of content.matchAll(/(?:^|[;{])\s*([\w-]+)\s*:\s*([^;{}]+)/gm)) {
    const property = match[1]!.toLowerCase();
    const value = match[2]!;
    const offset = match.index + match[0].indexOf(value);
    if (property === "z-index" && /\d/.test(value.replace(/var\(--[\w-]+\)/g, "")))
      report(offset, "use a layer token for z-index");
    if ((property === "font-size" || property === "font") && /\b\d*\.?\d+px\b/i.test(value))
      report(offset, "use a type token for font size");
    if (
      /^border(?:-(?:top|bottom)-(?:left|right))?-radius$/.test(property) &&
      [...value.matchAll(/\b(\d*\.?\d+)px\b/gi)].some((entry) => Number(entry[1]) !== 0)
    )
      report(offset, "use a radius token");
    for (const color of value.matchAll(/#[\da-f]{3,8}\b|rgba?\([^)]*\)/gi))
      report(offset + color.index, "define colors in styles/tokens.css");
  }
  return violations;
}

async function checkStyles() {
  const root = fileURLToPath(new URL("../src/", import.meta.url));
  let count = 0;
  for (const file of await readdir(root, { recursive: true })) {
    if (!file.endsWith(".css") || file === join("styles", "tokens.css")) continue;
    const path = join(root, file);
    for (const violation of styleViolations(await readFile(path, "utf8"))) {
      console.error(`${relative(process.cwd(), path)}:${violation.line}: ${violation.message}`);
      count++;
    }
  }
  if (count) process.exitCode = 1;
  else console.log("CSS tokens: all styles passed");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await checkStyles();
