import { expect, test } from "vitest";
import { maxImageBytes, readImages } from "./images.ts";

test("unsupported, oversized and excessive files fail before reading into memory", async () => {
  await expect(
    readImages([new File(["<svg/>"], "image.svg", { type: "image/svg+xml" })]),
  ).rejects.toThrow("支持 PNG、JPEG 和 GIF");
  await expect(
    readImages([new File([new Uint8Array(maxImageBytes + 1)], "huge.png", { type: "image/png" })]),
  ).rejects.toThrow("5 MiB");
  await expect(readImages([new File([], "empty.png", { type: "image/png" })])).rejects.toThrow(
    "不能为空",
  );
  await expect(
    readImages(
      Array.from({ length: 5 }, () => new File(["bytes"], "image.png", { type: "image/png" })),
    ),
  ).rejects.toThrow("最多添加 4 张");
});
