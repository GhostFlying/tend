const test = require("node:test");
const assert = require("node:assert/strict");
const { validatePlan, requireSuccess, refreshDecision, restoreBaseline, mergedSince, pruneIndexes } = require("./impact.cjs");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { commands } = require("./impact-web.cjs");

const index = { Format: 1, VerifiedFull: true, Source: { Commit: "a".repeat(40) } };
test("refresh counts distinct PRs and advances only from a successful baseline", () => {
  assert.equal(refreshDecision(index, [1, 2, 3, 4, 4]).refresh, false);
  assert.equal(refreshDecision(index, [1, 2, 3, 4, 5]).refresh, true);
  assert.equal(refreshDecision(index, [1, 2, 3, 4, 5, 6]).refresh, true);
  assert.equal(refreshDecision(undefined, []).reason, "bootstrap");
  assert.equal(refreshDecision({ ...index, VerifiedFull: false }, []).refresh, true);
  assert.equal(refreshDecision(index, [], true).refresh, true);
  assert.equal(refreshDecision(index, [], false, false).refresh, true);
  // A retry or PR with several rebased commits must not inflate the threshold.
  assert.equal(refreshDecision(index, [7, 7, 7, 8, 8, 9]).refresh, false);
});
test("planned not-applicable work cannot become an ordinary skipped success", () => {
  requireSuccess({ plan: { result: "success" } }, ["plan"]);
  for (const state of ["skipped", "cancelled", "failure", undefined]) {
    assert.throws(() => requireSuccess({ plan: { result: state } }, ["plan"]));
  }
  assert.throws(() => requireSuccess({}, ["race"]));
});
test("candidate plan must match source identity and contain valid nonempty selections", () => {
  const sha = "a".repeat(40);
  const plan = { Format: 1, Commit: sha, Fingerprint: "b".repeat(64), Full: false, Reasons: [], Go: { "m/p": ["TestOne"] }, Layout: [] };
  validatePlan(plan, sha);
  assert.throws(() => validatePlan(plan, "c".repeat(40)));
  assert.throws(() => validatePlan({ ...plan, Go: { "m/p": [] } }, sha));
  assert.throws(() => validatePlan({ ...plan, Go: { "m/p": ["TestOne", "TestOne"] } }, sha));
  assert.throws(() => validatePlan({ ...plan, Layout: ["test:one;command"] }, sha));
});
test("unaffected Web still builds the candidate; affected modules retain quality checks", () => {
  assert.deepEqual(commands({ Web: { Run: false, Full: false } }), [["install", "--frozen-lockfile"], ["build"]]);
  const selected = commands({ Web: { Run: true, Full: false, Base: "a".repeat(40) } });
  assert.ok(selected.some((args) => args[0] === "typecheck"));
  assert.ok(selected.some((args) => args.includes("--changed")));
  assert.throws(() => commands({ Web: { Run: true, Full: false, Base: "--bad" } }));
});

test("successful no-op refreshes preserve a preceding failed full validation", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "impact-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const runs = [
    { id: 4, conclusion: "skipped", updated_at: "4", event: "workflow_run" },
    { id: 3, conclusion: "success", updated_at: "3", event: "workflow_run" },
    { id: 2, conclusion: "failure", updated_at: "2", event: "workflow_run" },
    { id: 1, conclusion: "success", updated_at: "1", event: "pull_request" },
  ].map((run) => ({ ...run, head_branch: "main", head_repository: { full_name: "o/r" } }));
  const github = {
    rest: { actions: { listWorkflowRuns: "runs", listWorkflowRunArtifacts: "artifacts" } },
    paginate: async (method) => method === "runs" ? runs : [],
  };
  const result = await restoreBaseline({ github, directory, context: { repo: { owner: "o", repo: "r" } }, core: { info() {} } });
  assert.equal(result.index, undefined);
  assert.equal(result.failed, true);
});

test("multi-commit PRs count once and a merge during refresh remains in the next cycle", async () => {
  const commits = ["1", "2", "3", "4", "5", "6"].map((letter) => letter.repeat(40));
  const context = { repo: { owner: "o", repo: "r" } };
  const prs = commits.map((commit, i) => ({ number: i === 0 ? 1 : i, merge_commit_sha: i === 0 ? commits[1] : commit, merged_at: "now", base: { ref: "main", repo: { full_name: "o/r" } } }));
  const github = { rest: { repos: { listPullRequestsAssociatedWithCommit: "prs" } }, paginate: async (_, args) => [prs[commits.indexOf(args.commit_sha)]] };
  const git = (_, args) => args[0] === "rev-list" ? commits.join("\n") : "";
  const merged = await mergedSince({ github, context, git, baseline: "a".repeat(40), target: commits[5] });
  assert.deepEqual(merged, [1, 2, 3, 4, 5]);
  assert.equal(refreshDecision(index, merged).refresh, true);
  // The captured target contains PR 5. PR 6 arrives while it is running.
  const later = { number: 6, merge_commit_sha: "7".repeat(40), merged_at: "now", base: prs[0].base };
  github.paginate = async () => [later];
  const next = await mergedSince({ github, context, git: (_, args) => args[0] === "rev-list" ? later.merge_commit_sha : "", baseline: commits[5], target: later.merge_commit_sha });
  assert.deepEqual(next, [6]);
  assert.equal(refreshDecision({ ...index, Source: { Commit: commits[5] } }, next).refresh, false);
});

test("retention preserves successful trusted indexes and ignores failed or foreign publishers", async () => {
  const removed = [];
  const artifacts = [1, 2, 3, 4, 5].map((id) => ({ id, name: `impact-index-${String(id).repeat(40)}`, created_at: String(id), workflow_run: { id, head_branch: "main" } }));
  const github = {
    paginate: async () => artifacts,
    rest: { actions: {
      listArtifactsForRepo() {},
      getWorkflowRun: async ({ run_id }) => ({ data: { id: run_id, conclusion: run_id === 5 ? "failure" : "success", path: run_id === 4 ? ".github/workflows/foreign.yml" : ".github/workflows/impact-baseline.yml", head_repository: { full_name: "o/r" } } }),
      deleteArtifact: async ({ artifact_id }) => removed.push(artifact_id),
    } },
  };
  await pruneIndexes({ github, context: { repo: { owner: "o", repo: "r" }, runId: 99 } });
  assert.deepEqual(removed, [1]);
});
