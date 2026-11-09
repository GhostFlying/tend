import { describe, expect, it } from "vitest";
import { resolveFile, sourceFileIds, textFileFormat } from "./files.ts";

const file = { file_id: "file_example", name: "screen shot.png", size: 1, sha256: "hash" };
describe("immutable report references", () => {
  it("resolves only exact IDs or unique manifest names", () => {
    expect(resolveFile(file.file_id, [file])).toBe(file);
    expect(resolveFile("./screen%20shot.png", [file])).toBe(file);
    expect(resolveFile(file.name, [file, { ...file, file_id: "other" }])).toBeUndefined();
    for (const path of [
      "https://example.com/screen shot.png",
      "/tmp/screen shot.png",
      "../screen shot.png",
      "data:image/png;base64,AAAA",
      "missing.png",
      "%bad",
    ]) {
      expect(resolveFile(path, [file])).toBeUndefined();
    }
  });
  it("deduplicates precise original-file references without inventing IDs", () => {
    const id = "file_01M5M3ME31875GDA8F7P5XTD74";
    expect(
      sourceFileIds([`原图 ${id}`, id, "file_example", "profile_01M5M3ME31875GDA8F7P5XTD74"]),
    ).toEqual([id]);
  });
});

it("offers a text presentation format without selecting a primary report", () => {
  expect(textFileFormat({ ...file, name: "analysis.MD" })).toBe("markdown");
  expect(textFileFormat({ ...file, name: "无后缀", media_type: "text/plain; charset=utf-8" })).toBe(
    "text",
  );
  expect(textFileFormat({ ...file, name: "evidence.json" })).toBe("text");
  expect(textFileFormat(file)).toBeNull();
  expect(textFileFormat({ ...file, name: "report.pdf" })).toBeNull();
});
