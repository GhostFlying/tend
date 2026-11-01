import { describe, expect, it } from "vitest";
import { navActive, moreItems } from "./navigation.tsx";
import { parse, href } from "../app/router.ts";

describe("grouped navigation", () => {
  it("matches nested pages without activating other groups", () => {
    expect(navActive(parse("/issues/one"), "/issues")).toBe(true);
    expect(navActive(parse("/issues/one"), "/tasks")).toBe(false);
    expect(navActive(parse("/inbox/one"), "/inbox")).toBe(true);
    expect(navActive(parse("/settings/agents"), "/settings")).toBe(true);
    expect(navActive(parse("/chat/one"), "/diagnostics/main")).toBe(true);
    expect(navActive(parse("/repos"), "/more")).toBe(true);
    expect(navActive(parse("/tasks"), "/more")).toBe(true);
    expect(navActive(parse("/upgrades/one"), "/hosts")).toBe(true);
  });
  it("has a shareable More route and resource and diagnostic destinations", () => {
    expect(href(parse("/more"))).toBe("/more");
    expect(moreItems.map((item) => item.path)).toEqual([
      "/tasks",
      "/repos",
      "/usage",
      "/hosts",
      "/diagnostics/main",
      "/settings",
    ]);
  });
});
