const fs = require("node:fs");
const { spawnSync } = require("node:child_process");
const path = require("node:path");
const { runTests } = require("../../internal/testkit/impact/web.cjs");

function commands(plan) {
  if (!plan.Web || typeof plan.Web.Run !== "boolean" || typeof plan.Web.Full !== "boolean") throw new Error("Missing Web plan");
  const result = [["install", "--frozen-lockfile"]];
  if (plan.Web.Run) {
    for (const stage of ["typecheck", "lint", "format:check", "style:check"]) result.push([stage]);
    const test = ["exec", "vitest", "run"];
    if (!plan.Web.Full) {
      if (!/^[0-9a-f]{40}$/.test(plan.Web.Base)) throw new Error("Invalid module dependency baseline");
      test.push("--changed", plan.Web.Base);
    }
    result.push(test);
  }
  // Cross-compilation and browser jobs consume this candidate's actual build.
  result.push(["build"]);
  return result;
}

if (require.main === module) {
  const plan = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
  for (const args of commands(plan)) {
    if (args[0] === "exec" && args[1] === "vitest") {
      runTests(plan.Web, { web: path.resolve("web"), report: path.resolve(".local/impact/web-result.json") });
      continue;
    }
    const result = spawnSync("pnpm", ["--dir", "web", ...args], { stdio: "inherit" });
    if (result.error) throw result.error;
    if (result.status !== 0) process.exit(result.status ?? 1);
  }
}
module.exports = { commands };
