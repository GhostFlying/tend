const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

// Resolve through Vitest's module graph before execution. A zero-match graph
// result widens to the full inventory; no generic passWithNoTests is used.
function runTests(plan, { web, pnpm = "pnpm", report, execute = execFileSync }) {
  if (!plan || typeof plan.Full !== "boolean" || (!plan.Full && !/^[0-9a-f]{40}$/.test(plan.Base))) throw new Error("Invalid Web selection");
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "tend-impact-web-"));
  try {
    const relativeFile = (file) => {
      if (typeof file !== "string") throw new Error("Missing Vitest source");
      const relative = path.relative(web, file);
      if (path.isAbsolute(relative) || relative.startsWith("..") || !relative) throw new Error("Vitest source outside Web");
      return relative.split(path.sep).join("/");
    };
    const inventory = path.join(tmp, "files.json");
    const list = (changed) => {
      fs.rmSync(inventory, { force: true });
      const args = ["exec", "vitest", "list", "--filesOnly", `--json=${inventory}`];
      if (changed) args.push("--changed", plan.Base);
      execute(pnpm, args, { cwd: web, stdio: "inherit" });
      const files = JSON.parse(fs.readFileSync(inventory, "utf8"));
      if (!Array.isArray(files)) throw new Error("Invalid Vitest inventory");
      return files.map((item) => {
        if (typeof item.file !== "string" || item.projectName) throw new Error("Unsupported Vitest inventory entry");
        return relativeFile(item.file);
      });
    };
    let files = list(!plan.Full);
    let reason = plan.Full ? "full Web inventory" : "Vitest module dependency graph";
    if (files.length === 0 && !plan.Full) {
      files = list(false);
      reason = "empty dependency selection widened to full Web inventory";
    }
    if (files.length === 0 || new Set(files).size !== files.length) throw new Error("Empty or duplicate Web inventory");
    const result = { Files: files.sort(), Reason: reason, ExecutedFiles: [], FailedFiles: [], Completed: false };
    if (report) fs.writeFileSync(report, JSON.stringify(result));
    process.stdout.write(`[impact] ${reason}: ${files.length} files\n`);
    const native = path.join(tmp, "vitest.json");
    let failure;
    try {
      execute(pnpm, ["exec", "vitest", "run", ...files, "--reporter=default", "--reporter=json", `--outputFile=${native}`], { cwd: web, stdio: "inherit" });
    } catch (error) {
      failure = error;
    }
    if (!fs.existsSync(native)) throw failure ?? new Error("Vitest produced no execution report");
    const actual = JSON.parse(fs.readFileSync(native, "utf8"));
    if (!Array.isArray(actual.testResults)) throw new Error("Invalid Vitest execution report");
    for (const item of actual.testResults) {
      const file = relativeFile(item.name);
      result.ExecutedFiles.push(file);
      if (item.assertionResults?.some((assertion) => assertion.status === "failed")) result.FailedFiles.push(file);
    }
    if (files.some((file) => !result.ExecutedFiles.includes(file))) throw new Error("Planned Vitest file did not execute");
    result.ExecutedFiles.sort();
    result.FailedFiles.sort();
    result.Completed = true;
    if (report) fs.writeFileSync(report, JSON.stringify(result));
    if (failure) throw failure;
    if (actual.success !== true) throw new Error("Vitest execution did not succeed");
    return result;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}
if (require.main === module) {
  const [, , web, pnpm, plan, report] = process.argv;
  runTests(JSON.parse(fs.readFileSync(plan, "utf8")), { web, pnpm, report });
}
module.exports = { runTests };
