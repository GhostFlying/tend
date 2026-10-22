import { readFile } from "node:fs/promises";
import { expect, test } from "vitest";
import { generate, outputPath } from "./genapi.ts";

test("the committed API types match the schema", async () => {
  const committed = await readFile(outputPath, "utf8");
  // On failure, run `make gen` and then `pnpm gen` in web/.
  expect(committed).toBe(await generate());
});
