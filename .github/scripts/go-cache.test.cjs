const { test } = require("node:test");
const assert = require("node:assert/strict");
const { planPrune } = require("./go-cache.cjs");
const cache = (id, key, ref = "refs/heads/main") => ({ id, key, ref, created_at: `2026-11-${String(id).padStart(2, "0")}T00:00:00Z` });

test("retention keeps current main by role, bounds modules, and leaves unrelated caches alone", () => {
  const caches = [
    cache(1, "go-build-v2-api-Linux-X64-go-deps-current"),
    cache(2, "go-build-v2-api-Linux-X64-go-deps-old"),
    cache(3, "go-build-v2-app-Linux-X64-go-deps-old"),
    cache(4, "go-build-v2-app-Linux-X64-go-deps-newer"),
    cache(5, "go-modules-v2-first"), cache(6, "go-modules-v2-second"), cache(7, "go-modules-v2-third"),
    cache(8, "setup-go-Linux-legacy"),
    cache(9, "go-build-v2-api-Linux-X64-go-deps-pr", "refs/pull/1/merge"),
    cache(10, "go-build-v2-unknown-Linux-X64-go-deps-old"),
    cache(11, "go-build-v2-cross-Linux-X64-go-deps-current"),
    cache(12, "go-build-v2-cross-Linux-X64-old-deps-old"),
  ];
  assert.deepEqual(planPrune(caches, "current").map((entry) => entry.id).sort((a, b) => a - b), [2, 3, 5, 12]);
  assert.deepEqual(caches.map((entry) => entry.id), Array.from({ length: 12 }, (_, index) => index + 1));
});

test("empty caches and one snapshot per role require no deletions", () => {
  assert.deepEqual(planPrune([], "current"), []);
  assert.deepEqual(planPrune([cache(1, "go-build-v2-other-Linux-X64-go-deps-old")], "current"), []);
});
