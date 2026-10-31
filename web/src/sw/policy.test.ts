import { expect, test } from "vitest";
import { route, staleCaches } from "./policy.ts";

const origin = "https://tend.example";
const shell = new Set(["/index.html", "/assets/index-1a2b.js", "/manifest.webmanifest"]);
const get = (path: string, mode = "cors") => ({ method: "GET", url: origin + path, mode });

test("the API always goes to the network", () => {
  for (const path of ["/api/v1/session", "/api/v1/events?after=3", "/api", "/runner/v1/rpc"]) {
    expect(route(get(path), origin, shell)).toBe("network");
    expect(route(get(path, "navigate"), origin, shell)).toBe("network");
  }
});

test("navigations get the page, the network first", () => {
  expect(route(get("/", "navigate"), origin, shell)).toBe("page");
  for (const path of [
    "/tasks/task_01",
    "/issues/example",
    "/inbox",
    "/inbox/example",
    "/settings",
    "/settings/agents",
    "/chat/example",
    "/repos",
    "/usage",
    "/hosts",
    "/archive",
    "/upgrades/example",
  ])
    expect(route(get(path, "navigate"), origin, shell)).toBe("page");
});

test("only the shell's files come from the cache", () => {
  expect(route(get("/assets/index-1a2b.js"), origin, shell)).toBe("shell");
  expect(route(get("/manifest.webmanifest"), origin, shell)).toBe("shell");
  expect(route(get("/assets/index-old.js"), origin, shell)).toBe("network");
});

test("writes and other origins are left alone", () => {
  expect(route({ method: "POST", url: `${origin}/index.html`, mode: "cors" }, origin, shell)).toBe(
    "network",
  );
  expect(
    route(
      { method: "GET", url: "https://cdn.example/assets/index-1a2b.js", mode: "cors" },
      origin,
      shell,
    ),
  ).toBe("network");
});

test("activation drops the shells of other builds only", () => {
  expect(staleCaches(["tend-shell-a", "tend-shell-b", "other"], "b")).toEqual(["tend-shell-a"]);
});
