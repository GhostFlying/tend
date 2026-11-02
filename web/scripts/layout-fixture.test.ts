import { afterEach, expect, test, vi } from "vitest";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fixtureEnvironment, copyFixtureBuild } from "./layout-fixture.ts";
import { fixtureRoot } from "./navigation.ts";
import { readFileSync } from "node:fs";

afterEach(() => vi.unstubAllEnvs());

test("every CI layout entry uses the isolated build runner", () => {
  const { scripts } = JSON.parse(
    readFileSync(new URL("../package.json", import.meta.url), "utf8"),
  ) as {
    scripts: Record<string, string>;
  };
  for (const [name, command] of Object.entries(scripts)) {
    if (name.startsWith("test:"))
      expect(command).toMatch(/^node scripts\/layout-fixture\.ts scripts\//);
  }
});

test("each fixture owns its assets and cache without inheriting parent identity or proxies", () => {
  const source = {
    ORCA_SESSION: "parent",
    HTTP_PROXY: "proxy",
    https_proxy: "proxy",
    ALL_PROXY: "proxy",
    TEND_LAYOUT_OUTPUT: "screenshots",
  };
  const first = fixtureEnvironment(source, join(tmpdir(), "fixture-one"));
  const second = fixtureEnvironment(source, join(tmpdir(), "fixture-two"));
  expect(first.ORCA_SESSION).toBeUndefined();
  expect(first.HTTP_PROXY).toBeUndefined();
  expect(first.https_proxy).toBeUndefined();
  expect(first.ALL_PROXY).toBeUndefined();
  expect(first.TEND_LAYOUT_OUTPUT).toBe("screenshots");
  expect(first.TEND_LAYOUT_APP_ROOT).not.toBe(second.TEND_LAYOUT_APP_ROOT);
  expect(first.TEND_LAYOUT_CACHE_DIR).not.toBe(second.TEND_LAYOUT_CACHE_DIR);
  expect(source.ORCA_SESSION).toBe("parent");
  vi.stubEnv("TEND_LAYOUT_APP_ROOT", first.TEND_LAYOUT_APP_ROOT!);
  vi.stubEnv("TEND_LAYOUT_CACHE_DIR", first.TEND_LAYOUT_CACHE_DIR!);
  expect(fixtureRoot()).toBe(resolve(first.TEND_LAYOUT_APP_ROOT!));
  expect(fixtureRoot("cache")).toBe(resolve(first.TEND_LAYOUT_CACHE_DIR!));
});

test("fixtures cannot silently read the mutable shared build or dev cache", () => {
  vi.stubEnv("TEND_LAYOUT_APP_ROOT", undefined);
  vi.stubEnv("TEND_LAYOUT_CACHE_DIR", undefined);
  expect(() => fixtureRoot()).toThrow("layout-fixture.ts");
  expect(() => fixtureRoot("cache")).toThrow("layout-fixture.ts");
});

test("workflow artifacts are copied into private assets and missing builds fail", async () => {
  const { mkdtemp, mkdir, writeFile, readFile, rm } = await import("node:fs/promises");
  const directory = await mkdtemp(join(tmpdir(), "tend-copy-test-"));
  try {
    const source = join(directory, "artifact");
    const first = join(directory, "first");
    const second = join(directory, "second");
    await mkdir(source);
    await writeFile(join(source, "index.html"), "current workflow");
    await copyFixtureBuild(source, first);
    await copyFixtureBuild(source, second);
    await writeFile(join(first, "index.html"), "fixture mutation");
    expect(await readFile(join(second, "index.html"), "utf8")).toBe("current workflow");
    expect(await readFile(join(source, "index.html"), "utf8")).toBe("current workflow");
    await expect(
      copyFixtureBuild(join(directory, "missing"), join(directory, "third")),
    ).rejects.toThrow();
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
