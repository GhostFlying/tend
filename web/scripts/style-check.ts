import { readdir, readFile } from "node:fs/promises";
import { fileURLToPath, pathToFileURL } from "node:url";
import { join, relative } from "node:path";

const namedColors = new Set(
  `aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond
  blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue
  cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey
  darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon
  darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet
  deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen
  fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew
  hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon
  lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey
  lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey
  lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine
  mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen
  mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite
  navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen
  paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple
  rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell
  sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan
  teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen`.split(/\s+/),
);

function mask(text: string): string {
  return text.replace(/[^\n]/g, " ");
}

/** Mask non-declaration content without changing source offsets or line numbers. */
function declarationContent(source: string): string {
  const unquoted = source.replace(
    /\/\*[\s\S]*?\*\/|"(?:\\[\s\S]|[^"\\])*"|'(?:\\[\s\S]|[^'\\])*'/g,
    mask,
  );
  let content = "";
  let cursor = 0;
  for (const url of unquoted.matchAll(/\burl\s*\(/gi)) {
    if (url.index < cursor) continue;
    let end = url.index + url[0].length;
    let depth = 1;
    while (end < unquoted.length && depth > 0) {
      if (unquoted[end] === "\\") {
        end += 2;
        continue;
      }
      if (unquoted[end] === "(") depth++;
      if (unquoted[end] === ")") depth--;
      end++;
    }
    content += unquoted.slice(cursor, url.index) + mask(unquoted.slice(url.index, end));
    cursor = end;
  }
  return content + unquoted.slice(cursor);
}

export function styleViolations(source: string): { line: number; message: string }[] {
  const content = declarationContent(source);
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
    if (property === "font" || property === "font-family") continue;
    const colors = value.replace(/--[\w-]+/g, mask);
    for (const color of colors.matchAll(
      /#[\da-f]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\s*\(|\b[a-z][\w-]*\b/gi,
    )) {
      if (
        color[0].startsWith("#") ||
        color[0].includes("(") ||
        namedColors.has(color[0].toLowerCase())
      )
        report(offset + color.index, "define colors in styles/tokens.css");
    }
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
