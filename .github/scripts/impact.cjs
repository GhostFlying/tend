// Only successful main baseline workflows may publish shared execution indexes.
const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

const workflow = "impact-baseline.yml";
const prefix = "impact-index-";
const shaPattern = /^[0-9a-f]{40}$/;

function validatePlan(plan, commit) {
  if (plan.Format !== 1 || plan.Commit !== commit || !/^[0-9a-f]{64}$/.test(plan.Fingerprint) ||
      !plan.Go || !Array.isArray(plan.Reasons) || typeof plan.Full !== "boolean" || !Array.isArray(plan.Layout)) {
    throw new Error("Invalid candidate impact plan");
  }
  for (const [pkg, roots] of Object.entries(plan.Go)) {
    if (!pkg || !Array.isArray(roots) || roots.length === 0 || new Set(roots).size !== roots.length ||
        roots.some((root) => !/^(Test|Example|Fuzz)[\w]*$/.test(root))) throw new Error("Invalid planned roots");
  }
  if (plan.Layout.some((script) => !/^test:[a-z-]+$/.test(script))) throw new Error("Invalid layout command");
  return plan;
}

function requireSuccess(results, names) {
  for (const name of names) {
    if (results[name]?.result !== "success") throw new Error(`Required validation did not pass: ${name}`);
  }
}

function refreshDecision(index, mergedPRs, failed = false, compatible = true) {
  if (!index || index.Format !== 1 || !index.VerifiedFull || !shaPattern.test(index.Source?.Commit ?? "")) return { refresh: true, reason: "bootstrap" };
  if (failed) return { refresh: true, reason: "failed or incomplete full validation" };
  if (!compatible) return { refresh: true, reason: "configuration changed" };
  const count = new Set(mergedPRs).size;
  return { refresh: count >= 5, reason: `${count} distinct merged PRs since verified baseline` };
}

async function restoreBaseline({ github, context, core, directory }) {
  fs.mkdirSync(directory, { recursive: true });
  const repo = context.repo;
  let runs;
  try {
    runs = await github.paginate(github.rest.actions.listWorkflowRuns, {
      ...repo, workflow_id: workflow, branch: "main", status: "completed", per_page: 100,
    });
  } catch (error) {
    if (error.status === 404) return { index: undefined, failed: false };
    throw error;
  }
  const trusted = runs.filter((run) => run.head_branch === "main" &&
    run.head_repository?.full_name === `${repo.owner}/${repo.repo}` &&
    ["workflow_run", "workflow_dispatch"].includes(run.event));
  trusted.sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.id - a.id);
  // Successful no-op runs do not erase an earlier failed refresh. Only a
  // published, complete success clears that durable full-validation fallback.
  let failed = false;
  for (const run of trusted) {
    if (run.conclusion === "skipped") continue;
    if (run.conclusion !== "success") {
      failed = true;
      continue;
    }
    const artifacts = await github.paginate(github.rest.actions.listWorkflowRunArtifacts, { ...repo, run_id: run.id, per_page: 100 });
    const artifact = artifacts.find((item) => !item.expired && new RegExp(`^${prefix}[0-9a-f]{40}$`).test(item.name));
    if (!artifact) continue;
    const { data } = await github.rest.actions.downloadArtifact({ ...repo, artifact_id: artifact.id, archive_format: "zip" });
    const archive = path.join(directory, "index.zip");
    fs.writeFileSync(archive, Buffer.from(data));
    const entries = execFileSync("unzip", ["-Z1", archive], { encoding: "utf8" }).trim().split("\n");
    if (entries.length !== 1 || entries[0] !== "index.json") throw new Error("Unexpected baseline archive contents");
    execFileSync("unzip", ["-oq", archive, "-d", directory]);
    fs.rmSync(archive);
    const index = JSON.parse(fs.readFileSync(path.join(directory, "index.json"), "utf8"));
    if (index.Format !== 1 || !index.VerifiedFull || !index.Source?.Clean || !shaPattern.test(index.Source.Commit) || artifact.name !== prefix + index.Source.Commit) {
      throw new Error("Published index is incomplete or dirty");
    }
    core.info(`Restored main baseline ${index.Source.Commit}`);
    return { index, failed };
  }
  return { index: undefined, failed };
}

async function mergedSince({ github, context, baseline, target, git = execFileSync }) {
  if (!shaPattern.test(baseline) || !shaPattern.test(target)) throw new Error("Invalid baseline range");
  git("git", ["merge-base", "--is-ancestor", baseline, target]);
  const commits = git("git", ["rev-list", `${baseline}..${target}`], { encoding: "utf8" }).trim().split("\n").filter(Boolean);
  const inRange = new Set(commits);
  const pulls = new Set();
  for (const commit of commits) {
    const associated = await github.paginate(github.rest.repos.listPullRequestsAssociatedWithCommit, {
      ...context.repo, commit_sha: commit, per_page: 100,
    });
    for (const pr of associated) {
      if (pr.merged_at && pr.base?.ref === "main" &&
          pr.base?.repo?.full_name === `${context.repo.owner}/${context.repo.repo}` && inRange.has(pr.merge_commit_sha)) pulls.add(pr.number);
    }
  }
  return [...pulls].sort((a, b) => a - b);
}

async function prepareRefresh(args) {
  let restored;
  try {
    restored = await restoreBaseline(args);
  } catch (error) {
    args.core.info(`Baseline unavailable; rebuilding with full validation: ${error.message}`);
    restored = { index: undefined, failed: true };
  }
  const target = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  let merged = [];
  let compatible = true;
  if (restored.index) {
    try {
      const current = JSON.parse(execFileSync("go", ["run", "./internal/testkit/impact/cmd", "profile"], { encoding: "utf8" }));
      const profile = restored.index.Source.Profile;
      compatible = ["Go","OS","Arch","Flags","Race"].every(key => profile[key] === current[key]);
      merged = await mergedSince({ ...args, baseline: restored.index.Source.Commit, target });
    } catch (error) {
      compatible = false;
      args.core.info(`Cannot reuse baseline range: ${error.message}`);
    }
  }
  const decision = refreshDecision(restored.index, merged, restored.failed, compatible);
  args.core.setOutput("refresh", String(decision.refresh));
  args.core.setOutput("target", target);
  args.core.setOutput("reason", decision.reason);
  args.core.info(decision.reason);
  return { ...decision, target };
}

async function pruneIndexes({ github, context }) {
  const artifacts = await github.paginate(github.rest.actions.listArtifactsForRepo, { ...context.repo, per_page: 100 });
  const own = [];
  for (const artifact of artifacts) {
    if (!new RegExp(`^${prefix}[0-9a-f]{40}$`).test(artifact.name) || artifact.workflow_run?.head_branch !== "main" || artifact.expired) continue;
    const { data: run } = await github.rest.actions.getWorkflowRun({ ...context.repo, run_id: artifact.workflow_run.id });
    if (run.path === `.github/workflows/${workflow}` && run.head_repository?.full_name === `${context.repo.owner}/${context.repo.repo}` &&
        (run.conclusion === "success" || run.id === context.runId)) own.push(artifact);
  }
  own.sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id - a.id);
  for (const artifact of own.slice(2)) await github.rest.actions.deleteArtifact({ ...context.repo, artifact_id: artifact.id });
}

module.exports = { validatePlan, requireSuccess, refreshDecision, restoreBaseline, mergedSince, prepareRefresh, pruneIndexes };
