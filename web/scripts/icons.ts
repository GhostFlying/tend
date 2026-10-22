// Draws the app icons into public/icons: the sidebar's logo, a lower-case
// "t" on the label color, as an SVG and as the PNG sizes the manifest and
// iOS ask for. Run with `pnpm icons`; the output is committed.
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { crc32, deflateSync } from "node:zlib";

const outDir = fileURLToPath(new URL("../public/icons/", import.meta.url));

/** Colors from the light theme: --label behind, --bg in front. */
const background = [15, 17, 21] as const;
const foreground = [255, 255, 255] as const;

// The glyph in a unit square: stem, crossbar and a foot curving right.
const lift = 0.07;
const stem = { x0: 0.43, x1: 0.55, y0: 0.24 - lift, y1: 0.66 - lift };
const bar = { x0: 0.3, x1: 0.7, y0: 0.36 - lift, y1: 0.47 - lift };
const foot = { cx: 0.67, cy: 0.66 - lift, r0: 0.12, r1: 0.24 };
const tail = { x0: 0.67, x1: 0.72, y0: 0.78 - lift, y1: 0.9 - lift };

interface Rect {
  x0: number;
  x1: number;
  y0: number;
  y1: number;
}
const inRect = (r: Rect, x: number, y: number): boolean =>
  x >= r.x0 && x <= r.x1 && y >= r.y0 && y <= r.y1;

function inGlyph(x: number, y: number): boolean {
  if (inRect(stem, x, y) || inRect(bar, x, y) || inRect(tail, x, y)) {
    return true;
  }
  const d = Math.hypot(x - foot.cx, y - foot.cy);
  return x <= foot.cx && y >= foot.cy && d >= foot.r0 && d <= foot.r1;
}

/** radius is the corner radius as a fraction of the size; 0 is full bleed. */
function inTile(x: number, y: number, radius: number): boolean {
  const cx = Math.min(Math.max(x, radius), 1 - radius);
  const cy = Math.min(Math.max(y, radius), 1 - radius);
  return Math.hypot(x - cx, y - cy) <= radius;
}

interface Variant {
  size: number;
  radius: number;
  /** Scales the glyph about the center; maskable icons keep it in the safe zone. */
  scale: number;
}

function render({ size, radius, scale }: Variant): Buffer {
  const samples = 4;
  const rows: Buffer[] = [];
  for (let py = 0; py < size; py++) {
    const row = Buffer.alloc(1 + size * 4);
    for (let px = 0; px < size; px++) {
      let tile = 0;
      let glyph = 0;
      for (let sy = 0; sy < samples; sy++) {
        for (let sx = 0; sx < samples; sx++) {
          const x = (px + (sx + 0.5) / samples) / size;
          const y = (py + (sy + 0.5) / samples) / size;
          if (inTile(x, y, radius)) {
            tile++;
            if (inGlyph(0.5 + (x - 0.5) / scale, 0.5 + (y - 0.5) / scale)) {
              glyph++;
            }
          }
        }
      }
      const g = tile === 0 ? 0 : glyph / tile;
      const o = 1 + px * 4;
      for (let c = 0; c < 3; c++) {
        row[o + c] = Math.round(background[c]! * (1 - g) + foreground[c]! * g);
      }
      row[o + 3] = Math.round((255 * tile) / (samples * samples));
    }
    rows.push(row);
  }
  return png(size, Buffer.concat(rows));
}

function chunk(type: string, data: Buffer): Buffer {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, crc]);
}

function png(size: number, scanlines: Buffer): Buffer {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(scanlines, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function svg(radius: number): string {
  const n = (v: number): string => String(Math.round(v * 1000) / 10);
  const rect = (r: Rect): string =>
    `<rect x="${n(r.x0)}" y="${n(r.y0)}" width="${n(r.x1 - r.x0)}" height="${n(r.y1 - r.y0)}"/>`;
  const { cx, cy, r0, r1 } = foot;
  const arc =
    `<path d="M${n(cx - r1)} ${n(cy)}A${n(r1)} ${n(r1)} 0 0 0 ${n(cx)} ${n(cy + r1)}` +
    `V${n(cy + r0)}A${n(r0)} ${n(r0)} 0 0 1 ${n(cx - r0)} ${n(cy)}Z"/>`;
  const rgb = (c: readonly number[]): string => `rgb(${c.join(",")})`;
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">` +
    `<rect width="100" height="100" rx="${n(radius)}" fill="${rgb(background)}"/>` +
    `<g fill="${rgb(foreground)}">${rect(stem)}${rect(bar)}${rect(tail)}${arc}</g></svg>\n`
  );
}

const rounded = 8 / 26; // the sidebar logo's corner radius
mkdirSync(outDir, { recursive: true });
writeFileSync(`${outDir}icon.svg`, svg(rounded));
writeFileSync(`${outDir}icon-192.png`, render({ size: 192, radius: rounded, scale: 1 }));
writeFileSync(`${outDir}icon-512.png`, render({ size: 512, radius: rounded, scale: 1 }));
writeFileSync(`${outDir}icon-maskable-512.png`, render({ size: 512, radius: 0, scale: 0.8 }));
writeFileSync(`${outDir}apple-touch-icon.png`, render({ size: 180, radius: 0, scale: 1 }));
